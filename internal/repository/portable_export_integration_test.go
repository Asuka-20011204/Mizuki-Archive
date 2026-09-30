package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"mizuki-archive/internal/model"
)

// TestPortableExportMySQLIsolation 在独立数据库验证快照内容、账号隔离和删除后的清理结果。
func TestPortableExportMySQLIsolation(t *testing.T) {
	database, root := openProcessingIsolationDatabase(t)
	if err := root.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := root.ExportSnapshot(WithUserID(context.Background(), "empty-owner")); err != nil {
		t.Fatalf("read-only export transaction: %v", err)
	}
	transaction := database.Begin()
	if transaction.Error != nil {
		t.Fatal(transaction.Error)
	}
	t.Cleanup(func() { _ = transaction.Rollback().Error })
	store := NewMySQL(transaction)
	firstOwner, otherOwner := strings.Repeat("a", 32), strings.Repeat("b", 32)
	for _, owner := range []string{firstOwner, otherOwner} {
		if err := transaction.Table("users").Create(map[string]any{"id": owner, "username": owner, "created_at": time.Now().UTC()}).Error; err != nil {
			t.Fatal(err)
		}
	}
	firstContext := WithUserID(context.Background(), firstOwner)
	otherContext := WithUserID(context.Background(), otherOwner)
	fileID := strings.Repeat("c", 32)
	otherFileID := strings.Repeat("d", 32)
	for _, entry := range []struct {
		ctx  context.Context
		id   string
		name string
	}{{firstContext, fileID, "用户一的说明"}, {otherContext, otherFileID, "用户二的秘密"}} {
		if err := store.SaveResource(entry.ctx, model.Resource{ID: entry.id, Name: entry.name, OriginalName: "readme.txt", Kind: "text", MIME: "text/plain", SHA256: strings.Repeat("0", 64), StorageKey: entry.id, CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.ReplaceResourceTags(firstContext, fileID, []string{"安装说明"}); err != nil {
		t.Fatal(err)
	}
	cardID := strings.Repeat("e", 32)
	if err := store.CreateExternalResource(firstContext, model.ExternalResource{ID: cardID, Title: "游戏网站", Location: "https://example.com/share", ResourceType: "game", Note: "仅记录地址", Status: "pending", Tags: []string{"线索"}, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateResourceNote(firstContext, model.ResourceNote{ID: strings.Repeat("f", 32), ResourceID: fileID, Content: "私人笔记", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRelation(firstContext, model.InboxSelection{Source: "file", ID: fileID}, model.InboxSelection{Source: "external", ID: cardID}, strings.Repeat("1", 32)); err != nil {
		t.Fatal(err)
	}
	archive, err := store.ExportSnapshot(firstContext)
	if err != nil || len(archive.Files) != 1 || len(archive.ExternalResources) != 1 || len(archive.Notes) != 1 || len(archive.Relations) != 1 {
		t.Fatalf("own snapshot: files=%d cards=%d notes=%d links=%d err=%v", len(archive.Files), len(archive.ExternalResources), len(archive.Notes), len(archive.Relations), err)
	}
	if archive.Files[0].ID != fileID || len(archive.Files[0].Tags) != 1 || archive.Files[0].Tags[0] != "安装说明" || archive.ExternalResources[0].Tags[0] != "线索" || archive.Notes[0].Content != "私人笔记" {
		t.Fatalf("missing portable metadata: %+v", archive)
	}
	if other, err := store.ExportSnapshot(otherContext); err != nil || len(other.Files) != 1 || other.Files[0].ID != otherFileID || len(other.Notes) != 0 || len(other.Relations) != 0 {
		t.Fatalf("cross-account snapshot: %+v %v", other, err)
	}
	if _, err := store.ExportSnapshot(context.Background()); err == nil {
		t.Fatal("missing identity accepted")
	}
	if _, err := store.DeleteResource(firstContext, fileID); err != nil {
		t.Fatal(err)
	}
	archive, err = store.ExportSnapshot(firstContext)
	if err != nil || len(archive.Files) != 0 || len(archive.Notes) != 0 || len(archive.Relations) != 0 || len(archive.ExternalResources) != 1 {
		t.Fatalf("deleted file leaked into snapshot: %+v %v", archive, err)
	}
	if err := store.DeleteExternalResource(firstContext, cardID); err != nil {
		t.Fatal(err)
	}
	archive, err = store.ExportSnapshot(firstContext)
	if err != nil || len(archive.ExternalResources) != 0 {
		t.Fatalf("deleted card leaked into snapshot: %+v %v", archive, err)
	}
}
