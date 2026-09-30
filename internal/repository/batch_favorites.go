package repository

import (
	"context"
	"fmt"

	"gorm.io/gorm"
	"mizuki-archive/internal/model"
)

// BatchUpdateFavorites 在同一事务中更新文件和外部卡片的收藏目标，并返回实际变化项。
func (store *MySQL) BatchUpdateFavorites(ctx context.Context, items []model.InboxSelection, favorite bool) ([]model.InboxSelection, error) {
	owner, err := externalOwner(ctx)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 || len(items) > 50 {
		return nil, fmt.Errorf("invalid batch favorite request")
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
				ID       string `gorm:"column:id"`
				Favorite bool   `gorm:"column:favorite"`
			}
			if err := tx.Table(table).Select("id, favorite").Where("id IN ? AND user_id = ?", ids, owner).Find(&rows).Error; err != nil {
				return fmt.Errorf("read batch favorites: %w", err)
			}
			for _, row := range rows {
				if row.Favorite == favorite {
					continue
				}
				if err := tx.Table(table).Where("id = ? AND user_id = ?", row.ID, owner).Update("favorite", favorite).Error; err != nil {
					return fmt.Errorf("update batch favorites: %w", err)
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
