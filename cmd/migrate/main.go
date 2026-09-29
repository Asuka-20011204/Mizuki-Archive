package main

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"os"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/joho/godotenv"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"mizuki-archive/internal/repository"
)

// loadEnvironment 读取可选的本地环境文件；生产环境由容器环境直接注入迁移账号。
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

// required 读取迁移命令必需的环境变量；缺失时立即停止，避免对错误数据库执行迁移。
func required(name string) string {
	value := os.Getenv(name)
	if value == "" {
		log.Fatalf("missing required environment variable: %s", name)
	}
	return value
}

// main 使用独立迁移 DSN 执行数据库结构和初始管理员归属准备，完成后退出。
func main() {
	if err := loadEnvironment(); err != nil {
		log.Fatalf("cannot load environment file: %v", err)
	}
	dsn, err := mysql.ParseDSN(required("MIGRATION_DSN"))
	if err != nil {
		log.Fatal("invalid MIGRATION_DSN")
	}
	dsn.ParseTime = true
	dsn.Loc = time.UTC
	database, err := gorm.Open(gormmysql.Open(dsn.FormatDSN()), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		log.Fatal("cannot initialize migration database")
	}
	connection, err := database.DB()
	if err != nil {
		log.Fatal("cannot access migration database pool")
	}
	defer connection.Close()
	connection.SetMaxOpenConns(2)
	connection.SetConnMaxLifetime(5 * time.Minute)
	startup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := connection.PingContext(startup); err != nil {
		log.Fatal("migration database is unavailable")
	}
	store := repository.NewMySQL(database)
	adminUsername := required("APP_ADMIN_USERNAME")
	adminPasswordHash := []byte(required("APP_ADMIN_PASSWORD_HASH"))
	if err := repository.ValidatePasswordHash(adminPasswordHash); err != nil {
		log.Fatal("invalid admin password hash")
	}
	if err := store.Migrate(startup); err != nil {
		log.Fatal("database schema migration failed")
	}
	admin, err := store.EnsureAdminUser(startup, adminUsername, adminPasswordHash)
	if err != nil {
		log.Fatal("cannot initialize admin identity")
	}
	if err := store.FinalizeOwnership(startup, admin.ID); err != nil {
		log.Fatal("cannot finalize data ownership")
	}
	log.Printf("database migration completed")
}
