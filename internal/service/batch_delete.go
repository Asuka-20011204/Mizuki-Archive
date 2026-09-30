package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
)

// BatchDeleteResult 区分已提交的删除数量与提交后原件清理失败数量，避免误导用户重试整批。
type BatchDeleteResult struct {
	Deleted        int `json:"deleted"`
	CleanupPending int `json:"cleanup_pending"`
}

// BatchDelete 让用户范围的数据库事务先完成，随后才清理站内文件和关联缓存。
type BatchDelete struct {
	store     repository.BatchDeleteStore
	resources *Resources
}

// NewBatchDelete 不接受缺失依赖，避免只删数据库而没有原件清理能力。
func NewBatchDelete(store repository.BatchDeleteStore, resources *Resources) (*BatchDelete, error) {
	if store == nil || resources == nil {
		return nil, errors.New("missing batch delete dependency")
	}
	return &BatchDelete{store: store, resources: resources}, nil
}

// Apply 先校验会话及整个批次，事务失败不碰原件；清理失败只报告后续待处理项。
func (batch *BatchDelete) Apply(ctx context.Context, items []model.InboxSelection) (BatchDeleteResult, error) {
	if _, ok := repository.UserIDFromContext(ctx); !ok {
		return BatchDeleteResult{}, ErrInboxIdentity
	}
	if err := validateBatchSelections(items); err != nil {
		return BatchDeleteResult{}, err
	}
	files, err := batch.store.BatchDeleteEntries(ctx, items)
	if err != nil {
		return BatchDeleteResult{}, err
	}
	result := BatchDeleteResult{Deleted: len(items)}
	for _, id := range files.OriginalIDs {
		if err := batch.cleanupEntry(ctx, repository.PendingFileCleanup{Kind: "original", ID: id}); err != nil {
			result.CleanupPending++
		}
	}
	for _, id := range files.DerivedIDs {
		if err := batch.cleanupEntry(ctx, repository.PendingFileCleanup{Kind: "derived", ID: id}); err != nil {
			result.CleanupPending++
		}
	}
	return result, nil
}

// DrainPendingCleanup 有界重试事务中留下的文件清理项；一项失败不阻断其他项。
func (batch *BatchDelete) DrainPendingCleanup(ctx context.Context, limit int) (int, int, error) {
	entries, err := batch.store.ListPendingFileCleanup(ctx, limit)
	if err != nil {
		return 0, 0, err
	}
	completed, pending := 0, 0
	for _, entry := range entries {
		if err := batch.cleanupEntry(ctx, entry); err != nil {
			pending++
		} else {
			completed++
		}
	}
	return completed, pending, nil
}

// cleanupEntry 仅允许两个受控目录；只有文件已删或本来不存在时才移除重试记录。
func (batch *BatchDelete) cleanupEntry(ctx context.Context, entry repository.PendingFileCleanup) error {
	var err error
	switch entry.Kind {
	case "original":
		err = batch.resources.cleanupDeletedResource(ctx, entry.ID)
	case "derived":
		err = batch.cleanupDerivedAsset(entry.ID)
	default:
		return ErrFileUnavailable
	}
	if err != nil {
		_ = batch.store.DeferFileCleanup(ctx, entry)
		return err
	}
	if err := batch.store.CompleteFileCleanup(ctx, entry); err != nil {
		_ = batch.store.DeferFileCleanup(ctx, entry)
		return err
	}
	return nil
}

// cleanupDerivedAsset 只使用数据库中由服务端生成的十六进制 ID，绝不拼接用户提供的文件名。
func (batch *BatchDelete) cleanupDerivedAsset(id string) error {
	if !validResourceID(id) {
		return ErrFileUnavailable
	}
	err := os.Remove(filepath.Join(batch.resources.dataDir, "derived", id))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
