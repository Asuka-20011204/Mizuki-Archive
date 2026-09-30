package repository

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"mizuki-archive/internal/model"
)

// relationRow 以规范化的无方向两端存储关系；同一对资料只允许建立一次。
type relationRow struct {
	ID          string    `gorm:"column:id;primaryKey"`
	UserID      string    `gorm:"column:user_id"`
	LeftSource  string    `gorm:"column:left_source"`
	LeftID      string    `gorm:"column:left_id"`
	RightSource string    `gorm:"column:right_source"`
	RightID     string    `gorm:"column:right_id"`
	CreatedAt   time.Time `gorm:"column:created_at"`
}

// orderedRelationPair 使 A→B 与 B→A 落在相同唯一键上，避免双向重复。
func orderedRelationPair(first, second model.InboxSelection) (model.InboxSelection, model.InboxSelection) {
	if first.Source > second.Source || (first.Source == second.Source && first.ID > second.ID) {
		return second, first
	}
	return first, second
}

// lockRelationEndpoint 对归属主行加锁；删除入口使用相同主行锁，杜绝删除后建立悬空关系。
func lockRelationEndpoint(tx *gorm.DB, owner string, entry model.InboxSelection) (string, error) {
	var row struct {
		Name string `gorm:"column:name"`
	}
	query := tx.Clauses(clause.Locking{Strength: "UPDATE"})
	if entry.Source == "file" {
		query = query.Table("resources").Select("name").Where("id = ? AND user_id = ? AND deleted_at IS NULL", entry.ID, owner)
	} else if entry.Source == "external" {
		query = query.Table("external_resources").Select("title AS name").Where("id = ? AND user_id = ?", entry.ID, owner)
	} else {
		return "", ErrNotFound
	}
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("lock relation endpoint: %w", err)
	}
	return row.Name, nil
}

// relationCount 查询单条资料已有关联数量，使用两端索引并限制每项最多 100 条。
func relationCount(tx *gorm.DB, owner string, entry model.InboxSelection) (int64, error) {
	var count int64
	err := tx.Table("resource_relations").Where("user_id = ? AND ((left_source = ? AND left_id = ?) OR (right_source = ? AND right_id = ?))", owner, entry.Source, entry.ID, entry.Source, entry.ID).Count(&count).Error
	return count, err
}

// CreateRelation 依稳定顺序锁住两个端点，在同一事务中验证归属、去重并执行容量限制。
func (store *MySQL) CreateRelation(ctx context.Context, source, target model.InboxSelection, id string) (model.ResourceRelation, error) {
	owner, err := externalOwner(ctx)
	if err != nil {
		return model.ResourceRelation{}, err
	}
	left, right := orderedRelationPair(source, target)
	row := relationRow{ID: id, UserID: owner, LeftSource: left.Source, LeftID: left.ID, RightSource: right.Source, RightID: right.ID, CreatedAt: time.Now().UTC()}
	var targetName string
	err = store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		entries := []model.InboxSelection{source, target}
		// 批量删除总是先锁文件再锁卡片；这里采用同样顺序减少交叉操作的死锁风险。
		sort.Slice(entries, func(first, second int) bool {
			if entries[first].Source != entries[second].Source {
				return entries[first].Source == "file"
			}
			return entries[first].ID < entries[second].ID
		})
		for _, entry := range entries {
			name, err := lockRelationEndpoint(tx, owner, entry)
			if err != nil {
				return err
			}
			if entry == target {
				targetName = name
			}
		}
		var existing int64
		if err := tx.Table("resource_relations").Where("user_id = ? AND left_source = ? AND left_id = ? AND right_source = ? AND right_id = ?", owner, left.Source, left.ID, right.Source, right.ID).Count(&existing).Error; err != nil {
			return fmt.Errorf("check relation duplicate: %w", err)
		}
		if existing > 0 {
			return ErrRelationExists
		}
		for _, entry := range entries {
			count, err := relationCount(tx, owner, entry)
			if err != nil {
				return fmt.Errorf("count resource relations: %w", err)
			}
			if count >= 100 {
				return ErrRelationLimit
			}
		}
		if err := tx.Table("resource_relations").Create(&row).Error; err != nil {
			return fmt.Errorf("create relation: %w", err)
		}
		return nil
	})
	if err != nil {
		return model.ResourceRelation{}, err
	}
	return model.ResourceRelation{ID: id, Target: target, Name: targetName, CreatedAt: row.CreatedAt}, nil
}

// ListRelations 对来源本身再次核验归属，再批量装入另一端的最新名称而不逐行查询。
func (store *MySQL) ListRelations(ctx context.Context, source model.InboxSelection) ([]model.ResourceRelation, error) {
	owner, err := externalOwner(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := relationEndpointName(store.db.WithContext(ctx), owner, source); err != nil {
		return nil, err
	}
	var rows []relationRow
	err = store.db.WithContext(ctx).Table("resource_relations").Where("user_id = ? AND ((left_source = ? AND left_id = ?) OR (right_source = ? AND right_id = ?))", owner, source.Source, source.ID, source.Source, source.ID).Order("created_at DESC, id DESC").Limit(100).Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list relations: %w", err)
	}
	peers := make([]model.InboxSelection, len(rows))
	fileIDs, cardIDs := []string{}, []string{}
	for index, row := range rows {
		peer := model.InboxSelection{Source: row.LeftSource, ID: row.LeftID}
		if peer == source {
			peer = model.InboxSelection{Source: row.RightSource, ID: row.RightID}
		}
		peers[index] = peer
		if peer.Source == "file" {
			fileIDs = append(fileIDs, peer.ID)
		} else {
			cardIDs = append(cardIDs, peer.ID)
		}
	}
	names := make(map[model.InboxSelection]string, len(rows))
	if len(fileIDs) > 0 {
		var files []resourceRow
		if err := store.db.WithContext(ctx).Table("resources").Select("id, name").Where("id IN ? AND user_id = ? AND deleted_at IS NULL", fileIDs, owner).Find(&files).Error; err != nil {
			return nil, fmt.Errorf("load related files: %w", err)
		}
		for _, file := range files {
			names[model.InboxSelection{Source: "file", ID: file.ID}] = file.Name
		}
	}
	if len(cardIDs) > 0 {
		var cards []externalResourceRow
		if err := store.db.WithContext(ctx).Table("external_resources").Select("id, title").Where("id IN ? AND user_id = ?", cardIDs, owner).Find(&cards).Error; err != nil {
			return nil, fmt.Errorf("load related cards: %w", err)
		}
		for _, card := range cards {
			names[model.InboxSelection{Source: "external", ID: card.ID}] = card.Title
		}
	}
	result := make([]model.ResourceRelation, 0, len(rows))
	for index, row := range rows {
		if name, visible := names[peers[index]]; visible {
			result = append(result, model.ResourceRelation{ID: row.ID, Target: peers[index], Name: name, CreatedAt: row.CreatedAt})
		}
	}
	return result, nil
}

// relationEndpointName 读取来源是否仍属于当前用户；不存在与跨用户统一返回未找到。
func relationEndpointName(tx *gorm.DB, owner string, entry model.InboxSelection) (string, error) {
	var row struct {
		Name string `gorm:"column:name"`
	}
	query := tx
	if entry.Source == "file" {
		query = query.Table("resources").Select("name").Where("id = ? AND user_id = ? AND deleted_at IS NULL", entry.ID, owner)
	} else if entry.Source == "external" {
		query = query.Table("external_resources").Select("title AS name").Where("id = ? AND user_id = ?", entry.ID, owner)
	} else {
		return "", ErrNotFound
	}
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("find relation endpoint: %w", err)
	}
	return row.Name, nil
}

// DeleteRelation 只删除当前用户的关系，关联的两条资料及外部链接都保持不变。
func (store *MySQL) DeleteRelation(ctx context.Context, id string) error {
	owner, err := externalOwner(ctx)
	if err != nil {
		return err
	}
	result := store.db.WithContext(ctx).Table("resource_relations").Where("id = ? AND user_id = ?", id, owner).Delete(&relationRow{})
	if result.Error != nil {
		return fmt.Errorf("delete relation: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// removeRelationsForEntries 在删除事务内清理任一端匹配的关系，不允许留下失效链接。
func removeRelationsForEntries(tx *gorm.DB, owner string, entries []model.InboxSelection) error {
	for _, entry := range entries {
		if err := tx.Table("resource_relations").Where("user_id = ? AND ((left_source = ? AND left_id = ?) OR (right_source = ? AND right_id = ?))", owner, entry.Source, entry.ID, entry.Source, entry.ID).Delete(&relationRow{}).Error; err != nil {
			return fmt.Errorf("clear deleted resource relations: %w", err)
		}
	}
	return nil
}
