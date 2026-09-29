package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"mizuki-archive/internal/model"
)

// MySQL 是 GORM 实现；数据库表结构仍由显式 SQL 迁移管理。
type MySQL struct {
	db *gorm.DB
}

// NewMySQL 注入 GORM 连接；建表仍由显式迁移负责，不在构造时自动改表。
func NewMySQL(db *gorm.DB) *MySQL { return &MySQL{db: db} }

// resourceRow 只负责 GORM 字段映射，不把数据库标签渗入 model.Resource。
type resourceRow struct {
	ID           string    `gorm:"column:id;primaryKey"`
	Name         string    `gorm:"column:name"`
	OriginalName string    `gorm:"column:original_name"`
	Kind         string    `gorm:"column:kind"`
	MIME         string    `gorm:"column:mime"`
	Size         int64     `gorm:"column:size_bytes"`
	SHA256       string    `gorm:"column:sha256"`
	StorageKey   string    `gorm:"column:storage_key"`
	Favorite     bool      `gorm:"column:favorite"`
	CreatedAt    time.Time `gorm:"column:created_at"`
}

type sessionRow struct {
	TokenHash string    `gorm:"column:token_hash;primaryKey"`
	ExpiresAt time.Time `gorm:"column:expires_at"`
}

// resourceFromRow 将数据库行转换为业务模型，隔离 GORM 字段与对外 JSON 结构。
func resourceFromRow(row resourceRow) model.Resource {
	return model.Resource{ID: row.ID, Name: row.Name, OriginalName: row.OriginalName, Kind: row.Kind, MIME: row.MIME, Size: row.Size, SHA256: row.SHA256, StorageKey: row.StorageKey, Favorite: row.Favorite, CreatedAt: row.CreatedAt}
}

// SaveResource 登记已落盘文件的元数据；调用方负责数据库失败时的文件补偿清理。
func (store *MySQL) SaveResource(ctx context.Context, resource model.Resource) error {
	row := resourceRow{ID: resource.ID, Name: resource.Name, OriginalName: resource.OriginalName, Kind: resource.Kind, MIME: resource.MIME, Size: resource.Size, SHA256: resource.SHA256, StorageKey: resource.StorageKey, Favorite: resource.Favorite, CreatedAt: resource.CreatedAt}
	if err := store.db.WithContext(ctx).Table("resources").Create(&row).Error; err != nil {
		return fmt.Errorf("insert resource: %w", err)
	}
	return nil
}

// GetResource 按服务端 ID 查询一条资料，并将 GORM 的缺失结果统一映射为 ErrNotFound。
func (store *MySQL) GetResource(ctx context.Context, id string) (model.Resource, error) {
	// “不存在”统一转换为领域可识别的错误，其余数据库错误保持原始因果链。
	var row resourceRow
	err := store.db.WithContext(ctx).Table("resources").Where("id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.Resource{}, ErrNotFound
	}
	if err != nil {
		return model.Resource{}, fmt.Errorf("get resource: %w", err)
	}
	return resourceFromRow(row), nil
}

// ListResources 在 MySQL 中按名称、类型筛选并稳定分页；不在内存中扫描全部资料。
func (store *MySQL) ListResources(ctx context.Context, query model.ListQuery) ([]model.Resource, error) {
	// 筛选值始终作为参数绑定；稳定排序避免同一时间上传的资料翻页漂移。
	database := store.db.WithContext(ctx).Table("resources")
	if query.Search != "" {
		database = database.Where("LOCATE(?, name) > 0", query.Search)
	}
	if query.Kind != "" {
		database = database.Where("kind = ?", query.Kind)
	}
	var rows []resourceRow
	if err := database.Order("created_at DESC").Order("id DESC").Limit(query.Limit).Offset(query.Offset).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list resources: %w", err)
	}
	resources := make([]model.Resource, 0, len(rows))
	for _, row := range rows {
		resources = append(resources, resourceFromRow(row))
	}
	return resources, nil
}

// SaveSession 只保存令牌摘要和截止时间，原始会话令牌不进入数据库。
func (store *MySQL) SaveSession(ctx context.Context, hash string, expires time.Time) error {
	row := sessionRow{TokenHash: hash, ExpiresAt: expires}
	if err := store.db.WithContext(ctx).Table("sessions").Create(&row).Error; err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

// HasSession 同时检查摘要与过期时间，过期或缺失的会话都视为无效。
func (store *MySQL) HasSession(ctx context.Context, hash string) (bool, error) {
	// 会话表只保存令牌摘要，过期判断交给数据库条件，原始 Cookie 不落库。
	var count int64
	if err := store.db.WithContext(ctx).Table("sessions").Where("token_hash = ? AND expires_at > ?", hash, time.Now().UTC()).Count(&count).Error; err != nil {
		return false, fmt.Errorf("check session: %w", err)
	}
	return count > 0, nil
}

// DeleteSession 按摘要删除可撤销会话，使浏览器中的旧令牌失效。
func (store *MySQL) DeleteSession(ctx context.Context, hash string) error {
	if err := store.db.WithContext(ctx).Table("sessions").Where("token_hash = ?", hash).Delete(&sessionRow{}).Error; err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// Migrate 执行当前幂等初始建表；后续结构变更必须引入版本跟踪。
func (store *MySQL) Migrate(ctx context.Context) error {
	// 当前仅有幂等初始建表；后续变更需要独立版本跟踪，不能依赖自动改表。
	for _, statement := range strings.Split(initialMigration, ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		if err := store.db.WithContext(ctx).Exec(statement).Error; err != nil {
			return fmt.Errorf("apply initial schema: %w", err)
		}
	}
	return nil
}
