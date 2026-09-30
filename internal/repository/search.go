package repository

import (
	"context"
	"fmt"

	"mizuki-archive/internal/model"
)

// SearchFiles 在同一账号未删除文件的名称、标签和成功提取的正文中检索，返回稳定分页。
func (store *MySQL) SearchFiles(ctx context.Context, term string, limit, offset int) ([]model.Resource, error) {
	owner, err := externalOwner(ctx)
	if err != nil {
		return nil, err
	}
	var rows []resourceRow
	database := store.db.WithContext(ctx).Table("resources").Where("resources.user_id = ? AND resources.deleted_at IS NULL", owner)
	database = database.Where(`(LOCATE(?, resources.name) > 0 OR LOCATE(?, resources.original_name) > 0 OR EXISTS (
		SELECT 1 FROM resource_tags rt JOIN tags t ON t.id = rt.tag_id
		WHERE rt.resource_id = resources.id AND LOCATE(?, t.name) > 0
	) OR EXISTS (
		SELECT 1 FROM derived_assets da WHERE da.resource_id = resources.id AND da.kind = ? AND LOCATE(?, da.content_text) > 0
	))`, term, term, term, model.DerivedAssetText, term)
	if err := database.Order("resources.created_at DESC, resources.id DESC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("search files: %w", err)
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

// SearchExternal 在同账号卡片的标题、位置、类型、版本、备注和标签中检索，不请求外部位置。
func (store *MySQL) SearchExternal(ctx context.Context, term string, limit, offset int) ([]model.ExternalResource, error) {
	owner, err := externalOwner(ctx)
	if err != nil {
		return nil, err
	}
	pattern := externalSearchPattern(term)
	var rows []externalResourceRow
	database := store.db.WithContext(ctx).Table("external_resources").Where("external_resources.user_id = ?", owner)
	database = database.Where(`(title LIKE ? ESCAPE '!' OR location LIKE ? ESCAPE '!' OR resource_type LIKE ? ESCAPE '!'
		OR version LIKE ? ESCAPE '!' OR note LIKE ? ESCAPE '!' OR EXISTS (
			SELECT 1 FROM external_resource_tags ert WHERE ert.resource_id = external_resources.id AND ert.name LIKE ? ESCAPE '!'
		))`, pattern, pattern, pattern, pattern, pattern, pattern)
	if err := database.Order("external_resources.created_at DESC, external_resources.id DESC").Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("search external cards: %w", err)
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
