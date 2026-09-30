package repository

import (
	"context"
	"database/sql"
	"fmt"

	"gorm.io/gorm"
	"mizuki-archive/internal/model"
)

const portableExportFileLimit = 5000
const portableExportCardLimit = 5000
const portableExportNoteLimit = 10000
const portableExportRelationLimit = 10000

// ExportSnapshot 以同一数据库只读快照读取当前账号可见的资料，不复制文件或派生产物。
func (store *MySQL) ExportSnapshot(ctx context.Context) (model.PortableArchive, error) {
	owner, err := externalOwner(ctx)
	if err != nil {
		return model.PortableArchive{}, err
	}
	var archive model.PortableArchive
	err = store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := exportFiles(tx, owner, &archive); err != nil {
			return err
		}
		if err := exportCards(ctx, tx, owner, &archive); err != nil {
			return err
		}
		if err := exportNotes(tx, owner, &archive); err != nil {
			return err
		}
		return exportRelations(tx, owner, &archive)
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return model.PortableArchive{}, fmt.Errorf("export private archive: %w", err)
	}
	return archive, nil
}

// exportFiles 仅取未删除文件的清单和标签；数据库存储键与用户 ID 不进入 JSON。
func exportFiles(tx *gorm.DB, owner string, archive *model.PortableArchive) error {
	var rows []resourceRow
	if err := tx.Table("resources").Where("user_id = ? AND deleted_at IS NULL", owner).
		Order("created_at, id").Limit(portableExportFileLimit + 1).Find(&rows).Error; err != nil {
		return fmt.Errorf("read export files: %w", err)
	}
	if len(rows) > portableExportFileLimit {
		return ErrPortableExportLimit
	}
	archive.Files = make([]model.Resource, 0, len(rows))
	positions := make(map[string]int, len(rows))
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		positions[row.ID] = len(archive.Files)
		archive.Files = append(archive.Files, resourceFromRow(row))
		ids = append(ids, row.ID)
	}
	if len(ids) == 0 {
		return nil
	}
	var tags []struct {
		ResourceID string `gorm:"column:resource_id"`
		Name       string `gorm:"column:name"`
	}
	if err := tx.Table("resource_tags AS rt").Select("rt.resource_id, t.name").
		Joins("JOIN tags AS t ON t.id = rt.tag_id").
		Joins("JOIN resources AS r ON r.id = rt.resource_id").
		Where("r.user_id = ? AND r.deleted_at IS NULL AND rt.resource_id IN ?", owner, ids).
		Order("t.name, rt.resource_id").Find(&tags).Error; err != nil {
		return fmt.Errorf("read export file tags: %w", err)
	}
	for _, tag := range tags {
		archive.Files[positions[tag.ResourceID]].Tags = append(archive.Files[positions[tag.ResourceID]].Tags, tag.Name)
	}
	return nil
}

// exportCards 包含归档卡片及其标签，但不访问或下载卡片指向的外部位置。
func exportCards(ctx context.Context, tx *gorm.DB, owner string, archive *model.PortableArchive) error {
	var rows []externalResourceRow
	if err := tx.Table("external_resources").Where("user_id = ?", owner).
		Order("created_at, id").Limit(portableExportCardLimit + 1).Find(&rows).Error; err != nil {
		return fmt.Errorf("read export cards: %w", err)
	}
	if len(rows) > portableExportCardLimit {
		return ErrPortableExportLimit
	}
	archive.ExternalResources = make([]model.ExternalResource, 0, len(rows))
	for _, row := range rows {
		archive.ExternalResources = append(archive.ExternalResources, externalFromRow(row))
	}
	return attachExternalTags(ctx, tx, archive.ExternalResources)
}

// exportNotes 只导出清单中仍可见文件的私人笔记，避免已删除原件的残留内容泄露。
func exportNotes(tx *gorm.DB, owner string, archive *model.PortableArchive) error {
	archive.Notes = []model.ResourceNote{}
	if len(archive.Files) == 0 {
		return nil
	}
	ids := make([]string, 0, len(archive.Files))
	for _, file := range archive.Files {
		ids = append(ids, file.ID)
	}
	var rows []resourceNoteRow
	if err := tx.Table("resource_notes").Where("user_id = ? AND resource_id IN ?", owner, ids).
		Order("resource_id, created_at, id").Limit(portableExportNoteLimit + 1).Find(&rows).Error; err != nil {
		return fmt.Errorf("read export notes: %w", err)
	}
	if len(rows) > portableExportNoteLimit {
		return ErrPortableExportLimit
	}
	for _, row := range rows {
		archive.Notes = append(archive.Notes, resourceNoteFromRow(row))
	}
	return nil
}

// exportRelations 只保留本次快照中两端都可见的关联，稳定 ID 供离线交叉引用。
func exportRelations(tx *gorm.DB, owner string, archive *model.PortableArchive) error {
	var rows []relationRow
	if err := tx.Table("resource_relations").Where("user_id = ?", owner).
		Order("created_at, id").Limit(portableExportRelationLimit + 1).Find(&rows).Error; err != nil {
		return fmt.Errorf("read export relations: %w", err)
	}
	if len(rows) > portableExportRelationLimit {
		return ErrPortableExportLimit
	}
	visible := make(map[model.InboxSelection]bool, len(archive.Files)+len(archive.ExternalResources))
	for _, file := range archive.Files {
		visible[model.InboxSelection{Source: "file", ID: file.ID}] = true
	}
	for _, card := range archive.ExternalResources {
		visible[model.InboxSelection{Source: "external", ID: card.ID}] = true
	}
	archive.Relations = []model.PortableRelation{}
	for _, row := range rows {
		left := model.InboxSelection{Source: row.LeftSource, ID: row.LeftID}
		right := model.InboxSelection{Source: row.RightSource, ID: row.RightID}
		if visible[left] && visible[right] {
			archive.Relations = append(archive.Relations, model.PortableRelation{ID: row.ID, Left: left, Right: right, CreatedAt: row.CreatedAt})
		}
	}
	return nil
}
