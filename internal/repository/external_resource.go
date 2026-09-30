package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"mizuki-archive/internal/model"
)

// externalResourceRow 只承担 GORM 字段映射；所有查询都必须显式限定 user_id。
type externalResourceRow struct {
	ID                 string     `gorm:"column:id;primaryKey"`
	UserID             string     `gorm:"column:user_id"`
	Title              string     `gorm:"column:title"`
	Location           string     `gorm:"column:location"`
	LocationKey        *string    `gorm:"column:location_key"`
	ResourceType       string     `gorm:"column:resource_type"`
	Version            string     `gorm:"column:version"`
	Note               string     `gorm:"column:note"`
	Status             string     `gorm:"column:status"`
	Favorite           bool       `gorm:"column:favorite"`
	ArchivedAt         *time.Time `gorm:"column:archived_at"`
	OrganizationStatus string     `gorm:"column:organization_status"`
	CreatedAt          time.Time  `gorm:"column:created_at"`
	UpdatedAt          time.Time  `gorm:"column:updated_at"`
}

type externalTagRow struct {
	ResourceID string `gorm:"column:resource_id"`
	Name       string `gorm:"column:name"`
}

// externalOwner 从已验证会话读取归属；即使仓储被直接调用也不能退化成全库访问。
func externalOwner(ctx context.Context) (string, error) {
	userID, ok := UserIDFromContext(ctx)
	if !ok {
		return "", fmt.Errorf("external resource: missing user identity")
	}
	return userID, nil
}

// externalFromRow 把数据库行转换为 API 模型，确保空标签始终序列化为数组。
func externalFromRow(row externalResourceRow) model.ExternalResource {
	return model.ExternalResource{ID: row.ID, Title: row.Title, Location: row.Location, ResourceType: row.ResourceType, Version: row.Version, Note: row.Note, Status: row.Status, Favorite: row.Favorite, Archived: row.ArchivedAt != nil, OrganizationStatus: row.OrganizationStatus, Tags: []string{}, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

// writeExternalTags 在同一事务写入已校验的标签，任何一步失败都回滚卡片变动。
func writeExternalTags(tx *gorm.DB, id string, tags []string) error {
	for _, tag := range tags {
		if err := tx.Table("external_resource_tags").Create(&externalTagRow{ResourceID: id, Name: tag}).Error; err != nil {
			return err
		}
	}
	return nil
}

// attachExternalTags 一次性查询列表标签，不对每张卡片重复查询数据库。
func attachExternalTags(ctx context.Context, db *gorm.DB, resources []model.ExternalResource) error {
	if len(resources) == 0 {
		return nil
	}
	ids := make([]string, 0, len(resources))
	positions := make(map[string]int, len(resources))
	for index, resource := range resources {
		ids = append(ids, resource.ID)
		positions[resource.ID] = index
	}
	var rows []externalTagRow
	if err := db.WithContext(ctx).Table("external_resource_tags").Where("resource_id IN ?", ids).Order("name").Find(&rows).Error; err != nil {
		return fmt.Errorf("list external tags: %w", err)
	}
	for _, row := range rows {
		resources[positions[row.ResourceID]].Tags = append(resources[positions[row.ResourceID]].Tags, row.Name)
	}
	return nil
}

// CreateExternalResource 原子创建当前用户的卡片及标签，不允许客户端声明归属。
func (store *MySQL) CreateExternalResource(ctx context.Context, resource model.ExternalResource) error {
	owner, err := externalOwner(ctx)
	if err != nil {
		return err
	}
	key := model.ExternalLinkKey(resource.Location)
	row := externalResourceRow{ID: resource.ID, UserID: owner, Title: resource.Title, Location: resource.Location, LocationKey: &key, ResourceType: resource.ResourceType, Version: resource.Version, Note: resource.Note, Status: resource.Status, Favorite: resource.Favorite, OrganizationStatus: "pending", CreatedAt: resource.CreatedAt, UpdatedAt: resource.UpdatedAt}
	return store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("external_resources").Create(&row).Error; err != nil {
			return fmt.Errorf("create external resource: %w", err)
		}
		return writeExternalTags(tx, resource.ID, resource.Tags)
	})
}

// GetExternalResource 以 ID 和当前用户双条件读取；跨用户卡片与不存在返回相同结果。
func (store *MySQL) GetExternalResource(ctx context.Context, id string) (model.ExternalResource, error) {
	owner, err := externalOwner(ctx)
	if err != nil {
		return model.ExternalResource{}, err
	}
	var row externalResourceRow
	if err := store.db.WithContext(ctx).Table("external_resources").Where("id = ? AND user_id = ?", id, owner).Take(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return model.ExternalResource{}, ErrNotFound
		}
		return model.ExternalResource{}, fmt.Errorf("get external resource: %w", err)
	}
	resource := externalFromRow(row)
	items := []model.ExternalResource{resource}
	if err := attachExternalTags(ctx, store.db, items); err != nil {
		return model.ExternalResource{}, err
	}
	return items[0], nil
}

// externalSearchPattern 转义 LIKE 通配符，使用户输入的百分号和下划线按字面匹配。
func externalSearchPattern(query string) string {
	replacer := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_")
	return "%" + replacer.Replace(query) + "%"
}

// ListExternalResources 限定用户并控制返回数量；查询位置只是数据库文本匹配，绝不发起网络请求。
func (store *MySQL) ListExternalResources(ctx context.Context, query string) ([]model.ExternalResource, error) {
	owner, err := externalOwner(ctx)
	if err != nil {
		return nil, err
	}
	db := store.db.WithContext(ctx).Table("external_resources").Where("user_id = ? AND archived_at IS NULL", owner)
	if query != "" {
		pattern := externalSearchPattern(query)
		db = db.Where("(title LIKE ? ESCAPE '!' OR location LIKE ? ESCAPE '!')", pattern, pattern)
	}
	var rows []externalResourceRow
	if err := db.Order("created_at DESC, id DESC").Limit(100).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list external resources: %w", err)
	}
	resources := make([]model.ExternalResource, 0, len(rows))
	for _, row := range rows {
		resources = append(resources, externalFromRow(row))
	}
	if err := attachExternalTags(ctx, store.db, resources); err != nil {
		return nil, err
	}
	return resources, nil
}

// UpdateExternalResource 在同一事务更新并读回卡片与标签；读回失败会回滚，不误报已提交更新。
func (store *MySQL) UpdateExternalResource(ctx context.Context, resource model.ExternalResource) (model.ExternalResource, error) {
	owner, err := externalOwner(ctx)
	if err != nil {
		return model.ExternalResource{}, err
	}
	var updated model.ExternalResource
	err = store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Table("external_resources").Where("id = ? AND user_id = ?", resource.ID, owner).Updates(map[string]any{
			// 收藏由独立接口维护，普通编辑不写入 favorite，防止旧表单覆盖并发收藏。
			"title": resource.Title, "location": resource.Location, "location_key": model.ExternalLinkKey(resource.Location), "resource_type": resource.ResourceType,
			"version": resource.Version, "note": resource.Note, "status": resource.Status, "updated_at": resource.UpdatedAt,
		})
		if result.Error != nil {
			return fmt.Errorf("update external resource: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		if err := tx.Table("external_resource_tags").Where("resource_id = ?", resource.ID).Delete(&externalTagRow{}).Error; err != nil {
			return fmt.Errorf("remove external tags: %w", err)
		}
		if err := writeExternalTags(tx, resource.ID, resource.Tags); err != nil {
			return err
		}
		var row externalResourceRow
		if err := tx.Table("external_resources").Where("id = ? AND user_id = ?", resource.ID, owner).Take(&row).Error; err != nil {
			return fmt.Errorf("read updated external resource: %w", err)
		}
		items := []model.ExternalResource{externalFromRow(row)}
		if err := attachExternalTags(ctx, tx, items); err != nil {
			return err
		}
		updated = items[0]
		return nil
	})
	if err != nil {
		return model.ExternalResource{}, err
	}
	return updated, nil
}

// DeleteExternalResource 只删除当前用户的卡片；外部地址代表的真实文件永不被访问。
func (store *MySQL) DeleteExternalResource(ctx context.Context, id string) error {
	owner, err := externalOwner(ctx)
	if err != nil {
		return err
	}
	result := store.db.WithContext(ctx).Table("external_resources").Where("id = ? AND user_id = ?", id, owner).Delete(&externalResourceRow{})
	if result.Error != nil {
		return fmt.Errorf("delete external resource: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
