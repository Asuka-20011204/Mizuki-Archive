package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"mizuki-archive/internal/model"
)

const maxTopicsPerUser = 30

// topicRow 映射专题主表，不将 user_id 输出到接口。
type topicRow struct {
	ID          string    `gorm:"column:id;primaryKey"`
	UserID      string    `gorm:"column:user_id"`
	Title       string    `gorm:"column:title"`
	Intro       string    `gorm:"column:intro"`
	CoverFileID *string   `gorm:"column:cover_file_id"`
	CreatedAt   time.Time `gorm:"column:created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
}

// topicSectionRow 映射同一专题内的有序分区。
type topicSectionRow struct {
	ID       string `gorm:"column:id;primaryKey"`
	TopicID  string `gorm:"column:topic_id"`
	Position int    `gorm:"column:position"`
	Title    string `gorm:"column:title"`
}

// topicItemRow 映射分区内的有序引用，不保存名称或外部地址。
type topicItemRow struct {
	SectionID  string `gorm:"column:section_id"`
	Position   int    `gorm:"column:position"`
	Source     string `gorm:"column:source"`
	ResourceID string `gorm:"column:resource_id"`
}

// newTopicID 为分区生成与专题相同格式的不可预测 ID。
func newTopicID() (string, error) {
	identifier := make([]byte, 16)
	if _, err := rand.Read(identifier); err != nil {
		return "", err
	}
	return hex.EncodeToString(identifier), nil
}

// lockTopicReferences 按固定顺序加锁，阻止删除与引用创建交错形成悬空项。
func lockTopicReferences(tx *gorm.DB, owner string, input model.TopicInput) error {
	entries := make(map[model.InboxSelection]bool)
	for _, section := range input.Sections {
		for _, item := range section.Items {
			entries[model.InboxSelection{Source: item.Source, ID: item.ID}] = true
		}
	}
	if input.CoverFileID != nil {
		entries[model.InboxSelection{Source: "file", ID: *input.CoverFileID}] = true
	}
	ordered := make([]model.InboxSelection, 0, len(entries))
	for entry := range entries {
		ordered = append(ordered, entry)
	}
	sort.Slice(ordered, func(first, second int) bool {
		if ordered[first].Source != ordered[second].Source {
			return ordered[first].Source == "file"
		}
		return ordered[first].ID < ordered[second].ID
	})
	for _, entry := range ordered {
		if _, err := lockRelationEndpoint(tx, owner, entry); err != nil {
			return err
		}
	}
	if input.CoverFileID != nil {
		var count int64
		if err := tx.Table("resources").Where("id = ? AND user_id = ? AND kind = 'image' AND deleted_at IS NULL", *input.CoverFileID, owner).Count(&count).Error; err != nil {
			return fmt.Errorf("check topic cover: %w", err)
		}
		if count != 1 {
			return ErrNotFound
		}
	}
	return nil
}

// insertTopicSections 在主表写入的同一事务中持久化数组顺序。
func insertTopicSections(tx *gorm.DB, id string, sections []model.TopicSection) error {
	for index, section := range sections {
		sectionID, err := newTopicID()
		if err != nil {
			return err
		}
		row := topicSectionRow{ID: sectionID, TopicID: id, Position: index, Title: section.Title}
		if err := tx.Table("topic_sections").Create(&row).Error; err != nil {
			return fmt.Errorf("create topic section: %w", err)
		}
		for position, item := range section.Items {
			entry := topicItemRow{SectionID: sectionID, Position: position, Source: item.Source, ResourceID: item.ID}
			if err := tx.Table("topic_items").Create(&entry).Error; err != nil {
				return fmt.Errorf("create topic item: %w", err)
			}
		}
	}
	return nil
}

// SaveTopic 在事务中锁住本人专题及所有被引用资源，并整体替换内容。
func (store *MySQL) SaveTopic(ctx context.Context, id string, input model.TopicInput, update bool) (model.TopicDetail, error) {
	owner, err := externalOwner(ctx)
	if err != nil {
		return model.TopicDetail{}, err
	}
	var saved model.TopicDetail
	err = store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if !update {
			var user struct {
				ID string `gorm:"column:id"`
			}
			if err := tx.Table("users").Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("id = ?", owner).Take(&user).Error; err != nil {
				return fmt.Errorf("lock topic owner: %w", err)
			}
			var count int64
			if err := tx.Table("topics").Where("user_id = ?", owner).Count(&count).Error; err != nil {
				return fmt.Errorf("count topics: %w", err)
			}
			if count >= maxTopicsPerUser {
				return ErrTopicLimit
			}
		}
		// 删除入口先锁资源主行；更新也必须先锁引用、后锁专题，避免反向锁序。
		if err := lockTopicReferences(tx, owner, input); err != nil {
			return err
		}
		if update {
			var existing topicRow
			if err := tx.Table("topics").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", id, owner).Take(&existing).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrNotFound
				}
				return fmt.Errorf("lock topic: %w", err)
			}
		}
		now := time.Now().UTC()
		if update {
			if err := tx.Table("topics").Where("id = ? AND user_id = ?", id, owner).Updates(map[string]any{"title": input.Title, "intro": input.Intro, "cover_file_id": input.CoverFileID, "updated_at": now}).Error; err != nil {
				return fmt.Errorf("update topic: %w", err)
			}
			if err := tx.Table("topic_sections").Where("topic_id = ?", id).Delete(&topicSectionRow{}).Error; err != nil {
				return fmt.Errorf("replace topic sections: %w", err)
			}
		} else {
			row := topicRow{ID: id, UserID: owner, Title: input.Title, Intro: input.Intro, CoverFileID: input.CoverFileID, CreatedAt: now, UpdatedAt: now}
			if err := tx.Table("topics").Create(&row).Error; err != nil {
				return fmt.Errorf("create topic: %w", err)
			}
		}
		if err := insertTopicSections(tx, id, input.Sections); err != nil {
			return err
		}
		// 提交前在同一连接回读；读取失败会回滚写入，避免已保存却向用户报告失败。
		var readErr error
		saved, readErr = NewMySQL(tx).GetTopic(ctx, id)
		return readErr
	})
	if err != nil {
		return model.TopicDetail{}, err
	}
	return saved, nil
}

// ListTopics 按当前用户返回专题及已存在引用的计数，过滤失效封面。
func (store *MySQL) ListTopics(ctx context.Context) ([]model.TopicSummary, error) {
	owner, err := externalOwner(ctx)
	if err != nil {
		return nil, err
	}
	rows := make([]model.TopicSummary, 0)
	err = store.db.WithContext(ctx).Table("topics AS t").
		Select(`t.id, t.title, t.intro, CASE WHEN cover.id IS NOT NULL THEN t.cover_file_id ELSE NULL END AS cover_file_id, t.created_at, t.updated_at, COUNT(DISTINCT s.id) AS section_count, COALESCE(SUM(CASE WHEN f.id IS NOT NULL OR e.id IS NOT NULL THEN 1 ELSE 0 END), 0) AS item_count`).
		Joins("LEFT JOIN resources AS cover ON cover.id = t.cover_file_id AND cover.user_id = t.user_id AND cover.kind = 'image' AND cover.deleted_at IS NULL").
		Joins("LEFT JOIN topic_sections AS s ON s.topic_id = t.id").
		Joins("LEFT JOIN topic_items AS i ON i.section_id = s.id").
		Joins("LEFT JOIN resources AS f ON i.source = 'file' AND f.id = i.resource_id AND f.user_id = t.user_id AND f.deleted_at IS NULL").
		Joins("LEFT JOIN external_resources AS e ON i.source = 'external' AND e.id = i.resource_id AND e.user_id = t.user_id").
		Where("t.user_id = ?", owner).Group("t.id").Order("t.created_at DESC, t.id DESC").Limit(maxTopicsPerUser).Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list topics: %w", err)
	}
	return rows, nil
}

// GetTopic 批量读取有序分区与可见引用名称，永不选择地址、存储键或其他用户的名称。
func (store *MySQL) GetTopic(ctx context.Context, id string) (model.TopicDetail, error) {
	owner, err := externalOwner(ctx)
	if err != nil {
		return model.TopicDetail{}, err
	}
	var result model.TopicDetail
	err = store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row topicRow
		if err := tx.Table("topics").Where("id = ? AND user_id = ?", id, owner).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return fmt.Errorf("find topic: %w", err)
		}
		result.TopicSummary = model.TopicSummary{ID: row.ID, Title: row.Title, Intro: row.Intro, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
		if row.CoverFileID != nil {
			var cover int64
			if err := tx.Table("resources").Where("id = ? AND user_id = ? AND kind = 'image' AND deleted_at IS NULL", *row.CoverFileID, owner).Count(&cover).Error; err != nil {
				return err
			}
			if cover == 1 {
				result.CoverFileID = row.CoverFileID
			}
		}
		var sections []topicSectionRow
		if err := tx.Table("topic_sections").Where("topic_id = ?", id).Order("position ASC").Find(&sections).Error; err != nil {
			return fmt.Errorf("list topic sections: %w", err)
		}
		result.Sections = make([]model.TopicSection, 0, len(sections))
		result.SectionCount = len(sections)
		if len(sections) == 0 {
			return nil
		}
		sectionIDs := make([]string, 0, len(sections))
		for _, section := range sections {
			sectionIDs = append(sectionIDs, section.ID)
			result.Sections = append(result.Sections, model.TopicSection{Title: section.Title, Items: []model.TopicItem{}})
		}
		var items []struct {
			SectionID  string `gorm:"column:section_id"`
			Source     string `gorm:"column:source"`
			ResourceID string `gorm:"column:resource_id"`
			Name       string `gorm:"column:name"`
		}
		if err := tx.Table("topic_items AS i").
			Select("i.section_id, i.source, i.resource_id, CASE WHEN i.source = 'file' THEN f.name ELSE e.title END AS name").
			Joins("JOIN topic_sections AS s ON s.id = i.section_id").
			Joins("JOIN topics AS t ON t.id = s.topic_id AND t.user_id = ?", owner).
			Joins("LEFT JOIN resources AS f ON i.source = 'file' AND f.id = i.resource_id AND f.user_id = t.user_id AND f.deleted_at IS NULL").
			Joins("LEFT JOIN external_resources AS e ON i.source = 'external' AND e.id = i.resource_id AND e.user_id = t.user_id").
			Where("i.section_id IN ? AND (f.id IS NOT NULL OR e.id IS NOT NULL)", sectionIDs).
			Order("s.position ASC, i.position ASC").Find(&items).Error; err != nil {
			return fmt.Errorf("list topic items: %w", err)
		}
		positions := make(map[string]int, len(sections))
		for index, section := range sections {
			positions[section.ID] = index
		}
		for _, item := range items {
			index := positions[item.SectionID]
			result.Sections[index].Items = append(result.Sections[index].Items, model.TopicItem{Source: item.Source, ID: item.ResourceID, Name: item.Name})
			result.ItemCount++
		}
		return nil
	})
	if err != nil {
		return model.TopicDetail{}, err
	}
	return result, nil
}

// DeleteTopic 只删除本人专题；分区及引用由外键级联清理。
func (store *MySQL) DeleteTopic(ctx context.Context, id string) error {
	owner, err := externalOwner(ctx)
	if err != nil {
		return err
	}
	result := store.db.WithContext(ctx).Table("topics").Where("id = ? AND user_id = ?", id, owner).Delete(&topicRow{})
	if result.Error != nil {
		return fmt.Errorf("delete topic: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// removeTopicReferences 在文件软删除或卡片硬删除的同一事务移除引用及封面。
func removeTopicReferences(tx *gorm.DB, owner string, entries []model.InboxSelection) error {
	for _, entry := range entries {
		if entry.Source == "file" {
			if err := tx.Table("topics").Where("user_id = ? AND cover_file_id = ?", owner, entry.ID).Update("cover_file_id", nil).Error; err != nil {
				return fmt.Errorf("clear topic cover: %w", err)
			}
		}
		if err := tx.Exec(`DELETE i FROM topic_items AS i JOIN topic_sections AS s ON s.id = i.section_id JOIN topics AS t ON t.id = s.topic_id WHERE t.user_id = ? AND i.source = ? AND i.resource_id = ?`, owner, entry.Source, entry.ID).Error; err != nil {
			return fmt.Errorf("clear topic reference: %w", err)
		}
	}
	return nil
}
