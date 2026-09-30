package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"mizuki-archive/internal/model"
)

// TestResourceNotesMySQLIsolation 验证迁移、账号范围读写、软删除隐藏和笔记关键词检索。
func TestResourceNotesMySQLIsolation(t *testing.T) {
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
	transaction := database.Begin()
	if transaction.Error != nil {
		t.Fatal(transaction.Error)
	}
	t.Cleanup(func() { _ = transaction.Rollback().Error })
	store := NewMySQL(transaction)
	ownerA := strings.Repeat("a", 32)
	ownerB := strings.Repeat("b", 32)
	for _, owner := range []string{ownerA, ownerB} {
		if err := transaction.Table("users").Create(map[string]any{"id": owner, "username": owner, "created_at": time.Now().UTC()}).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctxA := WithUserID(context.Background(), ownerA)
	ctxB := WithUserID(context.Background(), ownerB)
	resourceID := strings.Repeat("c", 32)
	if err := store.SaveResource(ctxA, model.Resource{ID: resourceID, Name: "课程.pdf", OriginalName: "课程.pdf", Kind: "pdf", MIME: "application/pdf", SHA256: strings.Repeat("0", 64), StorageKey: resourceID, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	page := 4
	noteID := strings.Repeat("d", 32)
	note := model.ResourceNote{ID: noteID, ResourceID: resourceID, PageNumber: &page, Excerpt: "独特摘录词", Content: "私人正文词", Source: "来源说明词", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := store.CreateResourceNote(ctxB, note); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other user create: %v", err)
	}
	if err := store.CreateResourceNote(ctxA, note); err != nil {
		t.Fatal(err)
	}
	if items, err := store.ListResourceNotes(ctxB, resourceID); err != nil || len(items) != 0 {
		t.Fatalf("other user list: %+v, %v", items, err)
	}
	if items, err := store.ListResourceNotes(ctxA, resourceID); err != nil || len(items) != 1 || items[0].PageNumber == nil || *items[0].PageNumber != page {
		t.Fatalf("owner list: %+v, %v", items, err)
	}
	for _, keyword := range []string{"独特摘录词", "私人正文词", "来源说明词"} {
		if files, err := store.SearchFiles(ctxA, model.SearchFilter{Query: keyword}, 21, 0); err != nil || len(files) != 1 || files[0].ID != resourceID {
			t.Fatalf("owner search %s: %+v, %v", keyword, files, err)
		}
		if files, err := store.SearchFiles(ctxB, model.SearchFilter{Query: keyword}, 21, 0); err != nil || len(files) != 0 {
			t.Fatalf("other user search %s: %+v, %v", keyword, files, err)
		}
	}
	note.Content = "修改后的正文"
	note.PageNumber = nil
	if err := store.UpdateResourceNote(ctxB, note); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other user update: %v", err)
	}
	if err := store.UpdateResourceNote(ctxA, note); err != nil {
		t.Fatal(err)
	}
	if items, err := store.ListResourceNotes(ctxA, resourceID); err != nil || len(items) != 1 || items[0].Content != note.Content || items[0].PageNumber != nil {
		t.Fatalf("owner updated list: %+v, %v", items, err)
	}
	if err := store.DeleteResourceNote(ctxB, resourceID, noteID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other user delete: %v", err)
	}
	// 一份文件已有 100 条时必须拒绝新建，防止无界增长拖慢详情和关键词检索。
	for index := 1; index < 100; index++ {
		row := resourceNoteRow{ID: fmt.Sprintf("%032x", index), UserID: ownerA, ResourceID: resourceID, Content: "test", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
		if err := transaction.Table("resource_notes").Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	note.ID = strings.Repeat("e", 32)
	if err := store.CreateResourceNote(ctxA, note); !errors.Is(err, ErrResourceNoteLimit) {
		t.Fatalf("101st note: %v", err)
	}
	if err := store.DeleteResourceNote(ctxA, resourceID, noteID); err != nil {
		t.Fatalf("owner delete: %v", err)
	}
	if err := store.CreateResourceNote(ctxA, note); err != nil {
		t.Fatalf("create after delete: %v", err)
	}
	if _, err := store.DeleteResource(ctxA, resourceID); err != nil {
		t.Fatal(err)
	}
	var remaining int64
	if err := transaction.Table("resource_notes").Where("resource_id = ?", resourceID).Count(&remaining).Error; err != nil || remaining != 0 {
		t.Fatalf("single delete left private notes: %d, %v", remaining, err)
	}
	if items, err := store.ListResourceNotes(ctxA, resourceID); err != nil || len(items) != 0 {
		t.Fatalf("deleted file notes visible: %+v, %v", items, err)
	}
	if files, err := store.SearchFiles(ctxA, model.SearchFilter{Query: note.Excerpt}, 21, 0); err != nil || len(files) != 0 {
		t.Fatalf("deleted file searchable: %+v, %v", files, err)
	}
	if err := store.UpdateResourceNote(ctxA, note); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted file note update: %v", err)
	}
	// 批量删除同样必须在事务内清掉笔记，而不是只隐藏所属文件。
	batchResourceID := strings.Repeat("f", 32)
	if err := store.SaveResource(ctxA, model.Resource{ID: batchResourceID, Name: "batch.pdf", OriginalName: "batch.pdf", Kind: "pdf", MIME: "application/pdf", SHA256: strings.Repeat("0", 64), StorageKey: batchResourceID, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	batchNote := model.ResourceNote{ID: strings.Repeat("9", 32), ResourceID: batchResourceID, Content: "批量删除敏感笔记", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := store.CreateResourceNote(ctxA, batchNote); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BatchDeleteEntries(ctxA, []model.InboxSelection{{Source: "file", ID: batchResourceID}}); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Table("resource_notes").Where("resource_id = ?", batchResourceID).Count(&remaining).Error; err != nil || remaining != 0 {
		t.Fatalf("batch delete left private notes: %d, %v", remaining, err)
	}
}

// TestResourceNotesConcurrentLimitMySQL 在真实连接并发下确认文件行锁只允许最后一个容量名额成功。
func TestResourceNotesConcurrentLimitMySQL(t *testing.T) {
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
	store := NewMySQL(database)
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	owner, err := newTagID()
	if err != nil {
		t.Fatal(err)
	}
	resourceID, err := newTagID()
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Table("users").Create(map[string]any{"id": owner, "username": owner, "created_at": time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = database.Exec("DELETE FROM resources WHERE id = ?", resourceID).Error
		_ = database.Exec("DELETE FROM users WHERE id = ?", owner).Error
	})
	ctx := WithUserID(context.Background(), owner)
	if err := store.SaveResource(ctx, model.Resource{ID: resourceID, Name: "limit.txt", OriginalName: "limit.txt", Kind: "text", MIME: "text/plain", SHA256: strings.Repeat("0", 64), StorageKey: resourceID, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 99; index++ {
		row := resourceNoteRow{ID: fmt.Sprintf("%032x", index+1), UserID: owner, ResourceID: resourceID, Content: "seed", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
		if err := database.Table("resource_notes").Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	identifiers := make([]string, 16)
	for index := range identifiers {
		identifiers[index], err = newTagID()
		if err != nil {
			t.Fatal(err)
		}
	}
	results := make(chan error, len(identifiers))
	var workers sync.WaitGroup
	for _, id := range identifiers {
		workers.Add(1)
		go func(noteID string) {
			defer workers.Done()
			now := time.Now().UTC()
			results <- store.CreateResourceNote(ctx, model.ResourceNote{ID: noteID, ResourceID: resourceID, Content: "parallel", CreatedAt: now, UpdatedAt: now})
		}(id)
	}
	workers.Wait()
	close(results)
	succeeded, limited := 0, 0
	for result := range results {
		switch {
		case result == nil:
			succeeded++
		case errors.Is(result, ErrResourceNoteLimit):
			limited++
		default:
			t.Fatalf("unexpected concurrent failure: %v", result)
		}
	}
	var count int64
	if err := database.Table("resource_notes").Where("resource_id = ?", resourceID).Count(&count).Error; err != nil || succeeded != 1 || limited != 15 || count != 100 {
		t.Fatalf("concurrent capacity: succeeded=%d limited=%d rows=%d err=%v", succeeded, limited, count, err)
	}
}
