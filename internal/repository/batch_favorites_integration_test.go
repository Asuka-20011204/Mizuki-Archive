package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"mizuki-archive/internal/model"
)

// TestBatchFavoritesMySQLAtomicity 在隔离库验证 015 可重试迁移、混合归属和收藏幂等。
func TestBatchFavoritesMySQLAtomicity(t *testing.T) {
	database, root := openProcessingIsolationDatabase(t)
	if err := root.Migrate(context.Background()); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	if err := database.Exec("DELETE FROM schema_migrations WHERE version = ?", 15).Error; err != nil {
		t.Fatal(err)
	}
	if err := root.Migrate(context.Background()); err != nil {
		t.Fatalf("interrupted migration: %v", err)
	}
	transaction := database.Begin()
	if transaction.Error != nil {
		t.Fatal(transaction.Error)
	}
	t.Cleanup(func() { _ = transaction.Rollback().Error })
	store := NewMySQL(transaction)
	ownerA, ownerB := mustProcessingIsolationID(t), mustProcessingIsolationID(t)
	for _, owner := range []string{ownerA, ownerB} {
		if err := transaction.Table("users").Create(map[string]any{"id": owner, "username": owner, "created_at": time.Now().UTC()}).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctxA, ctxB := WithUserID(context.Background(), ownerA), WithUserID(context.Background(), ownerB)
	fileID, foreignID, cardID, deletedID := mustProcessingIsolationID(t), mustProcessingIsolationID(t), mustProcessingIsolationID(t), mustProcessingIsolationID(t)
	for _, entry := range []struct {
		ctx context.Context
		id  string
	}{{ctxA, fileID}, {ctxB, foreignID}, {ctxA, deletedID}} {
		file := model.Resource{ID: entry.id, Name: "test.txt", OriginalName: "test.txt", Kind: "text", MIME: "text/plain", StorageKey: entry.id, SHA256: strings.Repeat("a", 64), CreatedAt: time.Now().UTC()}
		if err := store.SaveResource(entry.ctx, file); err != nil {
			t.Fatal(err)
		}
	}
	card := model.ExternalResource{ID: cardID, Title: "outside", Location: "local folder", ResourceType: "note", Status: "pending", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := store.CreateExternalResource(ctxA, card); err != nil {
		t.Fatal(err)
	}
	file := model.InboxSelection{Source: "file", ID: fileID}
	external := model.InboxSelection{Source: "external", ID: cardID}
	valid := []model.InboxSelection{file, external}
	if _, err := store.BatchUpdateFavorites(ctxA, append(valid, model.InboxSelection{Source: "file", ID: foreignID}), true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("mixed-owner batch: %v", err)
	}
	if actual, err := store.GetResource(ctxA, fileID); err != nil || actual.Favorite {
		t.Fatalf("partial file write: %+v %v", actual, err)
	}
	if actual, err := store.GetExternalResource(ctxA, cardID); err != nil || actual.Favorite {
		t.Fatalf("partial card write: %+v %v", actual, err)
	}
	if changed, err := store.BatchUpdateFavorites(ctxA, valid, true); err != nil || len(changed) != 2 {
		t.Fatalf("favorite: %+v %v", changed, err)
	}
	if changed, err := store.BatchUpdateFavorites(ctxA, valid, true); err != nil || len(changed) != 0 {
		t.Fatalf("repeat favorite: %+v %v", changed, err)
	}
	loaded, err := store.GetExternalResource(ctxA, cardID)
	if err != nil || !loaded.Favorite {
		t.Fatalf("favorite card read: %+v %v", loaded, err)
	}
	loaded.Note = "edited without favorite field"
	loaded.UpdatedAt = time.Now().UTC()
	if updated, err := store.UpdateExternalResource(ctxA, loaded); err != nil || !updated.Favorite {
		t.Fatalf("edit preserved favorite: %+v %v", updated, err)
	}
	if changed, err := store.BatchUpdateFavorites(ctxA, valid, false); err != nil || len(changed) != 2 {
		t.Fatalf("unfavorite: %+v %v", changed, err)
	}
	if changed, err := store.BatchUpdateFavorites(ctxA, valid, false); err != nil || len(changed) != 0 {
		t.Fatalf("repeat unfavorite: %+v %v", changed, err)
	}
	if _, err := store.DeleteResource(ctxA, deletedID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BatchUpdateFavorites(ctxA, []model.InboxSelection{external, {Source: "file", ID: deletedID}}, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted file batch: %v", err)
	}
	if actual, err := store.GetExternalResource(ctxA, cardID); err != nil || actual.Favorite || actual.Status != "pending" {
		t.Fatalf("deleted rollback/status: %+v %v", actual, err)
	}
}
