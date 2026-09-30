package repository

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"mizuki-archive/internal/model"
)

// ensureDuplicateSchema 在迁移锁内补齐列和索引，再分批填充旧卡片的链接键；DDL 中断后可重试。
func ensureDuplicateSchema(connection *gorm.DB) error {
	rows, err := connection.Raw(`SELECT column_type, character_set_name, collation_name, is_nullable FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'external_resources' AND column_name = 'location_key'`).Rows()
	if err != nil {
		return fmt.Errorf("check external link key column: %w", err)
	}
	exists := rows.Next()
	if exists {
		var columnType, charset, collation, nullable string
		if err := rows.Scan(&columnType, &charset, &collation, &nullable); err != nil {
			rows.Close()
			return fmt.Errorf("read external link key column: %w", err)
		}
		if rows.Next() || !strings.EqualFold(columnType, "char(64)") || !strings.EqualFold(charset, "ascii") || !strings.EqualFold(collation, "ascii_bin") || !strings.EqualFold(nullable, "YES") {
			rows.Close()
			return fmt.Errorf("incompatible external_resources.location_key column")
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate external link key column: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close external link key query: %w", err)
	}
	if !exists {
		if err := connection.Exec("ALTER TABLE external_resources ADD COLUMN location_key CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL").Error; err != nil {
			return fmt.Errorf("add external link key: %w", err)
		}
	}
	if err := ensureMultiUserIdentityIndex(connection, "resources", "idx_resources_duplicate", "ALTER TABLE resources ADD INDEX idx_resources_duplicate (user_id, sha256, deleted_at, id)", []string{"user_id", "sha256", "deleted_at", "id"}); err != nil {
		return err
	}
	if err := ensureMultiUserIdentityIndex(connection, "external_resources", "idx_external_resources_duplicate", "ALTER TABLE external_resources ADD INDEX idx_external_resources_duplicate (user_id, location_key, id)", []string{"user_id", "location_key", "id"}); err != nil {
		return err
	}
	return backfillExternalLinkKeys(connection)
}

// backfillExternalLinkKeys 为升级前的卡片分批计算纯文本摘要；空键记录为无效位置，不再重复扫描。
func backfillExternalLinkKeys(connection *gorm.DB) error {
	for {
		var rows []struct {
			ID       string `gorm:"column:id"`
			Location string `gorm:"column:location"`
		}
		if err := connection.Table("external_resources").Select("id, location").Where("location_key IS NULL").Order("id").Limit(200).Find(&rows).Error; err != nil {
			return fmt.Errorf("read unindexed external links: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			if err := connection.Table("external_resources").Where("id = ? AND location_key IS NULL", row.ID).Update("location_key", model.ExternalLinkKey(row.Location)).Error; err != nil {
				return fmt.Errorf("backfill external link key: %w", err)
			}
		}
	}
}

// FindFileDuplicate 只返回同账号未删除且哈希相同的另一份资料，不暴露存储路径。
func (store *MySQL) FindFileDuplicate(ctx context.Context, hash, excludeID string) (*model.DuplicateHint, error) {
	owner, err := externalOwner(ctx)
	if err != nil {
		return nil, err
	}
	var result struct {
		ID   string `gorm:"column:id"`
		Name string `gorm:"column:name"`
	}
	err = store.db.WithContext(ctx).Table("resources").Select("id, name").Where("user_id = ? AND sha256 = ? AND deleted_at IS NULL AND id <> ?", owner, hash, excludeID).Order("id").Take(&result).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find file duplicate: %w", err)
	}
	return &model.DuplicateHint{ID: result.ID, Name: result.Name}, nil
}

// FindExternalDuplicate 只查询同账号相同规范化链接的另一张卡片；不访问站外地址。
func (store *MySQL) FindExternalDuplicate(ctx context.Context, key, excludeID string) (*model.DuplicateHint, error) {
	owner, err := externalOwner(ctx)
	if err != nil {
		return nil, err
	}
	if key == "" {
		return nil, nil
	}
	var result struct {
		ID   string `gorm:"column:id"`
		Name string `gorm:"column:title"`
	}
	err = store.db.WithContext(ctx).Table("external_resources").Select("id, title").Where("user_id = ? AND location_key = ? AND id <> ?", owner, key, excludeID).Order("id").Take(&result).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find external duplicate: %w", err)
	}
	return &model.DuplicateHint{ID: result.ID, Name: result.Name}, nil
}
