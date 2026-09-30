package service

import (
	"context"
	"errors"

	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
)

// BatchTags 仅管理标签批量增删，不触碰资料原件、链接状态或整理状态。
type BatchTags struct{ store repository.BatchTagStore }

// NewBatchTags 只有注入支持事务与归属检查的仓储后才启用批量标签路由。
func NewBatchTags(store repository.BatchTagStore) (*BatchTags, error) {
	if store == nil {
		return nil, errors.New("missing batch tag store")
	}
	return &BatchTags{store: store}, nil
}

// validateBatchSelections 限制混合来源批次的数量、ID 和重复项，供整理与标签操作共用。
func validateBatchSelections(items []model.InboxSelection) error {
	if len(items) == 0 || len(items) > 50 {
		return ErrInvalidInboxInput
	}
	seen := make(map[model.InboxSelection]struct{}, len(items))
	for _, item := range items {
		if (item.Source != "file" && item.Source != "external") || !validResourceID(item.ID) {
			return ErrInvalidInboxInput
		}
		if _, duplicate := seen[item]; duplicate {
			return ErrInvalidInboxInput
		}
		seen[item] = struct{}{}
	}
	return nil
}

// Apply 在当前会话用户范围内增删一个规范标签，返回真实变化项作为一次撤销的最小依据。
func (batch *BatchTags) Apply(ctx context.Context, items []model.InboxSelection, tag, mode string) ([]model.InboxSelection, error) {
	if _, ok := repository.UserIDFromContext(ctx); !ok {
		return nil, ErrInboxIdentity
	}
	if err := validateBatchSelections(items); err != nil || (mode != "add" && mode != "remove") {
		return nil, ErrInvalidInboxInput
	}
	normalized, err := NormalizeTag(tag)
	if err != nil {
		return nil, err
	}
	return batch.store.BatchUpdateTags(ctx, items, normalized, mode)
}
