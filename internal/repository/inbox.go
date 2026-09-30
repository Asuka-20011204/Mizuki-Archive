package repository

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"mizuki-archive/internal/model"
)

// ensureInboxSchema 在迁移锁内逐项确认列和用户索引；任一 DDL 中断后可从缺失项重试。
func ensureInboxSchema(connection *gorm.DB) error {
	columns := []struct {
		table string
		name  string
	}{
		{table: "resources", name: "organization_status"},
		{table: "external_resources", name: "organization_status"},
	}
	for _, column := range columns {
		var count int64
		if err := connection.Raw(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ? AND data_type = 'varchar' AND character_maximum_length = 16 AND is_nullable = 'NO'`, column.table, column.name).Scan(&count).Error; err != nil {
			return fmt.Errorf("check inbox column %s: %w", column.table, err)
		}
		if count == 1 {
			continue
		}
		var existing int64
		if err := connection.Raw(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?`, column.table, column.name).Scan(&existing).Error; err != nil {
			return fmt.Errorf("check existing inbox column %s: %w", column.table, err)
		}
		if existing != 0 {
			return fmt.Errorf("incompatible inbox column %s.%s", column.table, column.name)
		}
		// 两张表的已有资料此前没有整理状态；迁移后进入待整理队列，用户可逐项确认。
		if err := connection.Exec("ALTER TABLE `" + column.table + "` ADD COLUMN `organization_status` VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'pending'").Error; err != nil {
			return fmt.Errorf("add inbox column %s: %w", column.table, err)
		}
	}
	if err := ensureMultiUserIdentityIndex(connection, "resources", "idx_resources_inbox", "ALTER TABLE `resources` ADD INDEX `idx_resources_inbox` (`user_id`, `organization_status`, `deleted_at`, `created_at`, `id`)", []string{"user_id", "organization_status", "deleted_at", "created_at", "id"}); err != nil {
		return err
	}
	return ensureMultiUserIdentityIndex(connection, "external_resources", "idx_external_resources_inbox", "ALTER TABLE `external_resources` ADD INDEX `idx_external_resources_inbox` (`user_id`, `organization_status`, `created_at`, `id`)", []string{"user_id", "organization_status", "created_at", "id"})
}

// ListPendingResources 严格按会话用户筛选未删除的待整理文件，批量装入标签。
func (store *MySQL) ListPendingResources(ctx context.Context, limit, offset int) ([]model.Resource, error) {
	owner, err := externalOwner(ctx)
	if err != nil {
		return nil, err
	}
	var rows []resourceRow
	if err := store.db.WithContext(ctx).Table("resources").Where("user_id = ? AND organization_status = ? AND deleted_at IS NULL AND archived_at IS NULL", owner, "pending").Order("created_at DESC, id DESC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list pending files: %w", err)
	}
	tags, err := store.loadTags(ctx, resourceIDs(rows))
	if err != nil {
		return nil, err
	}
	items := make([]model.Resource, 0, len(rows))
	for _, row := range rows {
		item := resourceFromRow(row)
		if names, ok := tags[item.ID]; ok {
			item.Tags = names
		}
		items = append(items, item)
	}
	return items, nil
}

// ListPendingExternalResources 严格按会话用户筛选待整理卡片，链接可用性不参与筛选。
func (store *MySQL) ListPendingExternalResources(ctx context.Context, limit, offset int) ([]model.ExternalResource, error) {
	owner, err := externalOwner(ctx)
	if err != nil {
		return nil, err
	}
	var rows []externalResourceRow
	if err := store.db.WithContext(ctx).Table("external_resources").Where("user_id = ? AND organization_status = ? AND archived_at IS NULL", owner, "pending").Order("created_at DESC, id DESC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list pending external resources: %w", err)
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

// SetResourceOrganizationStatus 仅修改当前用户的未删除文件；重试相同状态时保持成功。
func (store *MySQL) SetResourceOrganizationStatus(ctx context.Context, id, status string) error {
	owner, err := externalOwner(ctx)
	if err != nil {
		return err
	}
	result := store.db.WithContext(ctx).Table("resources").Where("id = ? AND user_id = ? AND deleted_at IS NULL", id, owner).Update("organization_status", status)
	if result.Error != nil {
		return fmt.Errorf("update file organization: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		_, err := store.GetResource(ctx, id)
		return err
	}
	return nil
}

// SetExternalOrganizationStatus 仅修改当前用户卡片的整理状态，不修改其链接状态。
func (store *MySQL) SetExternalOrganizationStatus(ctx context.Context, id, status string) error {
	owner, err := externalOwner(ctx)
	if err != nil {
		return err
	}
	result := store.db.WithContext(ctx).Table("external_resources").Where("id = ? AND user_id = ?", id, owner).Update("organization_status", status)
	if result.Error != nil {
		return fmt.Errorf("update external organization: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		_, err := store.GetExternalResource(ctx, id)
		if errors.Is(err, ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

// lockOwnedBatch 用固定来源和 ID 顺序锁住全部条目；任何缺失、越权或软删除都使整批失败。
func lockOwnedBatch(tx *gorm.DB, owner string, items []model.InboxSelection) (map[string][]string, error) {
	groups := map[string][]string{"file": {}, "external": {}}
	seen := make(map[model.InboxSelection]struct{}, len(items))
	for _, item := range items {
		if _, ok := groups[item.Source]; !ok {
			return nil, ErrNotFound
		}
		if _, exists := seen[item]; exists {
			return nil, ErrNotFound
		}
		seen[item] = struct{}{}
		groups[item.Source] = append(groups[item.Source], item.ID)
	}
	// 固定来源与 ID 的锁顺序，减少并发批次交叉操作时的死锁概率。
	for _, source := range []string{"file", "external"} {
		ids := groups[source]
		if len(ids) == 0 {
			continue
		}
		sort.Strings(ids)
		table := "resources"
		if source == "external" {
			table = "external_resources"
		}
		query := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table(table).Where("id IN ? AND user_id = ?", ids, owner)
		if source == "file" {
			query = query.Where("deleted_at IS NULL")
		}
		var found []string
		if err := query.Pluck("id", &found).Error; err != nil {
			return nil, fmt.Errorf("lock batch entries: %w", err)
		}
		if len(found) != len(ids) {
			return nil, ErrNotFound
		}
	}
	return groups, nil
}

// BatchSetOrganizationStatus 在同一事务内锁定并验证所有条目的归属，任何缺失或越权都不更新整批。
func (store *MySQL) BatchSetOrganizationStatus(ctx context.Context, items []model.InboxSelection, status string) error {
	owner, err := externalOwner(ctx)
	if err != nil {
		return err
	}
	return store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
			query := tx.Table(table).Where("id IN ? AND user_id = ?", ids, owner)
			if source == "file" {
				query = query.Where("deleted_at IS NULL")
			}
			if err := query.Update("organization_status", status).Error; err != nil {
				return fmt.Errorf("update batch inbox entries: %w", err)
			}
		}
		return nil
	})
}
