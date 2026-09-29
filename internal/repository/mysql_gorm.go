package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"mizuki-archive/internal/model"
)

// MySQL 是 GORM 实现；数据库表结构仍由显式 SQL 迁移管理。
type MySQL struct {
	db            *gorm.DB
	outboxEnabled bool
}

// NewMySQL 注入 GORM 连接；建表仍由显式迁移负责，不在构造时自动改表。
func NewMySQL(db *gorm.DB) *MySQL { return &MySQL{db: db} }

// EnableOutbox 在 Rabbit Worker 启动前启用重试任务的事务事件写入；数据库模式只更新任务表。
func (store *MySQL) EnableOutbox() { store.outboxEnabled = true }

// resourceRow 只负责 GORM 字段映射，不把数据库标签渗入 model.Resource。
type resourceRow struct {
	ID           string     `gorm:"column:id;primaryKey"`
	Name         string     `gorm:"column:name"`
	OriginalName string     `gorm:"column:original_name"`
	Kind         string     `gorm:"column:kind"`
	MIME         string     `gorm:"column:mime"`
	Size         int64      `gorm:"column:size_bytes"`
	SHA256       string     `gorm:"column:sha256"`
	StorageKey   string     `gorm:"column:storage_key"`
	Favorite     bool       `gorm:"column:favorite"`
	DeletedAt    *time.Time `gorm:"column:deleted_at"`
	CreatedAt    time.Time  `gorm:"column:created_at"`
}

type sessionRow struct {
	TokenHash string    `gorm:"column:token_hash;primaryKey"`
	ExpiresAt time.Time `gorm:"column:expires_at"`
}

// tagRow 隔离标签表的数据库字段，避免把规范化键暴露给 API。
type tagRow struct {
	ID             string    `gorm:"column:id;primaryKey"`
	Name           string    `gorm:"column:name"`
	NormalizedName string    `gorm:"column:normalized_name"`
	CreatedAt      time.Time `gorm:"column:created_at"`
}

// resourceFromRow 将数据库行转换为业务模型，隔离 GORM 字段与对外 JSON 结构。
func resourceFromRow(row resourceRow) model.Resource {
	return model.Resource{ID: row.ID, Name: row.Name, OriginalName: row.OriginalName, Kind: row.Kind, MIME: row.MIME, Size: row.Size, SHA256: row.SHA256, StorageKey: row.StorageKey, Favorite: row.Favorite, Tags: []string{}, CreatedAt: row.CreatedAt}
}

// newTagID 生成不含业务含义的标签 ID，避免把标签名直接当成主键或路径的一部分。
func newTagID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate tag ID: %w", err)
	}
	return hex.EncodeToString(bytes), nil
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
	err := store.db.WithContext(ctx).Table("resources").Where("id = ? AND deleted_at IS NULL", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.Resource{}, ErrNotFound
	}
	if err != nil {
		return model.Resource{}, fmt.Errorf("get resource: %w", err)
	}
	resource := resourceFromRow(row)
	tags, err := store.loadTags(ctx, []string{id})
	if err != nil {
		return model.Resource{}, err
	}
	if loadedTags, ok := tags[id]; ok {
		resource.Tags = loadedTags
	}
	return resource, nil
}

// SetFavorite 显式写入目标状态并读取最终元数据；重复设置同值也应成功，不依赖受影响行数判断存在性。
func (store *MySQL) SetFavorite(ctx context.Context, id string, favorite bool) (model.Resource, error) {
	if err := store.db.WithContext(ctx).Table("resources").Where("id = ? AND deleted_at IS NULL", id).Update("favorite", favorite).Error; err != nil {
		return model.Resource{}, fmt.Errorf("set resource favorite: %w", err)
	}
	// 同值更新在 MySQL 中可能报告零受影响行；读取既区分资料不存在，也返回最新元数据。
	// 写入成功但读取失败时客户端可安全重试同一布尔目标值，不会发生二次反转。
	return store.GetResource(ctx, id)
}

// UpdateResourceName 修改展示名但保留原始文件名，便于用户整理资料而不改变磁盘文件和下载来源。
func (store *MySQL) UpdateResourceName(ctx context.Context, id, name string) (model.Resource, error) {
	if err := store.db.WithContext(ctx).Table("resources").Where("id = ? AND deleted_at IS NULL", id).Update("name", name).Error; err != nil {
		return model.Resource{}, fmt.Errorf("update resource name: %w", err)
	}
	return store.GetResource(ctx, id)
}

// DeleteResource 标记资料删除并解除标签关联；重复调用会返回已隐藏资料，让 Service 可以重试清理残留原件。
func (store *MySQL) DeleteResource(ctx context.Context, id string) (model.Resource, error) {
	var resource resourceRow
	err := store.db.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Table("resources").Where("id = ?", id).Take(&resource).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return fmt.Errorf("find resource to delete: %w", err)
		}
		if resource.DeletedAt == nil {
			deletedAt := time.Now().UTC()
			if err := transaction.Table("resources").Where("id = ? AND deleted_at IS NULL", id).Update("deleted_at", deletedAt).Error; err != nil {
				return fmt.Errorf("mark resource deleted: %w", err)
			}
		}
		if err := transaction.Exec("DELETE FROM resource_tags WHERE resource_id = ?", id).Error; err != nil {
			return fmt.Errorf("clear deleted resource tags: %w", err)
		}
		return nil
	})
	if err != nil {
		return model.Resource{}, err
	}
	return resourceFromRow(resource), nil
}

// ReplaceResourceTags 在单个数据库事务中替换资料的全部标签，并返回提交后的资料元数据。
func (store *MySQL) ReplaceResourceTags(ctx context.Context, id string, names []string) (model.Resource, error) {
	var resource resourceRow
	transactionErr := store.db.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
		if err := transaction.Table("resources").Where("id = ? AND deleted_at IS NULL", id).Take(&resource).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return fmt.Errorf("check resource for tags: %w", err)
		}
		rows := make([]tagRow, 0, len(names))
		for _, name := range names {
			id, err := newTagID()
			if err != nil {
				return err
			}
			candidate := tagRow{ID: id, Name: name, NormalizedName: name, CreatedAt: time.Now().UTC()}
			// INSERT IGNORE 只吞掉规范化名称的唯一键竞争；随后重新读取，确保关联到最终存在的标签。
			if err := transaction.Clauses(clause.Insert{Modifier: "IGNORE"}).Table("tags").Create(&candidate).Error; err != nil {
				return fmt.Errorf("create tag: %w", err)
			}
			var tag tagRow
			if err := transaction.Table("tags").Where("normalized_name = ?", name).Take(&tag).Error; err != nil {
				return fmt.Errorf("read created tag: %w", err)
			}
			rows = append(rows, tag)
		}
		if err := transaction.Table("resource_tags").Where("resource_id = ?", id).Delete(nil).Error; err != nil {
			return fmt.Errorf("clear resource tags: %w", err)
		}
		for _, tag := range rows {
			if err := transaction.Table("resource_tags").Create(map[string]any{"resource_id": id, "tag_id": tag.ID, "created_at": time.Now().UTC()}).Error; err != nil {
				return fmt.Errorf("link resource tag: %w", err)
			}
		}
		return nil
	})
	if transactionErr != nil {
		return model.Resource{}, transactionErr
	}
	return store.GetResource(ctx, id)
}

// ListTags 返回可供前端建议的已有标签，并按规范化名称稳定排序。
func (store *MySQL) ListTags(ctx context.Context, search string) ([]string, error) {
	var rows []tagRow
	database := store.db.WithContext(ctx).Table("tags")
	database = database.Where("EXISTS (SELECT 1 FROM resource_tags rt JOIN resources r ON r.id = rt.resource_id WHERE rt.tag_id = tags.id AND r.deleted_at IS NULL)")
	if search != "" {
		database = database.Where("normalized_name LIKE CONCAT('%', ?, '%')", search)
	}
	if err := database.Order("normalized_name ASC").Limit(50).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	tags := make([]string, 0, len(rows))
	for _, row := range rows {
		tags = append(tags, row.Name)
	}
	return tags, nil
}

// ListResources 在 MySQL 中按名称、类型筛选并稳定分页；不在内存中扫描全部资料。
func (store *MySQL) ListResources(ctx context.Context, query model.ListQuery) ([]model.Resource, error) {
	// 筛选值始终作为参数绑定；稳定排序避免同一时间上传的资料翻页漂移。
	database := store.db.WithContext(ctx).Table("resources")
	if query.Search != "" {
		database = database.Where("LOCATE(?, name) > 0 OR LOCATE(?, original_name) > 0 OR EXISTS (SELECT 1 FROM derived_assets da WHERE da.resource_id = resources.id AND da.kind = ? AND LOCATE(?, da.content_text) > 0)", query.Search, query.Search, model.DerivedAssetText, query.Search)
	}
	if query.Kind != "" {
		database = database.Where("kind = ?", query.Kind)
	}
	if query.Tag != "" {
		database = database.Where("EXISTS (SELECT 1 FROM resource_tags rt JOIN tags t ON t.id = rt.tag_id WHERE rt.resource_id = resources.id AND t.normalized_name = ?)", query.Tag)
	}
	database = database.Where("resources.deleted_at IS NULL")
	var rows []resourceRow
	if err := database.Order("created_at DESC").Order("id DESC").Limit(query.Limit).Offset(query.Offset).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list resources: %w", err)
	}
	resources := make([]model.Resource, 0, len(rows))
	resourceTags, err := store.loadTags(ctx, resourceIDs(rows))
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		resource := resourceFromRow(row)
		if loadedTags, ok := resourceTags[resource.ID]; ok {
			resource.Tags = loadedTags
		}
		resources = append(resources, resource)
	}
	return resources, nil
}

// resourceIDs 提取分页结果中的资源 ID，供一次性批量读取标签，避免列表出现逐条查询。
func resourceIDs(rows []resourceRow) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

// loadTags 批量加载资料标签，并按规范化名称排序以保证 API 返回稳定。
func (store *MySQL) loadTags(ctx context.Context, ids []string) (map[string][]string, error) {
	tags := make(map[string][]string, len(ids))
	if len(ids) == 0 {
		return tags, nil
	}
	var rows []struct {
		ResourceID string `gorm:"column:resource_id"`
		Name       string `gorm:"column:name"`
	}
	err := store.db.WithContext(ctx).Table("resource_tags AS rt").Select("rt.resource_id, t.name").Joins("JOIN tags AS t ON t.id = rt.tag_id").Where("rt.resource_id IN ?", ids).Order("t.normalized_name ASC").Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("load resource tags: %w", err)
	}
	for _, row := range rows {
		tags[row.ResourceID] = append(tags[row.ResourceID], row.Name)
	}
	return tags, nil
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

// Migrate 串行执行可重入迁移；MySQL DDL 可能隐式提交，因此不依赖跨 DDL 的事务回滚。
func (store *MySQL) Migrate(ctx context.Context) error {
	if err := store.db.WithContext(ctx).Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
  version INT UNSIGNED PRIMARY KEY,
  name VARCHAR(128) NOT NULL,
  applied_at DATETIME(6) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`).Error; err != nil {
		return fmt.Errorf("create schema migrations table: %w", err)
	}
	migrations := []struct {
		version uint
		name    string
		sql     string
	}{
		{version: 1, name: "initial_schema", sql: initialMigration},
		{version: 2, name: "manual_tags", sql: manualTagsMigration},
		{version: 3, name: "soft_delete", sql: softDeleteMigration},
		{version: 4, name: "processing_jobs", sql: processingJobsMigration},
		{version: 5, name: "processing_outbox", sql: processingOutboxMigration},
	}
	return store.db.WithContext(ctx).Connection(func(connection *gorm.DB) error {
		var locked int
		if err := connection.Raw("SELECT GET_LOCK(?, 15)", "mizuki_archive_schema_migration").Scan(&locked).Error; err != nil {
			return fmt.Errorf("acquire migration lock: %w", err)
		}
		if locked != 1 {
			return errors.New("acquire migration lock: timeout")
		}
		defer connection.Exec("SELECT RELEASE_LOCK(?)", "mizuki_archive_schema_migration")
		for _, migration := range migrations {
			var applied int64
			if err := connection.Table("schema_migrations").Where("version = ?", migration.version).Count(&applied).Error; err != nil {
				return fmt.Errorf("check migration %d: %w", migration.version, err)
			}
			if applied > 0 {
				continue
			}
			for _, statement := range strings.Split(migration.sql, ";") {
				statement = strings.TrimSpace(statement)
				if statement == "" {
					continue
				}
				if err := connection.Exec(statement).Error; err != nil {
					return fmt.Errorf("execute migration %d: %w", migration.version, err)
				}
			}
			if err := connection.Exec(
				"INSERT IGNORE INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)",
				migration.version,
				migration.name,
				time.Now().UTC(),
			).Error; err != nil {
				return fmt.Errorf("record migration %d: %w", migration.version, err)
			}
		}
		return nil
	})
}
