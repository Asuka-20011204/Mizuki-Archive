package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"mizuki-archive/internal/model"
)

// TestArchiveMySQLIsolation 使用隔离 MySQL 验证迁移重试、归档可找回、跨账号和软删除整批拒绝。
func TestArchiveMySQLIsolation(t *testing.T) {
	database, root := openProcessingIsolationDatabase(t)
	if err := root.Migrate(context.Background()); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	if err := database.Exec("DELETE FROM schema_migrations WHERE version IN ?", []int{16, 17}).Error; err != nil {
		t.Fatal(err)
	}
	if err := root.Migrate(context.Background()); err != nil {
		t.Fatalf("resume interrupted migration: %v", err)
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
		resource := model.Resource{ID: entry.id, Name: "fixture.txt", OriginalName: "fixture.txt", Kind: "text", MIME: "text/plain", StorageKey: entry.id, SHA256: strings.Repeat("a", 64), CreatedAt: time.Now().UTC()}
		if err := store.SaveResource(entry.ctx, resource); err != nil {
			t.Fatal(err)
		}
	}
	card := model.ExternalResource{ID: cardID, Title: "fixture card", Location: "local folder", ResourceType: "note", Status: "available", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := store.CreateExternalResource(ctxA, card); err != nil {
		t.Fatal(err)
	}
	file := model.InboxSelection{Source: "file", ID: fileID}
	if _, err := store.ReplaceResourceTags(ctxA, fileID, []string{"archive-only-tag"}); err != nil {
		t.Fatal(err)
	}
	external := model.InboxSelection{Source: "external", ID: cardID}
	valid := []model.InboxSelection{file, external}
	if _, err := store.BatchSetArchived(ctxA, append(valid, model.InboxSelection{Source: "file", ID: foreignID}), true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user batch: %v", err)
	}
	if found, err := store.GetResource(ctxA, fileID); err != nil || found.Archived {
		t.Fatalf("partial file update: %+v %v", found, err)
	}
	if changed, err := store.BatchSetArchived(ctxA, valid, true); err != nil || len(changed) != 2 {
		t.Fatalf("archive: %+v %v", changed, err)
	}
	if changed, err := store.BatchSetArchived(ctxA, valid, true); err != nil || len(changed) != 0 {
		t.Fatalf("repeat archive: %+v %v", changed, err)
	}
	if found, err := store.GetResource(ctxA, fileID); err != nil || !found.Archived {
		t.Fatalf("file still readable by owner: %+v %v", found, err)
	}
	if found, err := store.GetExternalResource(ctxA, cardID); err != nil || !found.Archived || found.Status != "available" {
		t.Fatalf("card state preserved: %+v %v", found, err)
	}
	if found, err := store.ListArchivedResources(ctxA, 10, 0); err != nil || len(found) != 1 || found[0].ID != fileID {
		t.Fatalf("archived file list: %+v %v", found, err)
	}
	if tags, err := store.ListTags(ctxA, "archive-only-tag"); err != nil || len(tags) != 0 {
		t.Fatalf("archived file tag should not be offered as active filter: %+v %v", tags, err)
	}
	if found, err := store.ListArchivedExternalResources(ctxA, 10, 0); err != nil || len(found) != 1 || found[0].ID != cardID {
		t.Fatalf("archived card list: %+v %v", found, err)
	}
	if found, err := store.ListResources(ctxA, model.ListQuery{Limit: 10}); err != nil || len(found) != 1 || found[0].ID != deletedID {
		t.Fatalf("active files: %+v %v", found, err)
	}
	if found, err := store.ListExternalResources(ctxA, ""); err != nil || len(found) != 0 {
		t.Fatalf("active cards: %+v %v", found, err)
	}
	if found, err := store.ListPendingResources(ctxA, 10, 0); err != nil || len(found) != 1 {
		t.Fatalf("inbox files: %+v %v", found, err)
	}
	if found, err := store.ListPendingExternalResources(ctxA, 10, 0); err != nil || len(found) != 0 {
		t.Fatalf("inbox cards: %+v %v", found, err)
	}
	if found, err := store.SearchExternal(ctxA, model.SearchFilter{Query: "fixture"}, 10, 0); err != nil || len(found) != 0 {
		t.Fatalf("active search: %+v %v", found, err)
	}
	if _, err := store.GetResource(ctxB, fileID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user read: %v", err)
	}
	if changed, err := store.BatchSetArchived(ctxA, valid, false); err != nil || len(changed) != 2 {
		t.Fatalf("restore: %+v %v", changed, err)
	}
	if tags, err := store.ListTags(ctxA, "archive-only-tag"); err != nil || len(tags) != 1 {
		t.Fatalf("restored file tag should return to active filter: %+v %v", tags, err)
	}
	if changed, err := store.BatchSetArchived(ctxA, valid, false); err != nil || len(changed) != 0 {
		t.Fatalf("repeat restore: %+v %v", changed, err)
	}
	if found, err := store.GetExternalResource(ctxA, cardID); err != nil || found.Archived || found.Status != "available" {
		t.Fatalf("restored card: %+v %v", found, err)
	}
	if _, err := store.DeleteResource(ctxA, deletedID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BatchSetArchived(ctxA, []model.InboxSelection{file, {Source: "file", ID: deletedID}}, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("soft-deleted batch: %v", err)
	}
	if found, err := store.GetResource(ctxA, fileID); err != nil || found.Archived {
		t.Fatalf("deleted batch partial write: %+v %v", found, err)
	}
	if _, err := store.BatchDeleteEntries(ctxA, []model.InboxSelection{file, {Source: "file", ID: foreignID}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user delete must reject entire batch: %v", err)
	}
	if _, err := store.BatchDeleteEntries(ctxA, []model.InboxSelection{file, {Source: "file", ID: deletedID}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("soft-deleted file must reject entire batch: %v", err)
	}
	if found, err := store.GetResource(ctxA, fileID); err != nil || found.ID != fileID {
		t.Fatalf("failed batch changed own file: %+v %v", found, err)
	}
	jobID, assetID, eventID := mustProcessingIsolationID(t), mustProcessingIsolationID(t), mustProcessingIsolationID(t)
	now := time.Now().UTC()
	if err := transaction.Table("processing_jobs").Create(map[string]any{"id": jobID, "resource_id": fileID, "type": model.ProcessingTypeExtractText, "source_sha256": strings.Repeat("a", 64), "status": model.ProcessingStatusSucceeded, "max_attempts": 3, "available_at": now, "created_at": now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := transaction.Table("derived_assets").Create(map[string]any{"id": assetID, "job_id": jobID, "resource_id": fileID, "kind": model.DerivedAssetText, "name": "derived.txt", "storage_key": assetID, "mime": "text/plain", "size_bytes": 6, "sha256": strings.Repeat("b", 64), "content_text": "private text", "created_at": now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := transaction.Table("processing_outbox").Create(map[string]any{"id": eventID, "job_id": jobID, "status": "published", "available_at": now, "created_at": now}).Error; err != nil {
		t.Fatal(err)
	}
	if files, err := store.BatchDeleteEntries(ctxA, valid); err != nil || len(files.OriginalIDs) != 1 || files.OriginalIDs[0] != fileID || len(files.DerivedIDs) != 1 || files.DerivedIDs[0] != assetID {
		t.Fatalf("delete mixed batch: %+v %v", files, err)
	}
	pendingCleanup, err := store.ListPendingFileCleanup(ctxA, 10)
	if err != nil || len(pendingCleanup) != 2 {
		t.Fatalf("cleanup record must survive database commit: %+v %v", pendingCleanup, err)
	}
	for _, entry := range pendingCleanup {
		if err := store.CompleteFileCleanup(ctxA, entry); err != nil {
			t.Fatalf("complete cleanup: %v", err)
		}
	}
	if left, err := store.ListPendingFileCleanup(ctxA, 10); err != nil || len(left) != 0 {
		t.Fatalf("cleanup record not removed: %+v %v", left, err)
	}
	for _, table := range []string{"processing_jobs", "derived_assets", "processing_outbox"} {
		var remaining int64
		if err := transaction.Table(table).Where("id IN ?", []string{jobID, assetID, eventID}).Count(&remaining).Error; err != nil || remaining != 0 {
			t.Fatalf("deleted batch left %s: %d %v", table, remaining, err)
		}
	}
	if _, err := store.GetResource(ctxA, fileID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted file still readable: %v", err)
	}
	if _, err := store.GetExternalResource(ctxA, cardID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted card still readable: %v", err)
	}
	if _, err := store.BatchDeleteEntries(ctxA, valid); !errors.Is(err, ErrNotFound) {
		t.Fatalf("repeated destructive batch: %v", err)
	}
}
