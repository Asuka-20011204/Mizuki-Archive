package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"mizuki-archive/internal/model"
)

// TestTopicsMySQLIsolation 在 mizuki_test_ 隔离库验证迁移、归属、顺序、删除清理和容量限制。
func TestTopicsMySQLIsolation(t *testing.T) {
	database, root := openProcessingIsolationDatabase(t)
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
	ctxA, ctxB := WithUserID(context.Background(), ownerA), WithUserID(context.Background(), ownerB)
	cover, text := strings.Repeat("c", 32), strings.Repeat("d", 32)
	for _, file := range []struct {
		owner          context.Context
		id, name, kind string
	}{
		{ctxA, cover, "封面", "image"}, {ctxA, text, "笔记", "text"}, {ctxB, strings.Repeat("e", 32), "他人的图", "image"},
	} {
		if err := store.SaveResource(file.owner, model.Resource{ID: file.id, Name: file.name, OriginalName: file.name, Kind: file.kind, MIME: "image/png", SHA256: strings.Repeat("0", 64), StorageKey: file.id, CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	cardID := strings.Repeat("f", 32)
	if err := store.CreateExternalResource(ctxA, model.ExternalResource{ID: cardID, Title: "站外卡片", Location: "private://secret", ResourceType: "link", Status: "pending", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	empty, err := store.ListTopics(ctxA)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty list: %+v %v", empty, err)
	}
	if empty == nil {
		t.Fatal("empty list must serialize as []")
	}
	input := model.TopicInput{Title: "我的专题", Intro: "介绍", CoverFileID: &cover, Sections: []model.TopicSection{{Title: "空分区"}, {Title: "资料", Items: []model.TopicItem{{Source: "external", ID: cardID}, {Source: "file", ID: text}}}}}
	for _, id := range []string{text, strings.Repeat("e", 32)} {
		bad := input
		bad.CoverFileID = &id
		if _, err := store.SaveTopic(ctxA, strings.Repeat("1", 32), bad, false); !errors.Is(err, ErrNotFound) {
			t.Fatalf("invalid cover %s: %v", id, err)
		}
	}
	foreign := input
	foreign.Sections = []model.TopicSection{{Title: "foreign", Items: []model.TopicItem{{Source: "file", ID: strings.Repeat("e", 32)}}}}
	if _, err := store.SaveTopic(ctxA, strings.Repeat("2", 32), foreign, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign item: %v", err)
	}
	id := strings.Repeat("3", 32)
	created, err := store.SaveTopic(ctxA, id, input, false)
	if err != nil || len(created.Sections) != 2 || len(created.Sections[0].Items) != 0 || created.Sections[1].Items[0].Name != "站外卡片" || created.Sections[1].Items[1].Name != "笔记" {
		t.Fatalf("ordered detail: %+v %v", created, err)
	}
	if strings.Contains(fmt.Sprintf("%+v", created), "private://secret") {
		t.Fatal("detail leaked location")
	}
	for _, action := range []func() error{
		func() error { _, err := store.GetTopic(ctxB, id); return err },
		func() error { _, err := store.SaveTopic(ctxB, id, input, true); return err },
		func() error { return store.DeleteTopic(ctxB, id) },
	} {
		if err := action(); !errors.Is(err, ErrNotFound) {
			t.Fatalf("foreign topic: %v", err)
		}
	}
	list, err := store.ListTopics(ctxA)
	if err != nil || len(list) != 1 || list[0].SectionCount != 2 || list[0].ItemCount != 2 {
		t.Fatalf("list counts: %+v %v", list, err)
	}
	if err := store.DeleteExternalResource(ctxA, cardID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeleteResource(ctxA, cover); err != nil {
		t.Fatal(err)
	}
	updated, err := store.GetTopic(ctxA, id)
	if err != nil || updated.CoverFileID != nil || updated.ItemCount != 1 || updated.Sections[1].Items[0].Name != "笔记" {
		t.Fatalf("deleted references: %+v %v", updated, err)
	}
	if _, err := store.BatchDeleteEntries(ctxA, []model.InboxSelection{{Source: "file", ID: text}}); err != nil {
		t.Fatal(err)
	}
	list, err = store.ListTopics(ctxA)
	if err != nil || len(list) != 1 || list[0].ItemCount != 0 || list[0].SectionCount != 2 {
		t.Fatalf("batch cleanup: %+v %v", list, err)
	}
	for index := 0; index < 29; index++ {
		id := fmt.Sprintf("%032x", index+16)
		if _, err := store.SaveTopic(ctxA, id, model.TopicInput{Title: "空"}, false); err != nil {
			t.Fatalf("create up to limit: %v", err)
		}
	}
	if _, err := store.SaveTopic(ctxA, strings.Repeat("9", 32), model.TopicInput{Title: "超额"}, false); !errors.Is(err, ErrTopicLimit) {
		t.Fatalf("limit: %v", err)
	}
	if err := store.DeleteTopic(ctxA, id); err != nil {
		t.Fatal(err)
	}
}
