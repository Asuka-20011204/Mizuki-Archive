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

// Query 限制搜索输入与分页范围，每类最多返回 20 条；任何一类查询失败都不返回部分结果。
func (search *Search) Query(ctx context.Context, term string, page int) (model.SearchResults, error) {
	if _, ok := repository.UserIDFromContext(ctx); !ok {
		return model.SearchResults{}, ErrExternalResourceIdentity
	}
	if strings.IndexFunc(term, unicode.IsControl) >= 0 {
		return model.SearchResults{}, ErrInvalidSearch
	}
	term = strings.TrimSpace(term)
	if utf8.RuneCountInString(term) == 0 || utf8.RuneCountInString(term) > 100 || page < 1 || page > 1000 {
		return model.SearchResults{}, ErrInvalidSearch
	}
	files, err := search.store.SearchFiles(ctx, term, 21, (page-1)*20)
	if err != nil {
		return model.SearchResults{}, err
	}
	externals, err := search.store.SearchExternal(ctx, term, 21, (page-1)*20)
	if err != nil {
		return model.SearchResults{}, err
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
