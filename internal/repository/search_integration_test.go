package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"mizuki-archive/internal/model"
)

// TestSearchMySQLIsolation 用独立 MySQL 测试库验证用户隔离、标签/备注/正文匹配与字面通配符。
func TestSearchMySQLIsolation(t *testing.T) {
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
		t.Fatalf("repeat saved view migration: %v", err)
	}
	if err := database.Exec("DELETE FROM schema_migrations WHERE version = ?", 14).Error; err != nil {
		t.Fatal(err)
	}
	if err := NewMySQL(database).Migrate(context.Background()); err != nil {
		t.Fatalf("resume saved view migration after DDL: %v", err)
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
	for _, file := range []struct {
		ctx      context.Context
		id, name string
	}{{ctxA, fileA, "站内手册"}, {ctxB, fileB, "他人秘密"}} {
		if err := store.SaveResource(file.ctx, model.Resource{ID: file.id, Name: file.name, OriginalName: file.name + ".txt", Kind: "text", MIME: "text/plain", SHA256: strings.Repeat("a", 64), StorageKey: file.id, CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.ReplaceResourceTags(ctxA, fileA, []string{"专用标签"}); err != nil {
		t.Fatal(err)
	}
	jobID, _ := newTagID()
	assetID, _ := newTagID()
	now := time.Now().UTC()
	if err := transaction.Table("processing_jobs").Create(map[string]any{"id": jobID, "resource_id": fileA, "type": model.ProcessingTypeExtractText, "source_sha256": strings.Repeat("a", 64), "status": model.ProcessingStatusSucceeded, "available_at": now, "created_at": now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := transaction.Table("derived_assets").Create(map[string]any{"id": assetID, "job_id": jobID, "resource_id": fileA, "kind": model.DerivedAssetText, "name": "extracted.txt", "storage_key": assetID, "mime": "text/plain", "size_bytes": 8, "sha256": strings.Repeat("b", 64), "content_text": "唯一正文词", "created_at": now}).Error; err != nil {
		t.Fatal(err)
	}
	cardID, _ := newTagID()
	card := model.ExternalResource{ID: cardID, Title: "外部笔记", Location: "https://example.org/data", ResourceType: "course", Note: "只在备注的线索", Tags: []string{"卡片标签"}, Status: "pending", CreatedAt: now, UpdatedAt: now}
	if err := store.CreateExternalResource(ctxA, card); err != nil {
		t.Fatal(err)
	}
	if files, err := store.SearchFiles(ctxA, model.SearchFilter{Kind: "text", Tag: "专用标签", OrganizationStatus: "pending"}, 21, 0); err != nil || len(files) != 1 || files[0].ID != fileA {
		t.Fatalf("file combination filter: %+v, %v", files, err)
	}
	if cards, err := store.SearchExternal(ctxA, model.SearchFilter{Kind: "course", Tag: "卡片标签", OrganizationStatus: "pending"}, 21, 0); err != nil || len(cards) != 1 || cards[0].ID != cardID {
		t.Fatalf("card combination filter: %+v, %v", cards, err)
	}
	if files, err := store.SearchFiles(ctxA, model.SearchFilter{OrganizationStatus: "organized"}, 21, 0); err != nil || len(files) != 0 {
		t.Fatalf("wrong file organization status: %+v, %v", files, err)
	}
	if cards, err := store.SearchExternal(ctxA, model.SearchFilter{OrganizationStatus: "organized"}, 21, 0); err != nil || len(cards) != 0 {
		t.Fatalf("wrong card organization status: %+v, %v", cards, err)
	}
	viewID, _ := newTagID()
	view := model.SavedSearch{ID: viewID, Name: "尚未整理", Filter: model.SearchFilter{Source: "file", OrganizationStatus: "pending"}, CreatedAt: now}
	if err := store.CreateSavedSearch(ctxA, view); err != nil {
		t.Fatalf("save view: %v", err)
	}
	view.ID, _ = newTagID()
	if err := store.CreateSavedSearch(ctxA, view); !errors.Is(err, ErrSavedSearchConflict) {
		t.Fatalf("duplicate name: %v", err)
	}
	if views, err := store.ListSavedSearches(ctxB); err != nil || len(views) != 0 {
		t.Fatalf("other user's saved views: %+v, %v", views, err)
	}
	if err := store.DeleteSavedSearch(ctxB, viewID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other user's delete: %v", err)
	}
	if views, err := store.ListSavedSearches(ctxA); err != nil || len(views) != 1 || views[0].Filter.OrganizationStatus != "pending" {
		t.Fatalf("owner's saved views: %+v, %v", views, err)
	}
	if err := store.DeleteSavedSearch(ctxA, viewID); err != nil {
		t.Fatalf("owner delete: %v", err)
	}
	for index := 0; index < 30; index++ {
		id, _ := newTagID()
		view.ID, view.Name = id, fmt.Sprintf("筛选 %02d", index)
		if err := store.CreateSavedSearch(ctxA, view); err != nil {
			t.Fatalf("create saved view %d: %v", index, err)
		}
	}
	view.ID, _ = newTagID()
	view.Name = "超过上限"
	if err := store.CreateSavedSearch(ctxA, view); !errors.Is(err, ErrSavedSearchLimit) {
		t.Fatalf("31st saved view: %v", err)
	}
	for _, term := range []string{"专用标签", "唯一正文词"} {
		items, err := store.SearchFiles(ctxA, model.SearchFilter{Query: term}, 21, 0)
		if err != nil || len(items) != 1 || items[0].ID != fileA {
			t.Fatalf("file search %q: %+v, %v", term, items, err)
		}
		if items[0].StorageKey != fileA || len(items[0].Tags) != 1 {
			t.Fatalf("file metadata lost: %+v", items[0])
		}
	}
	for _, term := range []string{"只在备注的线索", "卡片标签"} {
		items, err := store.SearchExternal(ctxA, model.SearchFilter{Query: term}, 21, 0)
		if err != nil || len(items) != 1 || items[0].ID != cardID {
			t.Fatalf("card search %q: %+v, %v", term, items, err)
		}
	}
	for _, term := range []string{"他人秘密", "%", "_"} {
		files, err := store.SearchFiles(ctxA, model.SearchFilter{Query: term}, 21, 0)
		if err != nil || len(files) != 0 {
			t.Fatalf("file leak/wildcard %q: %+v, %v", term, files, err)
		}
		cards, err := store.SearchExternal(ctxA, model.SearchFilter{Query: term}, 21, 0)
		if err != nil || len(cards) != 0 {
			t.Fatalf("card leak/wildcard %q: %+v, %v", term, cards, err)
		}
	}
	if files, err := store.SearchFiles(ctxB, model.SearchFilter{Query: "唯一正文词"}, 21, 0); err != nil || len(files) != 0 {
		t.Fatalf("cross-user content leak: %+v, %v", files, err)
	}
	if cards, err := store.SearchExternal(ctxB, model.SearchFilter{Query: "卡片标签"}, 21, 0); err != nil || len(cards) != 0 {
		t.Fatalf("cross-user tag leak: %+v, %v", cards, err)
	}
	if err := transaction.Table("resources").Where("id = ?", fileA).Update("deleted_at", now).Error; err != nil {
		t.Fatal(err)
	}
	if files, err := store.SearchFiles(ctxA, model.SearchFilter{Query: "唯一正文词"}, 21, 0); err != nil || len(files) != 0 {
		t.Fatalf("soft-deleted file leak: %+v, %v", files, err)
	}
	if _, err := store.SearchFiles(context.Background(), model.SearchFilter{Query: "手册"}, 21, 0); err == nil {
		t.Fatal("unscoped file search must fail closed")
	}
}
