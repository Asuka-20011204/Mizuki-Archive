package repository

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"mizuki-archive/internal/model"
)

// TestDuplicateMySQLIsolation 在独立测试库验证旧卡片回填、跨用户隔离、更新和软删除过滤。
func TestDuplicateMySQLIsolation(t *testing.T) {
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
		t.Fatal(err)
	}
	connection, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	if err := NewMySQL(database).Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := NewMySQL(database).Migrate(context.Background()); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	// 模拟索引与列已建成、迁移记录尚未落库时中断，重新执行应再次确认结构并安全返回。
	if err := database.Exec("DELETE FROM schema_migrations WHERE version = ?", 13).Error; err != nil {
		t.Fatal(err)
	}
	if err := NewMySQL(database).Migrate(context.Background()); err != nil {
		t.Fatalf("resume interrupted duplicate migration: %v", err)
	}
	transaction := database.Begin()
	if transaction.Error != nil {
		t.Fatal(transaction.Error)
	}
	t.Cleanup(func() { _ = transaction.Rollback().Error })
	store := NewMySQL(transaction)
	ownerA, _ := newTagID()
	ownerB, _ := newTagID()
	for _, owner := range []string{ownerA, ownerB} {
		if err := transaction.Table("users").Create(map[string]any{"id": owner, "username": owner, "created_at": time.Now().UTC()}).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctxA := WithUserID(context.Background(), ownerA)
	ctxB := WithUserID(context.Background(), ownerB)
	fileA, _ := newTagID()
	fileB, _ := newTagID()
	hash := strings.Repeat("a", 64)
	for _, file := range []struct {
		ctx context.Context
		id  string
	}{{ctxA, fileA}, {ctxB, fileB}} {
		if err := store.SaveResource(file.ctx, model.Resource{ID: file.id, Name: "私有文件", OriginalName: "test.txt", SHA256: hash, StorageKey: file.id, Kind: "text", MIME: "text/plain", CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	if hint, err := store.FindFileDuplicate(ctxB, hash, fileB); err != nil || hint != nil {
		t.Fatalf("cross-user file hint: %+v, %v", hint, err)
	}
	secondFile, _ := newTagID()
	if err := store.SaveResource(ctxA, model.Resource{ID: secondFile, Name: "第二份", OriginalName: "test.txt", SHA256: hash, StorageKey: secondFile, Kind: "text", MIME: "text/plain", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if hint, err := store.FindFileDuplicate(ctxA, hash, secondFile); err != nil || hint == nil || hint.ID != fileA {
		t.Fatalf("file duplicate hint: %+v, %v", hint, err)
	}
	if err := transaction.Table("resources").Where("id = ?", fileA).Update("deleted_at", time.Now().UTC()).Error; err != nil {
		t.Fatal(err)
	}
	if hint, err := store.FindFileDuplicate(ctxA, hash, secondFile); err != nil || hint != nil {
		t.Fatalf("soft-deleted file hint: %+v, %v", hint, err)
	}
	firstCard, _ := newTagID()
	otherCard, _ := newTagID()
	location := "https://example.org/a?b=2&a=1"
	for _, card := range []struct {
		ctx context.Context
		id  string
	}{{ctxA, firstCard}, {ctxB, otherCard}} {
		if err := store.CreateExternalResource(card.ctx, model.ExternalResource{ID: card.id, Title: "旧卡片", Location: location, ResourceType: "doc", Status: "pending", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	key := model.ExternalLinkKey("HTTPS://EXAMPLE.ORG:443/a?a=1&b=2#ref")
	if hint, err := store.FindExternalDuplicate(ctxB, key, otherCard); err != nil || hint != nil {
		t.Fatalf("cross-user card hint: %+v, %v", hint, err)
	}
	// 模拟旧版本已存在但尚未填充摘要的卡片；再次迁移不得漏掉它。
	if err := transaction.Table("external_resources").Where("id = ?", firstCard).Update("location_key", nil).Error; err != nil {
		t.Fatal(err)
	}
	if err := backfillExternalLinkKeys(transaction); err != nil {
		t.Fatal(err)
	}
	secondCard, _ := newTagID()
	if err := store.CreateExternalResource(ctxA, model.ExternalResource{ID: secondCard, Title: "第二张", Location: "https://example.org/a?a=1&b=2", ResourceType: "doc", Status: "pending", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if hint, err := store.FindExternalDuplicate(ctxA, key, secondCard); err != nil || hint == nil || hint.ID != firstCard {
		t.Fatalf("backfilled card hint: %+v, %v", hint, err)
	}
	if err := store.UpdateExternalResource(ctxA, model.ExternalResource{ID: firstCard, Title: "已修改", Location: "local-folder", ResourceType: "doc", Status: "pending", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if hint, err := store.FindExternalDuplicate(ctxA, key, secondCard); err != nil || hint != nil {
		t.Fatalf("updated card kept stale key: %+v, %v", hint, err)
	}
	if hint, err := store.FindExternalDuplicate(context.Background(), key, secondCard); err == nil || hint != nil {
		t.Fatalf("unscoped lookup should fail closed: %+v, %v", hint, err)
	}
}
