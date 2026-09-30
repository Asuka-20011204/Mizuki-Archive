package repository

import (
	"context"
	"testing"
	"time"
)

// TestPendingFileCleanupRetryMySQL 验证失败项延迟重试后不占用下一批清理名额。
func TestPendingFileCleanupRetryMySQL(t *testing.T) {
	database, root := openProcessingIsolationDatabase(t)
	if err := root.Migrate(context.Background()); err != nil {
		t.Fatalf("repeat cleanup migration: %v", err)
	}
	transaction := database.Begin()
	if transaction.Error != nil {
		t.Fatal(transaction.Error)
	}
	t.Cleanup(func() { _ = transaction.Rollback().Error })
	store := NewMySQL(transaction)
	ctx := context.Background()
	failed := PendingFileCleanup{Kind: "original", ID: mustProcessingIsolationID(t)}
	ready := PendingFileCleanup{Kind: "derived", ID: mustProcessingIsolationID(t)}
	for _, entry := range []PendingFileCleanup{failed, ready} {
		if err := transaction.Table("pending_file_cleanup").Create(&pendingFileCleanupRow{
			Kind: entry.Kind, StorageKey: entry.ID, CreatedAt: time.Now().UTC(), RetryAfter: time.Now().UTC().Add(-time.Second),
		}).Error; err != nil {
			t.Fatalf("insert pending cleanup: %v", err)
		}
	}
	if err := store.DeferFileCleanup(ctx, failed); err != nil {
		t.Fatalf("defer failed cleanup: %v", err)
	}
	var deferred pendingFileCleanupRow
	if err := transaction.Table("pending_file_cleanup").Where("kind = ? AND storage_key = ?", failed.Kind, failed.ID).Take(&deferred).Error; err != nil {
		t.Fatalf("read retry deadline: %v", err)
	}
	if deferred.RetryAfter.Before(time.Now().UTC().Add(4 * time.Minute)) {
		t.Fatalf("failed cleanup was not delayed: %v", deferred.RetryAfter)
	}
	entries, err := store.ListPendingFileCleanup(ctx, 1)
	if err != nil || len(entries) != 1 || entries[0] != ready {
		t.Fatalf("ready cleanup blocked by failed entry: %+v %v", entries, err)
	}
	if err := store.CompleteFileCleanup(ctx, ready); err != nil {
		t.Fatalf("complete ready cleanup: %v", err)
	}
	if entries, err := store.ListPendingFileCleanup(ctx, 1); err != nil || len(entries) != 0 {
		t.Fatalf("deferred cleanup returned before deadline: %+v %v", entries, err)
	}
}
