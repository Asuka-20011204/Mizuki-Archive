// Package main 启动独立的单进程资料处理 Worker。
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/joho/godotenv"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
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

// main 连接 MySQL、执行迁移并启动单 Worker 循环；任务状态以数据库为唯一真相源。
func main() {
	if err := loadEnvironment(); err != nil {
		log.Fatalf("cannot load environment file: %v", err)
	}
	dsn, err := mysql.ParseDSN(required("MYSQL_DSN"))
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
	if err := store.Migrate(startup); err != nil {
		log.Fatal("database schema initialization failed")
	}
	processor, err := service.NewProcessing(store, store, required("APP_DATA_DIR"))
	if err != nil {
		log.Fatal("cannot prepare processing service")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Println("archive processing worker started")
	if err := processor.RunLoop(ctx, time.Second); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("processing worker stopped: %v", err)
	}
}
