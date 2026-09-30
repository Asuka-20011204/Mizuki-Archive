package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
)

// SavedSearches 管理当前用户收藏的检索条件；结果始终在查询时重新按用户权限计算。
type SavedSearches struct{ store repository.SavedSearchStore }

// NewSavedSearches 要求显式注入私有视图仓储。
func NewSavedSearches(store repository.SavedSearchStore) (*SavedSearches, error) {
	if store == nil {
		return nil, errors.New("missing saved search store")
	}
	return &SavedSearches{store: store}, nil
}

// Create 校验名称与筛选条件后生成随机 ID；不会保存资料正文或实际搜索结果。
func (searches *SavedSearches) Create(ctx context.Context, name string, filter model.SearchFilter) (model.SavedSearch, error) {
	if _, ok := repository.UserIDFromContext(ctx); !ok {
		return model.SavedSearch{}, ErrExternalResourceIdentity
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return model.SavedSearch{}, ErrInvalidSearch
	}
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 60 {
		return model.SavedSearch{}, ErrInvalidSearch
	}
	filter, err := normalizeSearchFilter(filter)
	if err != nil {
		return model.SavedSearch{}, err
	}
	identifier := make([]byte, 16)
	if _, err := rand.Read(identifier); err != nil {
		return model.SavedSearch{}, err
	}
	view := model.SavedSearch{ID: hex.EncodeToString(identifier), Name: name, Filter: filter, CreatedAt: time.Now().UTC()}
	if err := searches.store.CreateSavedSearch(ctx, view); err != nil {
		return model.SavedSearch{}, err
	}
	return view, nil
}

// List 只读取已验证用户保存的检索条件，不自动执行高成本搜索。
func (searches *SavedSearches) List(ctx context.Context) ([]model.SavedSearch, error) {
	if _, ok := repository.UserIDFromContext(ctx); !ok {
		return nil, ErrExternalResourceIdentity
	}
	return searches.store.ListSavedSearches(ctx)
}

// Delete 只删除当前账号保存的视图，不删除任何资料、标签或原文件。
func (searches *SavedSearches) Delete(ctx context.Context, id string) error {
	if _, ok := repository.UserIDFromContext(ctx); !ok {
		return ErrExternalResourceIdentity
	}
	if !validResourceID(id) {
		return repository.ErrNotFound
	}
	return searches.store.DeleteSavedSearch(ctx, id)
}
