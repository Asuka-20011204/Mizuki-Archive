package repository

import (
	"context"
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

// TestExternalResourceMySQLIsolation 在隔离 MySQL 中验证卡片、收件箱迁移和跨用户读写删除。
func TestExternalResourceMySQLIsolation(t *testing.T) {
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
	db, err := gorm.Open(mysql.Open(config.FormatDSN()), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	if err := NewMySQL(db).Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := NewMySQL(db).Migrate(context.Background()); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	// 模拟新增两列和索引后尚未写入迁移记录的崩溃窗口，重试不能因重复 DDL 失败。
	if err := db.Exec("DELETE FROM schema_migrations WHERE version = ?", 11).Error; err != nil {
		t.Fatal(err)
	}
	if err := NewMySQL(db).Migrate(context.Background()); err != nil {
		t.Fatalf("resume interrupted inbox migration: %v", err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	store := NewMySQL(tx)
	ownerA := strings.Repeat("a", 32)
	ownerB := strings.Repeat("b", 32)
	for _, id := range []string{ownerA, ownerB} {
		if err := tx.Table("users").Create(map[string]any{"id": id, "username": id, "created_at": time.Now().UTC()}).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctxA := WithUserID(context.Background(), ownerA)
	ctxB := WithUserID(context.Background(), ownerB)
	now := time.Now().UTC()
	value := model.ExternalResource{ID: strings.Repeat("c", 32), Title: "private", Location: "https://example.org/a", ResourceType: "doc", Status: "pending", Tags: []string{"study"}, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateExternalResource(ctxB, value); err != nil {
		t.Fatal(err)
	}
	if items, err := store.ListExternalResources(ctxB, "%"); err != nil || len(items) != 0 {
		t.Fatalf("wildcard search must be literal: %+v, %v", items, err)
	}
	if _, err := store.GetExternalResource(ctxA, value.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other user get: %v", err)
	}
	if list, err := store.ListExternalResources(ctxA, ""); err != nil || len(list) != 0 {
		t.Fatalf("other user list: %+v, %v", list, err)
	}
	if _, err := store.UpdateExternalResource(ctxA, value); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other user update: %v", err)
	}
	if err := store.DeleteExternalResource(ctxA, value.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other user delete: %v", err)
	}
	value.Tags = []string{"new"}
	value.Note = "owner edit"
	if updated, err := store.UpdateExternalResource(ctxB, value); err != nil || updated.Note != "owner edit" || len(updated.Tags) != 1 || updated.Tags[0] != "new" || updated.OrganizationStatus != "pending" {
		t.Fatal(err)
	}
	owned, err := store.GetExternalResource(ctxB, value.ID)
	if err != nil || owned.Note != "owner edit" || len(owned.Tags) != 1 || owned.Tags[0] != "new" {
		t.Fatalf("owner get after edit: %+v, %v", owned, err)
	}
	if _, err := store.GetExternalResource(context.Background(), value.ID); err == nil {
		t.Fatal("unscoped read should fail closed")
	}
	file := model.Resource{ID: strings.Repeat("d", 32), Name: "local.txt", OriginalName: "local.txt", Kind: "text", MIME: "text/plain", SHA256: strings.Repeat("0", 64), StorageKey: strings.Repeat("d", 32), CreatedAt: now}
	if err := store.SaveResource(ctxB, file); err != nil {
		t.Fatal(err)
	}
	if files, err := store.ListPendingResources(ctxA, 51, 0); err != nil || len(files) != 0 {
		t.Fatalf("other user pending files: %+v, %v", files, err)
	}
	if external, err := store.ListPendingExternalResources(ctxA, 51, 0); err != nil || len(external) != 0 {
		t.Fatalf("other user pending external: %+v, %v", external, err)
	}
	if files, err := store.ListPendingResources(ctxB, 51, 0); err != nil || len(files) != 1 || files[0].OrganizationStatus != "pending" {
		t.Fatalf("owner pending file: %+v, %v", files, err)
	}
	if external, err := store.ListPendingExternalResources(ctxB, 51, 0); err != nil || len(external) != 1 || external[0].OrganizationStatus != "pending" {
		t.Fatalf("owner pending external: %+v, %v", external, err)
	}
	if err := store.SetResourceOrganizationStatus(ctxA, file.ID, "organized"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other user file update: %v", err)
	}
	if err := store.SetExternalOrganizationStatus(ctxA, value.ID, "organized"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other user external update: %v", err)
	}
	if err := store.SetResourceOrganizationStatus(ctxB, file.ID, "organized"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetExternalOrganizationStatus(ctxB, value.ID, "organized"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetResourceOrganizationStatus(ctxB, file.ID, "organized"); err != nil {
		t.Fatalf("idempotent file update: %v", err)
	}
	if files, err := store.ListPendingResources(ctxB, 51, 0); err != nil || len(files) != 0 {
		t.Fatalf("completed file still pending: %+v, %v", files, err)
	}
	if external, err := store.ListPendingExternalResources(ctxB, 51, 0); err != nil || len(external) != 0 {
		t.Fatalf("completed external still pending: %+v, %v", external, err)
	}
	if item, err := store.GetExternalResource(ctxB, value.ID); err != nil || item.Status != "pending" || item.OrganizationStatus != "organized" {
		t.Fatalf("link status changed after organization: %+v, %v", item, err)
	}
}
