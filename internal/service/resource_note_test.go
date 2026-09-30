package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
)

// noteStoreFake 保留实际文件归属约束，测试不能用不安全的全局 Map 假装越权被拒绝。
type noteStoreFake struct {
	files map[string]model.Resource
	notes map[string]model.ResourceNote
}

// GetResource 按会话用户读取未删除文件，其他账号统一返回未找到。
func (store *noteStoreFake) GetResource(ctx context.Context, id string) (model.Resource, error) {
	owner, _ := repository.UserIDFromContext(ctx)
	resource, ok := store.files[id]
	if !ok || resource.OwnerID != owner || owner == "" {
		return model.Resource{}, repository.ErrNotFound
	}
	return resource, nil
}

// CreateResourceNote 保存已通过服务层校验的注记，供读写流程断言。
func (store *noteStoreFake) CreateResourceNote(_ context.Context, note model.ResourceNote) error {
	store.notes[note.ID] = note
	return nil
}

// ListResourceNotes 只返回该文件的注记，服务层负责先验证文件身份。
func (store *noteStoreFake) ListResourceNotes(_ context.Context, resourceID string) ([]model.ResourceNote, error) {
	result := []model.ResourceNote{}
	for _, note := range store.notes {
		if note.ResourceID == resourceID {
			result = append(result, note)
		}
	}
	return result, nil
}

// UpdateResourceNote 仅覆盖存在且属于同一文件的注记。
func (store *noteStoreFake) UpdateResourceNote(_ context.Context, note model.ResourceNote) error {
	if previous, ok := store.notes[note.ID]; !ok || previous.ResourceID != note.ResourceID {
		return repository.ErrNotFound
	}
	store.notes[note.ID] = note
	return nil
}

// DeleteResourceNote 仅删除指定文件中的目标注记。
func (store *noteStoreFake) DeleteResourceNote(_ context.Context, resourceID, noteID string) error {
	if note, ok := store.notes[noteID]; !ok || note.ResourceID != resourceID {
		return repository.ErrNotFound
	}
	delete(store.notes, noteID)
	return nil
}

// TestResourceNoteFlow 验证 PDF 页码、私人注记读写删除和跨账号不可见。
func TestResourceNoteFlow(t *testing.T) {
	resourceID := strings.Repeat("a", 32)
	store := &noteStoreFake{files: map[string]model.Resource{resourceID: {ID: resourceID, OwnerID: "user-a", Kind: "pdf"}}, notes: map[string]model.ResourceNote{}}
	service, err := NewResourceNotes(store)
	if err != nil {
		t.Fatal(err)
	}
	userA := repository.WithUserID(context.Background(), "user-a")
	userB := repository.WithUserID(context.Background(), "user-b")
	page := 7
	input := model.ResourceNote{PageNumber: &page, Excerpt: " 源文本 ", Content: " 我理解的内容 ", Source: " 第一章 "}
	if _, err := service.Create(context.Background(), resourceID, input); !errors.Is(err, ErrExternalResourceIdentity) {
		t.Fatalf("anonymous create: %v", err)
	}
	if _, err := service.Create(userB, resourceID, input); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("other user create: %v", err)
	}
	created, err := service.Create(userA, resourceID, input)
	if err != nil || created.Excerpt != "源文本" || created.PageNumber == nil || *created.PageNumber != 7 || len(created.ID) != 32 {
		t.Fatalf("create: %+v, %v", created, err)
	}
	if items, err := service.List(userB, resourceID); !errors.Is(err, repository.ErrNotFound) || items != nil {
		t.Fatalf("other user list: %+v, %v", items, err)
	}
	if err := service.Update(userB, resourceID, created.ID, input); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("other user update: %v", err)
	}
	if err := service.Delete(userB, resourceID, created.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("other user delete: %v", err)
	}
	input.Content = "修订内容"
	if err := service.Update(userA, resourceID, created.ID, input); err != nil {
		t.Fatal(err)
	}
	if items, err := service.List(userA, resourceID); err != nil || len(items) != 1 || items[0].Content != "修订内容" {
		t.Fatalf("owner list: %+v, %v", items, err)
	}
	if err := service.Delete(userA, resourceID, created.ID); err != nil {
		t.Fatal(err)
	}
	if items, err := service.List(userA, resourceID); err != nil || len(items) != 0 {
		t.Fatalf("after delete: %+v, %v", items, err)
	}
}

// TestResourceNoteValidation 阻止无正文、非法页码、超长字段和隐藏控制字符落库。
func TestResourceNoteValidation(t *testing.T) {
	resourceID := strings.Repeat("b", 32)
	store := &noteStoreFake{files: map[string]model.Resource{resourceID: {ID: resourceID, OwnerID: "user-a", Kind: "text"}}, notes: map[string]model.ResourceNote{}}
	service, err := NewResourceNotes(store)
	if err != nil {
		t.Fatal(err)
	}
	ctx := repository.WithUserID(context.Background(), "user-a")
	page := 2
	for _, input := range []model.ResourceNote{
		{}, {Content: "body", PageNumber: &page}, {Content: strings.Repeat("x", 5001)},
		{Excerpt: "quote\x00hidden"}, {Content: "body", Source: "line\nother"},
	} {
		if _, err := service.Create(ctx, resourceID, input); !errors.Is(err, ErrInvalidResourceNote) {
			t.Fatalf("invalid input accepted: %v", err)
		}
	}
	if _, err := service.Create(ctx, resourceID, model.ResourceNote{Content: "多行\n笔记"}); err != nil {
		t.Fatalf("normal multiline note rejected: %v", err)
	}
}
