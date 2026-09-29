package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"mizuki-archive/internal/model"
)

// TestMySQLSetFavorite 仅在显式提供隔离测试库时运行，验证 GORM 对重复布尔更新和缺失资料的真实行为。
func TestMySQLSetFavorite(t *testing.T) {
	dsn := os.Getenv("MIZUKI_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("设置 MIZUKI_TEST_MYSQL_DSN 指向隔离测试库后运行")
	}
	config, err := driver.ParseDSN(dsn)
	if err != nil || !strings.HasPrefix(config.DBName, "mizuki_test_") {
		t.Fatal("测试 DSN 必须指向 mizuki_test_ 前缀的独立数据库")
	}
	config.ParseTime = true
	config.Loc = time.UTC
	database, err := gorm.Open(mysql.Open(config.FormatDSN()), &gorm.Config{})
	if err != nil {
		t.Fatalf("连接测试数据库失败: %v", err)
	}
	connection, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	// 清理回调只关闭本用例创建的连接，不删除测试库或其他资料。
	t.Cleanup(func() { _ = connection.Close() })
	ctx := context.Background()
	if err := NewMySQL(database).Migrate(ctx); err != nil {
		t.Fatalf("建立测试表失败: %v", err)
	}
	if err := NewMySQL(database).Migrate(ctx); err != nil {
		t.Fatalf("重复执行迁移失败: %v", err)
	}
	var migrationCount int64
	if err := database.Table("schema_migrations").Count(&migrationCount).Error; err != nil || migrationCount != 6 {
		t.Fatalf("迁移记录数量 = %d, error=%v", migrationCount, err)
	}
	transaction := database.Begin()
	if transaction.Error != nil {
		t.Fatal(transaction.Error)
	}
	// 清理回调撤销本用例插入的资料，测试表保留给后续集成测试复用。
	t.Cleanup(func() { _ = transaction.Rollback().Error })
	store := NewMySQL(transaction)
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		t.Fatal(err)
	}
	id := hex.EncodeToString(idBytes)
	resource := model.Resource{ID: id, Name: "example.txt", OriginalName: "example.txt", Kind: "text", MIME: "text/plain", StorageKey: id, SHA256: strings.Repeat("0", 64), CreatedAt: time.Now().UTC()}
	if err := store.SaveResource(ctx, resource); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetResource(ctx, id)
	if err != nil || loaded.Tags == nil || len(loaded.Tags) != 0 {
		t.Fatalf("无标签资料必须返回空数组，得到 %#v, %v", loaded.Tags, err)
	}
	for _, favorite := range []bool{true, true, false} {
		updated, err := store.SetFavorite(ctx, id, favorite)
		if err != nil || updated.Favorite != favorite {
			t.Fatalf("设置 favorite=%t 得到 %#v, %v", favorite, updated, err)
		}
	}
	if _, err := store.SetFavorite(ctx, strings.Repeat("f", 32), true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("缺失资料应返回 ErrNotFound，得到 %v", err)
	}
	updated, err := store.ReplaceResourceTags(ctx, id, []string{"go", "redis"})
	if err != nil || strings.Join(updated.Tags, ",") != "go,redis" {
		t.Fatalf("保存标签得到 %#v, %v", updated.Tags, err)
	}
	filtered, err := store.ListResources(ctx, model.ListQuery{Tag: "redis", Limit: 10})
	if err != nil || len(filtered) != 1 || filtered[0].ID != id {
		t.Fatalf("标签筛选得到 %#v, %v", filtered, err)
	}
	cleared, err := store.ReplaceResourceTags(ctx, id, nil)
	if err != nil || len(cleared.Tags) != 0 {
		t.Fatalf("清空标签得到 %#v, %v", cleared.Tags, err)
	}
	renamed, err := store.UpdateResourceName(ctx, id, "renamed.txt")
	if err != nil || renamed.Name != "renamed.txt" || renamed.OriginalName != "example.txt" {
		t.Fatalf("修改名称得到 %#v, %v", renamed, err)
	}
	if _, err := store.DeleteResource(ctx, id); err != nil {
		t.Fatalf("软删除失败: %v", err)
	}
	if _, err := store.DeleteResource(ctx, id); err != nil {
		t.Fatalf("重复软删除应可用于重试文件清理: %v", err)
	}
	if _, err := store.GetResource(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("软删除后仍可读取: %v", err)
	}
	resources, err := store.ListResources(ctx, model.ListQuery{Limit: 30})
	if err != nil {
		t.Fatalf("软删除后读取列表失败: %v", err)
	}
	for _, listed := range resources {
		if listed.ID == id {
			t.Fatalf("软删除资料仍在列表: %#v", listed)
		}
	}
}
