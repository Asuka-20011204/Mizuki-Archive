package repository

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// ensureArchiveSchema 在迁移锁内逐表建列和索引，兼容 DDL 成功但版本登记中断后的重试。
func ensureArchiveSchema(connection *gorm.DB) error {
	statements := strings.Split(resourceArchiveMigration, ";")
	for index, table := range []string{"resources", "external_resources"} {
		var count int64
		if err := connection.Raw(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name = 'archived_at'`, table).Scan(&count).Error; err != nil {
			return fmt.Errorf("check archive column %s: %w", table, err)
		}
		if count == 0 {
			if err := connection.Exec(strings.TrimSpace(statements[index])).Error; err != nil {
				return fmt.Errorf("add archive column %s: %w", table, err)
			}
		}
		var valid int64
		if err := connection.Raw(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name = 'archived_at' AND data_type = 'datetime' AND datetime_precision = 6 AND is_nullable = 'YES'`, table).Scan(&valid).Error; err != nil {
			return fmt.Errorf("validate archive column %s: %w", table, err)
		}
		if valid != 1 {
			return fmt.Errorf("incompatible archive column %s.archived_at", table)
		}
	}
	if err := ensureMultiUserIdentityIndex(connection, "resources", "idx_resources_archive", "ALTER TABLE `resources` ADD INDEX `idx_resources_archive` (`user_id`, `archived_at`, `created_at`, `id`)", []string{"user_id", "archived_at", "created_at", "id"}); err != nil {
		return err
	}
	return ensureMultiUserIdentityIndex(connection, "external_resources", "idx_external_archive", "ALTER TABLE `external_resources` ADD INDEX `idx_external_archive` (`user_id`, `archived_at`, `created_at`, `id`)", []string{"user_id", "archived_at", "created_at", "id"})
}
