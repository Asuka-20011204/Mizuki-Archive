package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"mizuki-archive/internal/model"
)

// BatchUpdateTags 在已验证归属的同一事务内对两类资料增删标签，返回真正发生变化的条目。
func (store *MySQL) BatchUpdateTags(ctx context.Context, items []model.InboxSelection, tag, mode string) ([]model.InboxSelection, error) {
	owner, err := externalOwner(ctx)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 || len(items) > 50 || (mode != "add" && mode != "remove") {
		return nil, errors.New("invalid batch tag request")
	}
	changed := make([]model.InboxSelection, 0, len(items))
	err = store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		groups, err := lockOwnedBatch(tx, owner, items)
		if err != nil {
			return err
		}
		var fileTag tagRow
		if len(groups["file"]) > 0 {
			if mode == "add" {
				id, err := newTagID()
				if err != nil {
					return err
				}
				candidate := tagRow{ID: id, Name: tag, NormalizedName: tag, CreatedAt: time.Now().UTC()}
				if err := tx.Clauses(clause.Insert{Modifier: "IGNORE"}).Table("tags").Create(&candidate).Error; err != nil {
					return fmt.Errorf("prepare batch tag: %w", err)
				}
			}
			if err := tx.Table("tags").Where("normalized_name = ?", tag).Take(&fileTag).Error; err != nil && !(mode == "remove" && errors.Is(err, gorm.ErrRecordNotFound)) {
				return fmt.Errorf("read batch tag: %w", err)
			}
		}
		for _, id := range groups["file"] {
			if fileTag.ID == "" {
				continue
			}
			selection := model.InboxSelection{Source: "file", ID: id}
			updated, err := changeFileTag(tx, id, fileTag.ID, mode)
			if err != nil {
				return err
			}
			if updated {
				changed = append(changed, selection)
			}
		}
		for _, id := range groups["external"] {
			selection := model.InboxSelection{Source: "external", ID: id}
			updated, err := changeExternalTag(tx, id, tag, mode)
			if err != nil {
				return err
			}
			if updated {
				changed = append(changed, selection)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return changed, nil
}

// changeFileTag 在行锁保护下保留文件现有标签，最多允许 10 个且重复提交不新增。
func changeFileTag(tx *gorm.DB, id, tagID, mode string) (bool, error) {
	query := tx.Table("resource_tags").Where("resource_id = ? AND tag_id = ?", id, tagID)
	if mode == "remove" {
		result := query.Delete(nil)
		return result.RowsAffected > 0, result.Error
	}
	var existing int64
	if err := query.Count(&existing).Error; err != nil {
		return false, fmt.Errorf("check file tag: %w", err)
	}
	if existing > 0 {
		return false, nil
	}
	var count int64
	if err := tx.Table("resource_tags").Where("resource_id = ?", id).Count(&count).Error; err != nil {
		return false, fmt.Errorf("count file tags: %w", err)
	}
	if count >= 10 {
		return false, ErrBatchTagLimit
	}
	if err := tx.Table("resource_tags").Create(map[string]any{"resource_id": id, "tag_id": tagID, "created_at": time.Now().UTC()}).Error; err != nil {
		return false, fmt.Errorf("add file tag: %w", err)
	}
	return true, nil
}

// changeExternalTag 在行锁保护下校验卡片标签上限并保留已存在的标签。
func changeExternalTag(tx *gorm.DB, id, tag, mode string) (bool, error) {
	query := tx.Table("external_resource_tags").Where("resource_id = ? AND name = ?", id, tag)
	if mode == "remove" {
		result := query.Delete(nil)
		return result.RowsAffected > 0, result.Error
	}
	var existing int64
	if err := query.Count(&existing).Error; err != nil {
		return false, fmt.Errorf("check card tag: %w", err)
	}
	if existing > 0 {
		return false, nil
	}
	var count int64
	if err := tx.Table("external_resource_tags").Where("resource_id = ?", id).Count(&count).Error; err != nil {
		return false, fmt.Errorf("count card tags: %w", err)
	}
	if count >= 10 {
		return false, ErrBatchTagLimit
	}
	if err := tx.Table("external_resource_tags").Create(&externalTagRow{ResourceID: id, Name: tag}).Error; err != nil {
		return false, fmt.Errorf("add card tag: %w", err)
	}
	return true, nil
}
