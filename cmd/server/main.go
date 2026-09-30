package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
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
	"mizuki-archive/internal/controller"
	"mizuki-archive/internal/notification"
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

// requiredSecret 读取必需秘密，支持直接环境变量和 Docker Secrets 文件两种部署方式。
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
	dsn, err := mysql.ParseDSN(requiredSecret("MYSQL_DSN"))
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
	adminUsername := required("APP_ADMIN_USERNAME")
	var adminPasswordHash []byte
	var adminUserID string
	if autoMigrateEnabled() {
		// 本地开发默认允许 API 自动迁移；生产编排关闭此开关，由一次性 migrate 服务使用独立账号执行。
		adminPasswordHash = []byte(requiredSecret("APP_ADMIN_PASSWORD_HASH"))
		if err := repository.ValidatePasswordHash(adminPasswordHash); err != nil {
			log.Fatal("invalid admin password hash")
		}
		if err := store.Migrate(startup); err != nil {
			log.Fatal("database schema initialization failed")
		}
		adminUser, ensureErr := store.EnsureAdminUser(startup, adminUsername, adminPasswordHash)
		if ensureErr != nil {
			log.Fatal("cannot migrate admin identity")
		}
		if ensureErr := store.FinalizeOwnership(startup, adminUser.ID); ensureErr != nil {
			log.Fatal("cannot finalize data ownership")
		}
		adminUserID = adminUser.ID
	} else {
		// 生产 API 只读取已初始化的管理员身份，避免运行时账号拥有 DDL 和数据回填权限。
		adminUser, lookupErr := store.GetUserByUsername(startup, adminUsername)
		if errors.Is(lookupErr, repository.ErrNotFound) {
			log.Fatal("admin identity is missing; run the migration command first")
		}
		if lookupErr != nil {
			log.Fatal("cannot load admin identity")
		}
		if len(adminUser.PasswordHash) == 0 {
			log.Fatal("admin identity has no password hash")
		}
		adminPasswordHash = adminUser.PasswordHash
		adminUserID = adminUser.ID
	}
	var sharedCache cache.Cache
	var redisClient *cache.Redis
	if redisURL := os.Getenv("REDIS_URL"); redisURL != "" {
		redisClient, err = cache.NewRedis(redisURL, os.Getenv("REDIS_KEY_PREFIX"))
		if err != nil {
			log.Printf("Redis disabled: %v", err)
		} else {
			sharedCache = redisClient
			defer redisClient.Close()
		}
	}
	resources, err := service.NewResourcesWithCache(store, dataDir, sharedCache)
	if err != nil {

		log.Fatal("cannot prepare file storage")

	}
	processingService, err := service.NewProcessingWithCache(store, store, dataDir, sharedCache)
	if err != nil {

		log.Fatal("cannot prepare processing service")

	}
	if value := os.Getenv("PROCESSING_MAX_OUTSTANDING"); value != "" {
		limit, parseErr := strconv.Atoi(value)
		if parseErr != nil || processingService.SetMaxOutstanding(limit) != nil {
			log.Fatal("PROCESSING_MAX_OUTSTANDING must be between 1 and 1000")
		}
	}
	if os.Getenv("PROCESSING_DELIVERY_MODE") == "rabbit" {
		if err := processingService.EnableOutbox(); err != nil {
			log.Fatal("cannot enable RabbitMQ processing delivery")
		}
		store.EnableOutbox()
	}
	auth, err := service.NewAuth(store, adminUsername, adminPasswordHash)
	if err != nil {
		log.Fatal("invalid admin password hash")
	}
	auth.SetUserID(adminUserID)
	var sharedLimiter cache.RateLimiter
	if sharedCache != nil {
		sharedLimiter = sharedCache
	}
	emailAuth, err := buildEmailAuth(store, auth)
	if err != nil {
		log.Fatal(err)
	}
	externalResources, err := service.NewExternalResources(store)
	if err != nil {
		log.Fatal("cannot prepare external resource service")
	}
	inbox, err := service.NewInbox(store)
	if err != nil {
		log.Fatal("cannot prepare inbox service")
	}
	router, err := controller.New(controller.Config{Resources: resources, ExternalResources: externalResources, Inbox: inbox, Processing: processingService, Auth: auth, EmailAuth: emailAuth, Origin: origin, SecureCookie: parsedOrigin.Scheme == "https", TrustProxyHeaders: trustProxyHeadersEnabled(), RateLimiter: sharedLimiter, Ready: connection.PingContext})
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

// autoMigrateEnabled 保留本地开发的开箱即用体验，并允许生产 API/Worker 关闭启动时 DDL。
func autoMigrateEnabled() bool {
	return !strings.EqualFold(strings.TrimSpace(os.Getenv("APP_AUTO_MIGRATE")), "false")
}

// trustProxyHeadersEnabled 只为受控 Docker 入口打开代理地址读取，直连 API 默认不信任客户端转发头。
func trustProxyHeadersEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("APP_TRUST_PROXY_HEADERS")), "true")
}

// buildEmailAuth 仅在完整配置 SMTP 和验证码密钥时启用邮箱功能，避免暴露无法工作的注册入口。
func buildEmailAuth(store *repository.MySQL, auth *service.Auth) (*service.EmailAuth, error) {
	host, err := appconfig.ReadSecret("SMTP_HOST")
	if err != nil {
		return nil, err
	}
	portValue, err := appconfig.ReadSecret("SMTP_PORT")
	if err != nil {
		return nil, err
	}
	username, err := appconfig.ReadSecret("SMTP_USERNAME")
	if err != nil {
		return nil, err
	}
	password, err := appconfig.ReadSecret("SMTP_PASSWORD")
	if err != nil {
		return nil, err
	}
	from, err := appconfig.ReadSecret("SMTP_FROM")
	if err != nil {
		return nil, err
	}
	secret, err := appconfig.ReadSecret("EMAIL_CODE_SECRET")
	if err != nil {
		return nil, err
	}
	configured := host != "" || portValue != "" || username != "" || password != "" || from != "" || secret != ""
	if !configured {
		return nil, nil
	}
	if host == "" || portValue == "" || username == "" || password == "" || from == "" || len(secret) < 32 {
		return nil, errors.New("SMTP_HOST、SMTP_PORT、SMTP_USERNAME、SMTP_PASSWORD、SMTP_FROM 和 EMAIL_CODE_SECRET 必须完整配置")
	}
	port, err := strconv.Atoi(portValue)
	if err != nil {
		return nil, errors.New("SMTP_PORT 必须是有效端口")
	}
	var sender *notification.SMTP
	caPath := strings.TrimSpace(os.Getenv("SMTP_CA_FILE"))
	if caPath != "" {
		caBytes, readErr := os.ReadFile(filepath.Clean(caPath))
		if readErr != nil {
			return nil, errors.New("read SMTP_CA_FILE failed")
		}
		sender, err = notification.NewSMTPWithRootCA(host, port, username, password, from, caBytes)
	} else {
		sender, err = notification.NewSMTP(host, port, username, password, from)
	}
	if err != nil {
		return nil, fmt.Errorf("invalid SMTP configuration: %w", err)
	}
	emailAuth, err := service.NewEmailAuth(store, store, auth, sender, []byte(secret))
	if err != nil {
		return nil, fmt.Errorf("invalid email authentication configuration: %w", err)
	}
	log.Printf("email verification enabled; SMTP host=%s port=%d", host, port)
	return emailAuth, nil
}
