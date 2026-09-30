package repository

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
	"mizuki-archive/internal/model"
)

// ListArchivedResources 只列出当前用户未删除的归档文件，并一次性装入标签。
func (store *MySQL) ListArchivedResources(ctx context.Context, limit, offset int) ([]model.Resource, error) {
	owner, err := externalOwner(ctx)
	if err != nil {
		return nil, err
	}
	var rows []resourceRow
	if err := store.db.WithContext(ctx).Table("resources").Where("user_id = ? AND archived_at IS NOT NULL AND deleted_at IS NULL", owner).Order("archived_at DESC, created_at DESC, id DESC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list archived files: %w", err)
	}
	tags, err := store.loadTags(ctx, resourceIDs(rows))
	if err != nil {
		return nil, err
	}
	items := make([]model.Resource, 0, len(rows))
	for _, row := range rows {
		item := resourceFromRow(row)
		item.Tags = append(item.Tags, tags[item.ID]...)
		items = append(items, item)
	}
	return items, nil
}

// ListArchivedExternalResources 只列出当前用户归档的外部卡片；不访问其位置。
func (store *MySQL) ListArchivedExternalResources(ctx context.Context, limit, offset int) ([]model.ExternalResource, error) {
	owner, err := externalOwner(ctx)
	if err != nil {
		return nil, err
	}
	var rows []externalResourceRow
	if err := store.db.WithContext(ctx).Table("external_resources").Where("user_id = ? AND archived_at IS NOT NULL", owner).Order("archived_at DESC, created_at DESC, id DESC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list archived cards: %w", err)
	}
	items := make([]model.ExternalResource, 0, len(rows))
	for _, row := range rows {
		items = append(items, externalFromRow(row))
	}
	if err := attachExternalTags(ctx, store.db, items); err != nil {
		return nil, err
	}
	return items, nil
}

// BatchSetArchived 先锁住并核验两类所有者，再只修改目标不同的条目；任何失败都回滚整批。
func (store *MySQL) BatchSetArchived(ctx context.Context, items []model.InboxSelection, archived bool) ([]model.InboxSelection, error) {
	owner, err := externalOwner(ctx)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 || len(items) > 50 {
		return nil, fmt.Errorf("invalid archive batch")
	}
	changed := make([]model.InboxSelection, 0, len(items))
	err = store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		groups, err := lockOwnedBatch(tx, owner, items)
		if err != nil {
			return err
		}
		for _, source := range []string{"file", "external"} {
			ids := groups[source]
			if len(ids) == 0 {
				continue
			}
			table := "resources"
			if source == "external" {
				table = "external_resources"
			}
			var rows []struct {
				ID         string     `gorm:"column:id"`
				ArchivedAt *time.Time `gorm:"column:archived_at"`
			}
			if err := tx.Table(table).Select("id, archived_at").Where("id IN ? AND user_id = ?", ids, owner).Find(&rows).Error; err != nil {
				return fmt.Errorf("read archive states: %w", err)
			}
			var timestamp any
			if archived {
				timestamp = time.Now().UTC()
			}
			for _, row := range rows {
				if (row.ArchivedAt != nil) == archived {
					continue
				}
				result := tx.Table(table).Where("id = ? AND user_id = ?", row.ID, owner).Update("archived_at", timestamp)
				if result.Error != nil {
					return fmt.Errorf("update archive state: %w", result.Error)
				}
				if result.RowsAffected != 1 {
					return fmt.Errorf("archive state changed unexpectedly")
				}
				changed = append(changed, model.InboxSelection{Source: source, ID: row.ID})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return changed, nil
}
