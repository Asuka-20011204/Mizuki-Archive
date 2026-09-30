package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
)

type searchStoreFake struct {
	files     []model.Resource
	externals []model.ExternalResource
	filter    model.SearchFilter
	limit     int
	offset    int
	fail      bool
}

// SearchFiles 模拟有限窗口查询，记录业务层传入的页码和关键词。
func (store *searchStoreFake) SearchFiles(_ context.Context, filter model.SearchFilter, limit, offset int) ([]model.Resource, error) {
	store.filter, store.limit, store.offset = filter, limit, offset
	if store.fail {
		return nil, errors.New("file search failed")
	}
	return store.files, nil
}

// SearchExternal 模拟卡片窗口查询，文件查询失败时不会调用此方法。
func (store *searchStoreFake) SearchExternal(_ context.Context, filter model.SearchFilter, limit, offset int) ([]model.ExternalResource, error) {
	store.filter, store.limit, store.offset = filter, limit, offset
	if store.fail {
		return nil, errors.New("card search failed")
	}
	return store.externals, nil
}

// TestSearchValidationAndPagination 验证身份、输入边界、分页及两类来源互不覆盖。
func TestSearchValidationAndPagination(t *testing.T) {
	store := &searchStoreFake{files: make([]model.Resource, 21), externals: make([]model.ExternalResource, 2)}
	search, err := NewSearch(store)
	if err != nil {
		t.Fatal(err)
	}
	ctx := repository.WithUserID(context.Background(), "owner")
	for _, term := range []string{"", "  ", strings.Repeat("测", 101), "\x00", "文件\n名", "文件\u0085名"} {
		if _, err := search.Query(ctx, term, 1); !errors.Is(err, ErrInvalidSearch) {
			t.Fatalf("invalid term %q: %v", term, err)
		}
	}
	for _, page := range []int{0, 1001} {
		if _, err := search.Query(ctx, "课程", page); !errors.Is(err, ErrInvalidSearch) {
			t.Fatalf("invalid page %d: %v", page, err)
		}
	}
	if _, err := search.Query(context.Background(), "课程", 1); !errors.Is(err, ErrExternalResourceIdentity) {
		t.Fatalf("missing identity: %v", err)
	}
	results, err := search.Query(ctx, " 课程 ", 3)
	if err != nil || store.filter.Query != "课程" || store.limit != 21 || store.offset != 40 || results.Page != 3 || len(results.Files) != 20 || len(results.ExternalResources) != 2 || !results.HasMoreFiles || results.HasMoreExternal {
		t.Fatalf("paginated grouped search: %+v, %+v, %v", results, store, err)
	}
	store.fail = true
	if results, err := search.Query(ctx, "课程", 1); err == nil || len(results.Files) != 0 {
		t.Fatalf("file failure returned partial results: %+v, %v", results, err)
	}
}

// TestSearchFilteredSourceAndValidation 验证状态可独立检索、来源跳过不相关仓储、非法组合被拒绝。
func TestSearchFilteredSourceAndValidation(t *testing.T) {
	store := &searchStoreFake{files: []model.Resource{{ID: "file-a"}}, externals: []model.ExternalResource{{ID: "card-a"}}}
	search, err := NewSearch(store)
	if err != nil {
		t.Fatal(err)
	}
	ctx := repository.WithUserID(context.Background(), "owner")
	for _, filter := range []model.SearchFilter{{}, {Source: "file"}, {Query: "valid", Source: "other"}, {OrganizationStatus: "unknown"}, {Query: "hello\nworld"}, {Tag: "<script>"}} {
		if _, err := search.QueryFiltered(ctx, filter, 1); !errors.Is(err, ErrInvalidSearch) {
			t.Fatalf("invalid filter %+v: %v", filter, err)
		}
	}
	result, err := search.QueryFiltered(ctx, model.SearchFilter{Source: "external", OrganizationStatus: "pending"}, 1)
	if err != nil || len(result.Files) != 0 || len(result.ExternalResources) != 1 || store.filter.OrganizationStatus != "pending" {
		t.Fatalf("external-only view: %+v, %v", result, err)
	}
	result, err = search.QueryFiltered(ctx, model.SearchFilter{Source: "file", Kind: "pdf", Tag: "学习"}, 1)
	if err != nil || len(result.Files) != 1 || len(result.ExternalResources) != 0 || store.filter.Tag != "学习" {
		t.Fatalf("file-only view: %+v, %v", result, err)
	}
}
