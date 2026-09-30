package service

import (
	"context"
	"errors"
	"testing"

	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
)

type savedSearchFake struct{ views map[string]model.SavedSearch }

// CreateSavedSearch 仅记录服务已规范化的条件，模拟重名冲突。
func (store *savedSearchFake) CreateSavedSearch(_ context.Context, view model.SavedSearch) error {
	for _, existing := range store.views {
		if existing.Name == view.Name {
			return repository.ErrSavedSearchConflict
		}
	}
	store.views[view.ID] = view
	return nil
}

// ListSavedSearches 返回独立的视图集合，不读取用户文件内容。
func (store *savedSearchFake) ListSavedSearches(context.Context) ([]model.SavedSearch, error) {
	views := []model.SavedSearch{}
	for _, view := range store.views {
		views = append(views, view)
	}
	return views, nil
}

// DeleteSavedSearch 对不存在的视图返回同样的未找到错误。
func (store *savedSearchFake) DeleteSavedSearch(_ context.Context, id string) error {
	if _, ok := store.views[id]; !ok {
		return repository.ErrNotFound
	}
	delete(store.views, id)
	return nil
}

// TestSavedSearchLifecycle 验证用户身份、名称/筛选校验、保存与删除。
func TestSavedSearchLifecycle(t *testing.T) {
	store := &savedSearchFake{views: map[string]model.SavedSearch{}}
	searches, err := NewSavedSearches(store)
	if err != nil {
		t.Fatal(err)
	}
	owner := repository.WithUserID(context.Background(), "owner")
	if _, err := searches.Create(context.Background(), "未整理", model.SearchFilter{OrganizationStatus: "pending"}); !errors.Is(err, ErrExternalResourceIdentity) {
		t.Fatalf("anonymous create: %v", err)
	}
	for _, name := range []string{"", " 不可\n见"} {
		if _, err := searches.Create(owner, name, model.SearchFilter{Query: "课程"}); !errors.Is(err, ErrInvalidSearch) {
			t.Fatalf("invalid name %q: %v", name, err)
		}
	}
	view, err := searches.Create(owner, " 尚未整理 ", model.SearchFilter{Source: "file", OrganizationStatus: "pending"})
	if err != nil || view.Name != "尚未整理" || view.ID == "" || view.Filter.Source != "file" || view.CreatedAt.IsZero() {
		t.Fatalf("create: %+v, %v", view, err)
	}
	if _, err := searches.Create(owner, "尚未整理", view.Filter); !errors.Is(err, repository.ErrSavedSearchConflict) {
		t.Fatalf("duplicate view name: %v", err)
	}
	if views, err := searches.List(owner); err != nil || len(views) != 1 {
		t.Fatalf("list: %+v, %v", views, err)
	}
	if err := searches.Delete(owner, view.ID); err != nil {
		t.Fatal(err)
	}
	if err := searches.Delete(owner, view.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("repeat delete: %v", err)
	}
}
