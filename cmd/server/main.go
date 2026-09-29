package main

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/joho/godotenv"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"mizuki-archive/internal/controller"
	"mizuki-archive/internal/repository"
	"mizuki-archive/internal/service"
)

// loadEnvironment 读取可选的 .env 文件；系统环境变量保持更高优先级，便于生产环境覆盖本地配置。
func loadEnvironment() error {
	path, explicit := os.LookupEnv("MIZUKI_ENV_FILE")
	if !explicit || path == "" {
		path = ".env"
		explicit = false
	}
	if err := godotenv.Load(path); err != nil {
		if !explicit && errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	return nil
}

// required 读取必需环境变量；缺失时立即中止启动，避免服务带着不完整配置运行。
func required(name string) string {
	value := os.Getenv(name)
	if value == "" {
		log.Fatalf("missing required environment variable: %s", name)
	}
	return value
}

// main 是 API 服务的启动入口：读取配置、接入 MySQL、装配 MVC 依赖并优雅停机。
// 前端入口在 web/src/main.ts；生成管理员密码哈希的独立命令在 cmd/hash-password。
func main() {
	if err := loadEnvironment(); err != nil {
		log.Fatalf("cannot load environment file: %v", err)
	}
	address := os.Getenv("APP_LISTEN_ADDR")
	if address == "" {
		address = "127.0.0.1:8080"
	}
	origin := required("APP_ORIGIN")
	// Origin 必须是明确的站点根地址；写请求依赖它做来源校验，不能接受路径或模糊值。
	parsedOrigin, err := url.Parse(origin)
	if err != nil || (parsedOrigin.Scheme != "http" && parsedOrigin.Scheme != "https") || parsedOrigin.Host == "" || parsedOrigin.Path != "" || parsedOrigin.RawQuery != "" || parsedOrigin.Fragment != "" {
		log.Fatal("APP_ORIGIN must be a scheme and host only")
	}
	if parsedOrigin.Scheme == "http" && !strings.HasPrefix(parsedOrigin.Host, "localhost:") && !strings.HasPrefix(parsedOrigin.Host, "127.0.0.1:") {
		log.Fatal("non-local APP_ORIGIN requires HTTPS")
	}
	dataDir := required("APP_DATA_DIR")
	// 只解析 DSN 再交给 GORM，不在日志中输出可能包含数据库密码的原始字符串。
	dsn, err := mysql.ParseDSN(required("MYSQL_DSN"))
	if err != nil {
		log.Fatal("invalid MYSQL_DSN")
	}
	dsn.ParseTime = true
	dsn.Loc = time.UTC
	// 数据库连接、初始表结构都在接收 HTTP 请求之前准备好，启动失败不提供半可用服务。
	database, err := gorm.Open(gormmysql.Open(dsn.FormatDSN()), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		log.Fatal("cannot initialize database")
	}
	connection, err := database.DB()
	if err != nil {
		log.Fatal("cannot access database pool")
	}
	defer connection.Close()
	connection.SetMaxOpenConns(10)
	connection.SetConnMaxLifetime(5 * time.Minute)
	// 设置启动超时：数据库不可用时尽快失败，而不是让浏览器看到一个无法工作的 API。
	startup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := connection.PingContext(startup); err != nil {
		log.Fatal("database is unavailable")
	}
	store := repository.NewMySQL(database)
	if err := store.Migrate(startup); err != nil {
		log.Fatal("database schema initialization failed")
	}
	resources, err := service.NewResources(store, dataDir)
	if err != nil {

		log.Fatal("cannot prepare file storage")

	}
	processingService, err := service.NewProcessing(store, store, dataDir)
	if err != nil {

		log.Fatal("cannot prepare processing service")

	}
	auth, err := service.NewAuth(store, required("APP_ADMIN_USERNAME"), []byte(required("APP_ADMIN_PASSWORD_HASH")))
	if err != nil {
		log.Fatal("invalid admin password hash")
	}
	router, err := controller.New(controller.Config{Resources: resources, Processing: processingService, Auth: auth, Origin: origin, SecureCookie: parsedOrigin.Scheme == "https"})
	if err != nil {
		log.Fatal("cannot initialize HTTP server")
	}
	server := &http.Server{Addr: address, Handler: router, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 90 * time.Second, WriteTimeout: 90 * time.Second, IdleTimeout: 120 * time.Second}
	// 收到终止信号后停止接新请求，让正在处理的请求在限定时间内结束。
	stopped := make(chan os.Signal, 1)
	signal.Notify(stopped, syscall.SIGINT, syscall.SIGTERM)
	// 监听协程把启动后的异常退出转成停机信号；主协程统一走带超时的优雅关闭。
	go func() {
		log.Printf("archive API listening on %s", address)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP server stopped unexpectedly: %v", err)
			stopped <- syscall.SIGTERM
		}
	}()
	<-stopped
	shutdown, finish := context.WithTimeout(context.Background(), 10*time.Second)
	defer finish()
	if err := server.Shutdown(shutdown); err != nil {
		log.Printf("HTTP shutdown: %v", err)
	}
}
