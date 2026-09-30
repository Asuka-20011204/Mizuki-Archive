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

var ErrResourceNoteLimit = errors.New("resource note limit reached")

// resourceNoteRow 隔离 GORM 表字段与对外模型，账号 ID 从会话取得而非 HTTP 正文。
type resourceNoteRow struct {
	ID         string    `gorm:"column:id;primaryKey"`
	UserID     string    `gorm:"column:user_id"`
	ResourceID string    `gorm:"column:resource_id"`
	PageNumber *int      `gorm:"column:page_number"`
	Excerpt    string    `gorm:"column:excerpt"`
	Content    string    `gorm:"column:content"`
	Source     string    `gorm:"column:source"`
	CreatedAt  time.Time `gorm:"column:created_at"`
	UpdatedAt  time.Time `gorm:"column:updated_at"`
}

// resourceNoteFromRow 只返回当前用户可见的注记字段，不暴露数据库账号列。
func resourceNoteFromRow(row resourceNoteRow) model.ResourceNote {
	return model.ResourceNote{ID: row.ID, ResourceID: row.ResourceID, PageNumber: row.PageNumber, Excerpt: row.Excerpt, Content: row.Content, Source: row.Source, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

// CreateResourceNote 锁定文件行后检查数量与归属，避免并发创建越过每文件 100 条上限。
func (store *MySQL) CreateResourceNote(ctx context.Context, note model.ResourceNote) error {
	owner, err := externalOwner(ctx)
	if err != nil {
		return err
	}
	return store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var resource resourceRow
		if err := tx.Table("resources").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ? AND deleted_at IS NULL", note.ResourceID, owner).Take(&resource).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return fmt.Errorf("lock note resource: %w", err)
		}
		var count int64
		if err := tx.Table("resource_notes").Where("user_id = ? AND resource_id = ?", owner, note.ResourceID).Count(&count).Error; err != nil {
			return fmt.Errorf("count resource notes: %w", err)
		}
		if count >= 100 {
			return ErrResourceNoteLimit
		}
		row := resourceNoteRow{ID: note.ID, UserID: owner, ResourceID: note.ResourceID, PageNumber: note.PageNumber, Excerpt: note.Excerpt, Content: note.Content, Source: note.Source, CreatedAt: note.CreatedAt, UpdatedAt: note.UpdatedAt}
		if err := tx.Table("resource_notes").Create(&row).Error; err != nil {
			return fmt.Errorf("create resource note: %w", err)
		}
		return nil
	})
}

// ListResourceNotes 从属于当前账号且未删除的文件读取最多 100 条注记，按创建时间倒序。
func (store *MySQL) ListResourceNotes(ctx context.Context, resourceID string) ([]model.ResourceNote, error) {
	owner, err := externalOwner(ctx)
	if err != nil {
		return nil, err
	}
	var rows []resourceNoteRow
	query := store.db.WithContext(ctx).Table("resource_notes AS n").Select("n.*").
		Joins("JOIN resources AS r ON r.id = n.resource_id AND r.user_id = n.user_id").
		Where("n.user_id = ? AND n.resource_id = ? AND r.deleted_at IS NULL", owner, resourceID).
		Order("n.created_at DESC, n.id DESC").Limit(100)
	if err := query.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list resource notes: %w", err)
	}
	result := make([]model.ResourceNote, 0, len(rows))
	for _, row := range rows {
		result = append(result, resourceNoteFromRow(row))
	}
	return result, nil
}

// UpdateResourceNote 双重校验注记和文件归属，软删除后的文件也不能再改注记。
func (store *MySQL) UpdateResourceNote(ctx context.Context, note model.ResourceNote) error {
	owner, err := externalOwner(ctx)
	if err != nil {
		return err
	}
	result := store.db.WithContext(ctx).Table("resource_notes").Where(`user_id = ? AND resource_id = ? AND id = ? AND EXISTS (
		SELECT 1 FROM resources r WHERE r.id = resource_notes.resource_id AND r.user_id = ? AND r.deleted_at IS NULL
	)`, owner, note.ResourceID, note.ID, owner).Updates(map[string]any{
		"page_number": note.PageNumber, "excerpt": note.Excerpt, "content": note.Content, "source": note.Source, "updated_at": note.UpdatedAt,
	})
	if result.Error != nil {
		return fmt.Errorf("update resource note: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteResourceNote 只删除当前账号指定文件中的注记，绝不通过注记 ID 单独跨文件删除。
func (store *MySQL) DeleteResourceNote(ctx context.Context, resourceID, noteID string) error {
	owner, err := externalOwner(ctx)
	if err != nil {
		return err
	}
	result := store.db.WithContext(ctx).Table("resource_notes").Where(`user_id = ? AND resource_id = ? AND id = ? AND EXISTS (
		SELECT 1 FROM resources r WHERE r.id = resource_notes.resource_id AND r.user_id = ? AND r.deleted_at IS NULL
	)`, owner, resourceID, noteID, owner).Delete(&resourceNoteRow{})
	if result.Error != nil {
		return fmt.Errorf("delete resource note: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
