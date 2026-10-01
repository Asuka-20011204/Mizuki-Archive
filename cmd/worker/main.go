// Package main 启动独立的单进程资料处理 Worker。
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/joho/godotenv"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"mizuki-archive/internal/cache"
	appconfig "mizuki-archive/internal/config"
	"mizuki-archive/internal/queue"
	"mizuki-archive/internal/repository"
	"mizuki-archive/internal/service"
)

// loadEnvironment 读取可选的本地配置文件，系统环境变量仍由 godotenv 保持优先。
func loadEnvironment() error {
	path := os.Getenv("MIZUKI_ENV_FILE")
	if path == "" {
		path = ".env"
	}
	if err := godotenv.Load(path); err != nil && os.Getenv("MIZUKI_ENV_FILE") != "" {
		return err
	}
	return nil
}

// required 读取 Worker 启动所需配置，缺失时立即停止而不是静默空转。
func required(name string) string {
	value := os.Getenv(name)
	if value == "" {
		log.Fatalf("missing required environment variable: %s", name)
	}
	return value
}

// requiredSecret 读取 Worker 所需秘密，支持环境变量和 Docker Secrets 文件。
func requiredSecret(name string) string {
	value, err := appconfig.ReadSecret(name)
	if err != nil {
		log.Fatalf("cannot read secret configuration %s: %v", name, err)
	}
	if value == "" {
		log.Fatalf("missing required secret configuration: %s", name)
	}
	return value
}

// main 连接 MySQL、执行迁移并启动单 Worker 循环；任务状态以数据库为唯一真相源。
func main() {
	if err := loadEnvironment(); err != nil {
		log.Fatalf("cannot load environment file: %v", err)
	}
	workers, err := processingWorkerCount(os.Getenv("PROCESSING_WORKERS"))
	if err != nil {
		log.Fatal(err)
	}
	dsn, err := mysql.ParseDSN(requiredSecret("MYSQL_DSN"))
	if err != nil {
		log.Fatal("invalid MYSQL_DSN")
	}
	dsn.ParseTime = true
	dsn.Loc = time.UTC
	database, err := gorm.Open(gormmysql.Open(dsn.FormatDSN()), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		log.Fatal("cannot initialize database")
	}
	connection, err := database.DB()
	if err != nil {
		log.Fatal("cannot access database pool")
	}
	defer connection.Close()
	connection.SetMaxOpenConns(4)
	connection.SetConnMaxLifetime(5 * time.Minute)
	startup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := connection.PingContext(startup); err != nil {
		log.Fatal("database is unavailable")
	}
	store := repository.NewMySQL(database)
	if os.Getenv("PROCESSING_DELIVERY_MODE") == "rabbit" {
		store.EnableOutbox()
	}
	if autoMigrateEnabled() {
		// 本地开发允许 Worker 自动准备结构；生产 Worker 只使用迁移服务完成后的数据库。
		if err := store.Migrate(startup); err != nil {
			log.Fatal("database schema initialization failed")
		}
	}
	var sharedCache cache.Cache
	if redisURL := os.Getenv("REDIS_URL"); redisURL != "" {
		redisClient, redisErr := cache.NewRedis(redisURL, os.Getenv("REDIS_KEY_PREFIX"))
		if redisErr != nil {
			log.Printf("Redis cache disabled in worker: %v", redisErr)
		} else {
			sharedCache = redisClient
			defer redisClient.Close()
		}
	}
	processor, err := service.NewProcessingWithCache(store, store, required("APP_DATA_DIR"), sharedCache)
	if err != nil {
		log.Fatal("cannot prepare processing service")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if os.Getenv("PROCESSING_DELIVERY_MODE") == "rabbit" {
		if err := runRabbitWorker(ctx, processor, workers); err != nil && !errors.Is(err, context.Canceled) {
			log.Fatalf("RabbitMQ processing worker stopped: %v", err)
		}
		return
	}
	log.Printf("archive processing worker started in database mode with %d slots", workers)
	if err := processor.RunPool(ctx, workers, time.Second); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("processing worker stopped: %v", err)
	}
}

// autoMigrateEnabled 让生产 Worker 避免使用拥有 DDL 权限的数据库账号。
func autoMigrateEnabled() bool {
	return !strings.EqualFold(strings.TrimSpace(os.Getenv("APP_AUTO_MIGRATE")), "false")
}

// processingWorkerCount 限制每个进程的并发处理数，避免大文件处理耗尽内存。
func processingWorkerCount(value string) (int, error) {
	if value == "" {
		return 2, nil
	}
	count, err := strconv.Atoi(value)
	if err != nil || count < 1 || count > 4 {
		return 0, fmt.Errorf("PROCESSING_WORKERS must be between 1 and 4")
	}
	return count, nil
}

// runRabbitWorker 在连接中断后重新建立发布与消费通道；数据库模式仍可作为明确的回滚开关。
func runRabbitWorker(ctx context.Context, processor *service.Processing, workers int) error {
	prefetch := workers
	if value := os.Getenv("RABBITMQ_PREFETCH"); value != "" {
		if parsed, parseErr := strconv.Atoi(value); parseErr == nil && parsed > 0 && parsed <= workers {
			prefetch = parsed
		} else {
			return errors.New("RABBITMQ_PREFETCH must be between 1 and PROCESSING_WORKERS")
		}
	}
	rabbitURL, err := appconfig.ReadSecret("RABBITMQ_URL")
	if err != nil {
		return err
	}
	if rabbitURL == "" {
		return errors.New("missing RABBITMQ_URL")
	}
	for ctx.Err() == nil {
		broker, err := queue.NewRabbitMQ(rabbitURL, os.Getenv("RABBITMQ_EXCHANGE"), os.Getenv("RABBITMQ_QUEUE"), os.Getenv("RABBITMQ_DEAD_QUEUE"))
		if err == nil {
			session, cancel := context.WithCancel(ctx)
			published := make(chan struct{})
			go func() {
				defer close(published)
				publishOutbox(session, cancel, processor, broker)
			}()
			log.Println("archive processing worker connected to RabbitMQ")
			err = broker.ConsumeJobs(session, func(ctx context.Context, jobID string) (bool, error) {
				err := processor.RunJob(ctx, jobID)
				return err == nil, err
			}, prefetch)
			cancel()
			_ = broker.Close()
			<-published
		}
		if ctx.Err() != nil {
			break
		}
		// 不记录连接字符串或错误中的潜在凭据；重试期间任务继续保留在事务 Outbox。
		log.Println("RabbitMQ unavailable; reconnecting after 2 seconds")
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
	return ctx.Err()
}

// publishOutbox 持续扫描事务 Outbox；RabbitMQ 暂时不可用时只记录脱敏错误并等待下一轮。
func publishOutbox(ctx context.Context, cancel context.CancelFunc, processor *service.Processing, broker *queue.RabbitMQ) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	repairTicker := time.NewTicker(30 * time.Second)
	defer repairTicker.Stop()
	for {
		_, err := processor.RunOutboxOnce(ctx, broker)
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("processing outbox publish deferred: %v", err)
		}
		if broker.PublisherClosed() {
			cancel()
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-repairTicker.C:
			if _, err := processor.RepairOutbox(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Println("processing outbox repair deferred")
			}
		case <-ticker.C:
		}
	}
}
