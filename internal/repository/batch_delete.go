package repository

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"mizuki-archive/internal/model"
)

// BatchDeleteEntries 先锁住全部本人条目和任务，再软删除文件、清除私人笔记/派生索引与卡片。
// 原件及派生文件由 Service 在事务成功后清理；提交前绝不访问文件系统或外部位置。
func (store *MySQL) BatchDeleteEntries(ctx context.Context, items []model.InboxSelection) (BatchDeletedFiles, error) {
	owner, err := externalOwner(ctx)
	if err != nil {
		return BatchDeletedFiles{}, err
	}
	if len(items) == 0 || len(items) > 50 {
		return BatchDeletedFiles{}, fmt.Errorf("invalid delete batch")
	}
	files := BatchDeletedFiles{}
	err = store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		groups, err := lockOwnedBatch(tx, owner, items)
		if err != nil {
			return err
		}
		if err := removeRelationsForEntries(tx, owner, items); err != nil {
			return err
		}
		if err := removeTopicReferences(tx, owner, items); err != nil {
			return err
		}
		files.OriginalIDs = groups["file"]
		if len(files.OriginalIDs) > 0 {
			// 锁住任务后再读取其产物，避免执行中的 Worker 在收集 ID 后写入无法清理的新产物。
			var jobs []string
			if err := tx.Table("processing_jobs").Clauses(clause.Locking{Strength: "UPDATE"}).Where("resource_id IN ?", files.OriginalIDs).Pluck("id", &jobs).Error; err != nil {
				return fmt.Errorf("lock batch processing jobs: %w", err)
			}
			if err := tx.Table("derived_assets").Clauses(clause.Locking{Strength: "UPDATE"}).Where("resource_id IN ?", files.OriginalIDs).Pluck("id", &files.DerivedIDs).Error; err != nil {
				return fmt.Errorf("read batch derived assets: %w", err)
			}
			for _, entry := range append(cleanupEntries("original", files.OriginalIDs), cleanupEntries("derived", files.DerivedIDs)...) {
				if err := tx.Table("pending_file_cleanup").Create(&entry).Error; err != nil {
					return fmt.Errorf("record batch file cleanup: %w", err)
				}
			}
			result := tx.Table("resources").Where("id IN ? AND user_id = ? AND deleted_at IS NULL", files.OriginalIDs, owner).Update("deleted_at", time.Now().UTC())
			if result.Error != nil {
				return fmt.Errorf("mark batch files deleted: %w", result.Error)
			}
			if result.RowsAffected != int64(len(files.OriginalIDs)) {
				return ErrNotFound
			}
			// 与文件软删除同一事务清理私人注记，不能只让 HTTP 隐藏仍在数据库的明文。
			if err := tx.Exec("DELETE FROM resource_notes WHERE resource_id IN ? AND user_id = ?", files.OriginalIDs, owner).Error; err != nil {
				return fmt.Errorf("clear batch file notes: %w", err)
			}
			if err := tx.Exec("DELETE FROM resource_tags WHERE resource_id IN ?", files.OriginalIDs).Error; err != nil {
				return fmt.Errorf("clear batch file tags: %w", err)
			}
			if len(jobs) > 0 {
				// FK 级联删除派生正文及 outbox，旧消息到达时会因任务不存在而被安全忽略。
				if err := tx.Table("processing_jobs").Where("id IN ?", jobs).Delete(&processingJobRow{}).Error; err != nil {
					return fmt.Errorf("clear batch processing history: %w", err)
				}
			}
		}
		if cardIDs := groups["external"]; len(cardIDs) > 0 {
			result := tx.Table("external_resources").Where("id IN ? AND user_id = ?", cardIDs, owner).Delete(&externalResourceRow{})
			if result.Error != nil {
				return fmt.Errorf("delete batch cards: %w", result.Error)
			}
			if result.RowsAffected != int64(len(cardIDs)) {
				return ErrNotFound
			}
		}
		return nil
	})
	if err != nil {
		return BatchDeletedFiles{}, err
	}
	return files, nil
}

// cleanupEntries 把受控 ID 转成事务内持久清理记录，供进程重启后继续处理。
func cleanupEntries(kind string, ids []string) []pendingFileCleanupRow {
	entries := make([]pendingFileCleanupRow, 0, len(ids))
	for _, id := range ids {
		now := time.Now().UTC()
		entries = append(entries, pendingFileCleanupRow{Kind: kind, StorageKey: id, CreatedAt: now, RetryAfter: now})
	}
	return entries
}

// pendingFileCleanupRow 对应持久清理表；主键避免同一个路径被重复登记。
type pendingFileCleanupRow struct {
	Kind       string    `gorm:"column:kind;primaryKey"`
	StorageKey string    `gorm:"column:storage_key;primaryKey"`
	CreatedAt  time.Time `gorm:"column:created_at"`
	RetryAfter time.Time `gorm:"column:retry_after"`
}

// ListPendingFileCleanup 按时间有界读取所有待清理文件，不在日志或 HTTP 中暴露私有路径。
func (store *MySQL) ListPendingFileCleanup(ctx context.Context, limit int) ([]PendingFileCleanup, error) {
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("invalid cleanup limit")
	}
	var rows []pendingFileCleanupRow
	if err := store.db.WithContext(ctx).Table("pending_file_cleanup").Where("retry_after <= ?", time.Now().UTC()).Order("retry_after ASC, kind ASC, storage_key ASC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list pending file cleanup: %w", err)
	}
	entries := make([]PendingFileCleanup, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, PendingFileCleanup{Kind: row.Kind, ID: row.StorageKey})
	}
	return entries, nil
}

// CompleteFileCleanup 只移除已经清理或已不存在的文件记录，重复完成同一记录安全无副作用。
func (store *MySQL) CompleteFileCleanup(ctx context.Context, entry PendingFileCleanup) error {
	if err := store.db.WithContext(ctx).Table("pending_file_cleanup").Where("kind = ? AND storage_key = ?", entry.Kind, entry.ID).Delete(&pendingFileCleanupRow{}).Error; err != nil {
		return fmt.Errorf("complete file cleanup: %w", err)
	}
	return nil
}

// DeferFileCleanup 让不可访问存储暂缓五分钟，防止反复失败的老项饿死后续清理任务。
func (store *MySQL) DeferFileCleanup(ctx context.Context, entry PendingFileCleanup) error {
	if err := store.db.WithContext(ctx).Table("pending_file_cleanup").Where("kind = ? AND storage_key = ?", entry.Kind, entry.ID).Update("retry_after", time.Now().UTC().Add(5*time.Minute)).Error; err != nil {
		return fmt.Errorf("defer file cleanup: %w", err)
	}
	return nil
}
