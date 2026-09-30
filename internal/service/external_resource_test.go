package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
)

type externalStoreFake struct {
	items map[string]model.ExternalResource
}

// CreateExternalResource 保存测试卡片，不执行网络调用。
func (store *externalStoreFake) CreateExternalResource(_ context.Context, value model.ExternalResource) error {
	store.items[value.ID] = value
	return nil
}

// GetExternalResource 返回已有测试卡片，不存在时模拟真实仓储的未找到错误。
func (store *externalStoreFake) GetExternalResource(_ context.Context, id string) (model.ExternalResource, error) {
	item, ok := store.items[id]
	if !ok {
		return model.ExternalResource{}, repository.ErrNotFound
	}
	return item, nil
}

// ListExternalResources 返回内存卡片以核对列表入口需要已验证身份。
func (store *externalStoreFake) ListExternalResources(context.Context, string) ([]model.ExternalResource, error) {
	result := make([]model.ExternalResource, 0, len(store.items))
	for _, item := range store.items {
		result = append(result, item)
	}
	return result, nil
}

// UpdateExternalResource 替换测试卡片并模拟未找到响应。
func (store *externalStoreFake) UpdateExternalResource(_ context.Context, value model.ExternalResource) error {
	if _, ok := store.items[value.ID]; !ok {
		return repository.ErrNotFound
	}
	store.items[value.ID] = value
	return nil
}

// DeleteExternalResource 删除测试卡片并模拟未找到响应。
func (store *externalStoreFake) DeleteExternalResource(_ context.Context, id string) error {
	if _, ok := store.items[id]; !ok {
		return repository.ErrNotFound
	}
	delete(store.items, id)
	return nil
}

// TestExternalResourceFlow 覆盖创建、标签规范化、完整替换、删除及无身份拒绝。
func TestExternalResourceFlow(t *testing.T) {
	store := &externalStoreFake{items: map[string]model.ExternalResource{}}
	resources, err := NewExternalResources(store)
	if err != nil {
		t.Fatal(err)
	}
	owner := repository.WithUserID(context.Background(), "owner")
	input := model.ExternalResource{Title: " 资料 ", Location: " https://example.org/file ", ResourceType: "教程", Tags: []string{"Go", "go"}}
	if _, err := resources.Create(context.Background(), input); !errors.Is(err, ErrExternalResourceIdentity) {
		t.Fatalf("create without session: %v", err)
	}
	created, err := resources.Create(owner, input)
	if err != nil || created.Title != "资料" || created.Status != "pending" || len(created.Tags) != 1 || created.Tags[0] != "go" {
		t.Fatalf("created = %+v, err = %v", created, err)
	}
	if len(created.ID) != 32 || created.CreatedAt.IsZero() {
		t.Fatalf("missing id or timestamp: %+v", created)
	}
	created.Note = "手动标记"
	created.Tags = nil
	updated, err := resources.Update(owner, created.ID, created)
	if err != nil || updated.Note != "手动标记" || len(updated.Tags) != 0 {
		t.Fatalf("updated = %+v, err = %v", updated, err)
	}
	if err := resources.Delete(owner, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := resources.Get(owner, created.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("deleted resource still visible: %v", err)
	}
}

// TestExternalResourceRejectsInvalidFields 确保过长、控制字符和非法状态不会到达仓储。
func TestExternalResourceRejectsInvalidFields(t *testing.T) {
	resources, err := NewExternalResources(&externalStoreFake{items: map[string]model.ExternalResource{}})
	if err != nil {
		t.Fatal(err)
	}
	owner := repository.WithUserID(context.Background(), "owner")
	base := model.ExternalResource{Title: "title", Location: "local folder", ResourceType: "book"}
	for _, change := range []struct {
		name string
		edit func(*model.ExternalResource)
	}{
		{"empty title", func(item *model.ExternalResource) { item.Title = " " }},
		{"long location", func(item *model.ExternalResource) { item.Location = strings.Repeat("a", 2049) }},
		{"control character", func(item *model.ExternalResource) { item.Note = "a\nsecret" }},
		{"invalid state", func(item *model.ExternalResource) { item.Status = "unknown" }},
		{"invalid tag", func(item *model.ExternalResource) { item.Tags = []string{"<script>"} }},
	} {
		t.Run(change.name, func(t *testing.T) {
			value := base
			change.edit(&value)
			if _, err := resources.Create(owner, value); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
