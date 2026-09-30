package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"mizuki-archive/internal/model"
)

// savedSearchRow 把组合筛选条件映射到归属明确的持久化表。
type savedSearchRow struct {
	ID                 string    `gorm:"column:id;primaryKey"`
	UserID             string    `gorm:"column:user_id"`
	Name               string    `gorm:"column:name"`
	QueryText          string    `gorm:"column:query_text"`
	Source             string    `gorm:"column:source"`
	Kind               string    `gorm:"column:kind"`
	Tag                string    `gorm:"column:tag"`
	OrganizationStatus string    `gorm:"column:organization_status"`
	CreatedAt          time.Time `gorm:"column:created_at"`
}

// savedSearchFromRow 返回当前账号的纯筛选条件，不加载或缓存任何匹配资料。
func savedSearchFromRow(row savedSearchRow) model.SavedSearch {
	return model.SavedSearch{ID: row.ID, Name: row.Name, CreatedAt: row.CreatedAt, Filter: model.SearchFilter{
		Query: row.QueryText, Source: row.Source, Kind: row.Kind, Tag: row.Tag, OrganizationStatus: row.OrganizationStatus,
	}}
}

// CreateSavedSearch 锁住账号行限制视图数；唯一约束防止同账号重复名称。
func (store *MySQL) CreateSavedSearch(ctx context.Context, view model.SavedSearch) error {
	owner, err := externalOwner(ctx)
	if err != nil {
		return err
	}
	err = store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user struct{ ID string }
		if err := tx.Table("users").Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("id = ?", owner).Take(&user).Error; err != nil {
			return fmt.Errorf("lock saved search owner: %w", err)
		}
		var count int64
		if err := tx.Table("saved_search_views").Where("user_id = ?", owner).Count(&count).Error; err != nil {
			return fmt.Errorf("count saved searches: %w", err)
		}
		if count >= 30 {
			return ErrSavedSearchLimit
		}
		row := savedSearchRow{ID: view.ID, UserID: owner, Name: view.Name, QueryText: view.Filter.Query, Source: view.Filter.Source,
			Kind: view.Filter.Kind, Tag: view.Filter.Tag, OrganizationStatus: view.Filter.OrganizationStatus, CreatedAt: view.CreatedAt}
		if err := tx.Table("saved_search_views").Create(&row).Error; err != nil {
			var mysqlError *driver.MySQLError
			if errors.As(err, &mysqlError) && mysqlError.Number == 1062 {
				return ErrSavedSearchConflict
			}
			return fmt.Errorf("create saved search: %w", err)
		}
		return nil
	})
	return err
}

// ListSavedSearches 最多列出当前账号的 30 个视图，按创建时间倒序。
func (store *MySQL) ListSavedSearches(ctx context.Context) ([]model.SavedSearch, error) {
	owner, err := externalOwner(ctx)
	if err != nil {
		return nil, err
	}
	var rows []savedSearchRow
	if err := store.db.WithContext(ctx).Table("saved_search_views").Where("user_id = ?", owner).
		Order("created_at DESC, id DESC").Limit(30).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list saved searches: %w", err)
	}
	views := make([]model.SavedSearch, 0, len(rows))
	for _, row := range rows {
		views = append(views, savedSearchFromRow(row))
	}
	return views, nil
}

// DeleteSavedSearch 用 ID 和用户归属双条件删除视图；不删除关联资料。
func (store *MySQL) DeleteSavedSearch(ctx context.Context, id string) error {
	owner, err := externalOwner(ctx)
	if err != nil {
		return err
	}
	result := store.db.WithContext(ctx).Table("saved_search_views").Where("id = ? AND user_id = ?", id, owner).Delete(&savedSearchRow{})
	if result.Error != nil {
		return fmt.Errorf("delete saved search: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}
