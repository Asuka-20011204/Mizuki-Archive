package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"mizuki-archive/internal/model"
)

// TestRelationsMySQLIsolation 在隔离数据库验证双向查询、跨用户拒绝、去重及删除级联。
func TestRelationsMySQLIsolation(t *testing.T) {
	database, root := openProcessingIsolationDatabase(t)
	if err := root.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := root.Migrate(context.Background()); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	tx := database.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	store := NewMySQL(tx)
	ownerA, ownerB := strings.Repeat("a", 32), strings.Repeat("b", 32)
	for _, owner := range []string{ownerA, ownerB} {
		if err := tx.Table("users").Create(map[string]any{"id": owner, "username": owner, "created_at": time.Now().UTC()}).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctxA := WithUserID(context.Background(), ownerA)
	ctxB := WithUserID(context.Background(), ownerB)
	file := model.InboxSelection{Source: "file", ID: strings.Repeat("c", 32)}
	card := model.InboxSelection{Source: "external", ID: strings.Repeat("d", 32)}
	foreign := model.InboxSelection{Source: "external", ID: strings.Repeat("e", 32)}
	if err := store.SaveResource(ctxA, model.Resource{ID: file.ID, Name: "游戏", OriginalName: "game.txt", Kind: "text", MIME: "text/plain", SHA256: strings.Repeat("0", 64), StorageKey: file.ID, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		owner context.Context
		id    string
		name  string
	}{{ctxA, card.ID, "攻略"}, {ctxB, foreign.ID, "他人的攻略"}} {
		if err := store.CreateExternalResource(entry.owner, model.ExternalResource{ID: entry.id, Title: entry.name, Location: "local", ResourceType: "guide", Status: "pending", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.CreateRelation(ctxA, file, foreign, strings.Repeat("1", 32)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign endpoint: %v", err)
	}
	linked, err := store.CreateRelation(ctxA, file, card, strings.Repeat("2", 32))
	if err != nil || linked.Name != "攻略" {
		t.Fatalf("create relation: %+v %v", linked, err)
	}
	if _, err := store.CreateRelation(ctxA, card, file, strings.Repeat("3", 32)); !errors.Is(err, ErrRelationExists) {
		t.Fatalf("reverse duplicate: %v", err)
	}
	for _, item := range []struct {
		ctx    context.Context
		source model.InboxSelection
		name   string
		count  int
	}{{ctxA, file, "攻略", 1}, {ctxA, card, "游戏", 1}, {ctxB, file, "", 0}} {
		list, err := store.ListRelations(item.ctx, item.source)
		if item.count == 0 {
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("foreign list: %v", err)
			}
			continue
		}
		if err != nil || len(list) != item.count || list[0].Name != item.name {
			t.Fatalf("list: %+v %v", list, err)
		}
	}
	if _, err := store.UpdateResourceName(ctxA, file.ID, "游戏新版"); err != nil {
		t.Fatal(err)
	}
	if list, err := store.ListRelations(ctxA, card); err != nil || len(list) != 1 || list[0].Name != "游戏新版" {
		t.Fatalf("renamed endpoint: %+v %v", list, err)
	}
	if err := store.DeleteRelation(ctxB, linked.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign delete: %v", err)
	}
	if err := store.DeleteExternalResource(ctxA, card.ID); err != nil {
		t.Fatal(err)
	}
	if list, err := store.ListRelations(ctxA, file); err != nil || len(list) != 0 {
		t.Fatalf("card deletion left relation: %+v %v", list, err)
	}
	second := model.InboxSelection{Source: "external", ID: strings.Repeat("f", 32)}
	if err := store.CreateExternalResource(ctxA, model.ExternalResource{ID: second.ID, Title: "截图", Location: "local", ResourceType: "image", Status: "pending", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRelation(ctxA, file, second, strings.Repeat("4", 32)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeleteResource(ctxA, file.ID); err != nil {
		t.Fatal(err)
	}
	if list, err := store.ListRelations(ctxA, second); err != nil || len(list) != 0 {
		t.Fatalf("file deletion left relation: %+v %v", list, err)
	}
	batchFile := model.InboxSelection{Source: "file", ID: strings.Repeat("6", 32)}
	if err := store.SaveResource(ctxA, model.Resource{ID: batchFile.ID, Name: "安装说明", OriginalName: "install.txt", Kind: "text", MIME: "text/plain", SHA256: strings.Repeat("0", 64), StorageKey: batchFile.ID, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRelation(ctxA, batchFile, second, strings.Repeat("7", 32)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BatchDeleteEntries(ctxA, []model.InboxSelection{second}); err != nil {
		t.Fatal(err)
	}
	if list, err := store.ListRelations(ctxA, batchFile); err != nil || len(list) != 0 {
		t.Fatalf("batch deletion left relation: %+v %v", list, err)
	}
}
