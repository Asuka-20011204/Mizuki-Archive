package service

import (
	"context"
	"errors"

	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
)

// BatchFavorites 以明确布尔目标管理文件和卡片收藏，避免重试时重复反转。
type BatchFavorites struct{ store repository.BatchFavoriteStore }

// NewBatchFavorites 要求可原子校验归属的仓储依赖，否则不开放路由。
func NewBatchFavorites(store repository.BatchFavoriteStore) (*BatchFavorites, error) {
	if store == nil {
		return nil, errors.New("missing batch favorite store")
	}
	return &BatchFavorites{store: store}, nil
}

// Apply 先校验会话和全部条目，再按当前用户边界请求同事务更新。
func (batch *BatchFavorites) Apply(ctx context.Context, items []model.InboxSelection, favorite bool) ([]model.InboxSelection, error) {
	if _, ok := repository.UserIDFromContext(ctx); !ok {
		return nil, ErrInboxIdentity
	}
	if err := validateBatchSelections(items); err != nil {
		return nil, err
	}
	return batch.store.BatchUpdateFavorites(ctx, items, favorite)
}
