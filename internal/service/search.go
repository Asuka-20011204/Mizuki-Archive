package service

import (
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
)

var ErrInvalidSearch = errors.New("invalid search query")

// Search 统一多来源检索业务；持久层强制对两类结果使用当前登录用户的归属条件。
type Search struct{ store repository.SearchStore }

// NewSearch 要求明确的仓储依赖，不在业务服务里拼接 SQL。
func NewSearch(store repository.SearchStore) (*Search, error) {
	if store == nil {
		return nil, errors.New("missing search store")
	}
	return &Search{store: store}, nil
}

// normalizeSearchFilter 校验可保存的筛选组合；至少有一个条件，避免无意列出整个资料库。
func normalizeSearchFilter(filter model.SearchFilter) (model.SearchFilter, error) {
	for _, field := range []string{filter.Query, filter.Source, filter.Kind, filter.Tag, filter.OrganizationStatus} {
		if strings.IndexFunc(field, unicode.IsControl) >= 0 {
			return model.SearchFilter{}, ErrInvalidSearch
		}
	}
	filter.Query = strings.TrimSpace(filter.Query)
	filter.Source = strings.TrimSpace(filter.Source)
	filter.Kind = strings.TrimSpace(filter.Kind)
	filter.Tag = strings.TrimSpace(filter.Tag)
	filter.OrganizationStatus = strings.TrimSpace(filter.OrganizationStatus)
	if utf8.RuneCountInString(filter.Query) > 100 || utf8.RuneCountInString(filter.Kind) > 40 || utf8.RuneCountInString(filter.Tag) > 40 {
		return model.SearchFilter{}, ErrInvalidSearch
	}
	if filter.Query == "" && filter.Kind == "" && filter.Tag == "" && filter.OrganizationStatus == "" {
		return model.SearchFilter{}, ErrInvalidSearch
	}
	if filter.Source != "" && filter.Source != "file" && filter.Source != "external" {
		return model.SearchFilter{}, ErrInvalidSearch
	}
	if filter.OrganizationStatus != "" && filter.OrganizationStatus != "pending" && filter.OrganizationStatus != "organized" {
		return model.SearchFilter{}, ErrInvalidSearch
	}
	if filter.Tag != "" {
		tags, err := NormalizeTags([]string{filter.Tag})
		if err != nil || len(tags) != 1 {
			return model.SearchFilter{}, ErrInvalidSearch
		}
		filter.Tag = tags[0]
	}
	return filter, nil
}

// Query 保留关键词检索调用方式；筛选检索由 QueryFiltered 执行。
func (search *Search) Query(ctx context.Context, term string, page int) (model.SearchResults, error) {
	return search.QueryFiltered(ctx, model.SearchFilter{Query: term}, page)
}

// QueryFiltered 验证当前用户与所有筛选项，来源独立分页；一类查询失败不返回部分结果。
func (search *Search) QueryFiltered(ctx context.Context, filter model.SearchFilter, page int) (model.SearchResults, error) {
	if _, ok := repository.UserIDFromContext(ctx); !ok {
		return model.SearchResults{}, ErrExternalResourceIdentity
	}
	filter, err := normalizeSearchFilter(filter)
	if err != nil || page < 1 || page > 1000 {
		return model.SearchResults{}, ErrInvalidSearch
	}
	files := []model.Resource{}
	if filter.Source != "external" {
		files, err = search.store.SearchFiles(ctx, filter, 21, (page-1)*20)
		if err != nil {
			return model.SearchResults{}, err
		}
	}
	externals := []model.ExternalResource{}
	if filter.Source != "file" {
		externals, err = search.store.SearchExternal(ctx, filter, 21, (page-1)*20)
		if err != nil {
			return model.SearchResults{}, err
		}
	}
	results := model.SearchResults{Files: files, ExternalResources: externals, Page: page, HasMoreFiles: len(files) > 20, HasMoreExternal: len(externals) > 20}
	if results.HasMoreFiles {
		results.Files = files[:20]
	}
	if results.HasMoreExternal {
		results.ExternalResources = externals[:20]
	}
	return results, nil
}
