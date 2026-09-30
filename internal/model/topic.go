package model

import "time"

// TopicInput 是专题创建和整体替换的输入，不接收客户端指定归属与顺序编号。
type TopicInput struct {
	Title       string         `json:"title"`
	Intro       string         `json:"intro"`
	CoverFileID *string        `json:"cover_file_id"`
	Sections    []TopicSection `json:"sections"`
}

// TopicSection 保存分区标题及由数组顺序决定的条目顺序。
type TopicSection struct {
	Title string      `json:"title"`
	Items []TopicItem `json:"items"`
}

// TopicItem 仅呈现当前可见文件或卡片的名称，不包含外部位置及存储路径。
type TopicItem struct {
	Source string `json:"source"`
	ID     string `json:"id"`
	Name   string `json:"name"`
}

// TopicSummary 为私有专题列表提供有界的统计信息。
type TopicSummary struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Intro        string    `json:"intro"`
	CoverFileID  *string   `json:"cover_file_id"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	SectionCount int       `json:"section_count"`
	ItemCount    int       `json:"item_count"`
}

// TopicDetail 扩展列表信息，包含有序的分区及可见条目。
type TopicDetail struct {
	TopicSummary
	Sections []TopicSection `json:"sections"`
}
