package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"mizuki-archive/internal/model"
)

// TestInboxBatchMySQLAtomicity 使用隔离 MySQL 验证跨两表事务、越权整批回滚和显式状态撤销。
func TestInboxBatchMySQLAtomicity(t *testing.T) {
	database, _ := openProcessingIsolationDatabase(t)
	transaction := database.Begin()
	if transaction.Error != nil {
		t.Fatal(transaction.Error)
	}
	t.Cleanup(func() { _ = transaction.Rollback().Error })
	store := NewMySQL(transaction)
	ownerA, ownerB := strings.Repeat("1", 32), strings.Repeat("2", 32)
	for _, owner := range []string{ownerA, ownerB} {
		if err := transaction.Table("users").Create(map[string]any{"id": owner, "username": owner, "created_at": time.Now().UTC()}).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctxA, ctxB := WithUserID(context.Background(), ownerA), WithUserID(context.Background(), ownerB)
	fileID, foreignID, cardID := strings.Repeat("3", 32), strings.Repeat("4", 32), strings.Repeat("5", 32)
	for _, entry := range []struct {
		ctx context.Context
		id  string
	}{{ctxA, fileID}, {ctxB, foreignID}} {
		file := model.Resource{ID: entry.id, Name: "test.txt", OriginalName: "test.txt", Kind: "text", MIME: "text/plain", StorageKey: entry.id, SHA256: strings.Repeat("a", 64), CreatedAt: time.Now().UTC()}
		if err := store.SaveResource(entry.ctx, file); err != nil {
			t.Fatal(err)
		}
	}
	card := model.ExternalResource{ID: cardID, Title: "outside", Location: "local folder", ResourceType: "note", Status: "pending", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := store.CreateExternalResource(ctxA, card); err != nil {
		t.Fatal(err)
	}
	valid := []model.InboxSelection{{Source: "file", ID: fileID}, {Source: "external", ID: cardID}}
	if err := store.BatchSetOrganizationStatus(ctxA, append(valid, model.InboxSelection{Source: "file", ID: foreignID}), "organized"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("mixed-owner batch: %v", err)
	}
	file, err := store.GetResource(ctxA, fileID)
	if err != nil || file.OrganizationStatus != "pending" {
		t.Fatalf("partial file update: %v %#v", err, file)
	}
	cardAfter, err := store.GetExternalResource(ctxA, cardID)
	if err != nil || cardAfter.OrganizationStatus != "pending" {
		t.Fatalf("partial card update: %v %#v", err, cardAfter)
	}
	if err := store.BatchSetOrganizationStatus(ctxA, valid, "organized"); err != nil {
		t.Fatal(err)
	}
	if err := store.BatchSetOrganizationStatus(ctxA, valid, "pending"); err != nil {
		t.Fatalf("undo batch: %v", err)
	}
	file, err = store.GetResource(ctxA, fileID)
	if err != nil || file.OrganizationStatus != "pending" {
		t.Fatalf("file after undo: %v %#v", err, file)
	}
	cardAfter, err = store.GetExternalResource(ctxA, cardID)
	if err != nil || cardAfter.OrganizationStatus != "pending" || cardAfter.Status != "pending" {
		t.Fatalf("card after undo: %v %#v", err, cardAfter)
	}
}
