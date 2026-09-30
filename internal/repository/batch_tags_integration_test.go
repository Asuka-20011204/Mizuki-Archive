package repository

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"mizuki-archive/internal/model"
)

// TestBatchTagsMySQLAtomicity 使用隔离数据库验证跨来源标签幂等、越权、软删除及上限回滚。
func TestBatchTagsMySQLAtomicity(t *testing.T) {
	database, _ := openProcessingIsolationDatabase(t)
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
	card := model.ExternalResource{ID: cardID, Title: "fixture", Location: "local folder", ResourceType: "note", Status: "pending", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := store.CreateExternalResource(ctxA, card); err != nil {
		t.Fatal(err)
	}
	file := model.InboxSelection{Source: "file", ID: fileID}
	external := model.InboxSelection{Source: "external", ID: cardID}
	valid := []model.InboxSelection{file, external}
	if _, err := store.BatchUpdateTags(ctxA, append(valid, model.InboxSelection{Source: "file", ID: foreignID}), "学习", "add"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user batch: %v", err)
	}
	if _, err := store.BatchUpdateTags(ctxA, valid, "学习", "add"); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if changed, err := store.BatchUpdateTags(ctxA, valid, "学习", "add"); err != nil || len(changed) != 0 {
		t.Fatalf("repeated add: %+v %v", changed, err)
	}
	loadedFile, err := store.GetResource(ctxA, fileID)
	if err != nil || !slices.Contains(loadedFile.Tags, "学习") {
		t.Fatalf("file tags: %+v %v", loadedFile.Tags, err)
	}
	loadedCard, err := store.GetExternalResource(ctxA, cardID)
	if err != nil || !slices.Contains(loadedCard.Tags, "学习") {
		t.Fatalf("card tags: %+v %v", loadedCard.Tags, err)
	}
	if changed, err := store.BatchUpdateTags(ctxA, valid, "学习", "remove"); err != nil || len(changed) != 2 {
		t.Fatalf("remove: %+v %v", changed, err)
	}
	if changed, err := store.BatchUpdateTags(ctxA, valid, "学习", "remove"); err != nil || len(changed) != 0 {
		t.Fatalf("repeated remove: %+v %v", changed, err)
	}
	if _, err := store.DeleteResource(ctxA, deletedID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BatchUpdateTags(ctxA, []model.InboxSelection{external, {Source: "file", ID: deletedID}}, "测试", "add"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted entry: %v", err)
	}
	loadedCard, err = store.GetExternalResource(ctxA, cardID)
	if err != nil || slices.Contains(loadedCard.Tags, "测试") {
		t.Fatalf("deleted rollback: %+v %v", loadedCard.Tags, err)
	}
	tags := make([]string, 10)
	for index := range tags {
		tags[index] = fmt.Sprintf("tag-%d", index)
	}
	if _, err := store.ReplaceResourceTags(ctxA, fileID, tags); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BatchUpdateTags(ctxA, valid, "越限", "add"); !errors.Is(err, ErrBatchTagLimit) {
		t.Fatalf("limit: %v", err)
	}
	loadedCard, err = store.GetExternalResource(ctxA, cardID)
	if err != nil || slices.Contains(loadedCard.Tags, "越限") {
		t.Fatalf("limit rollback: %+v %v", loadedCard.Tags, err)
	}
	loadedCard.Tags = tags
	loadedCard.Note = "card limit fixture"
	loadedCard.UpdatedAt = time.Now().UTC()
	if _, err := store.UpdateExternalResource(ctxA, loadedCard); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BatchUpdateTags(ctxA, []model.InboxSelection{{Source: "file", ID: foreignID}}, "越限", "add"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign file: %v", err)
	}
	if _, err := store.BatchUpdateTags(ctxA, []model.InboxSelection{external}, "越限", "add"); !errors.Is(err, ErrBatchTagLimit) {
		t.Fatalf("card limit: %v", err)
	}
	if _, err := store.BatchUpdateTags(ctxA, []model.InboxSelection{file}, tags[0], "remove"); err != nil {
		t.Fatal(err)
	}
	// 文件先添加成功而卡片达到上限，整个事务必须回滚先前的文件写入。
	if _, err := store.BatchUpdateTags(ctxA, valid, "回滚测试", "add"); !errors.Is(err, ErrBatchTagLimit) {
		t.Fatalf("card limit after file write: %v", err)
	}
	loadedFile, err = store.GetResource(ctxA, fileID)
	if err != nil || slices.Contains(loadedFile.Tags, "回滚测试") {
		t.Fatalf("file write survived rollback: %+v %v", loadedFile.Tags, err)
	}
}
