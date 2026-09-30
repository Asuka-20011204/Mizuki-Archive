package model

import "time"

// SearchFilter 是用户可保存的检索条件；空关键词只有在其他条件存在时才允许执行。
type SearchFilter struct {
	Query              string `json:"q"`
	Source             string `json:"source"`
	Kind               string `json:"kind"`
	Tag                string `json:"tag"`
	OrganizationStatus string `json:"organization_status"`
}

// SavedSearch 只保存当前账号的检索条件，不复制匹配到的私有资料或派生正文。
type SavedSearch struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	Filter    SearchFilter `json:"filter"`
	CreatedAt time.Time    `json:"created_at"`
}

// SearchResults 将站内文件与外部卡片分组，避免混淆原件是否已上传；分页分别限制两类结果。
type SearchResults struct {
	Files             []Resource         `json:"files"`
	ExternalResources []ExternalResource `json:"external_resources"`
	Page              int                `json:"page"`
	HasMoreFiles      bool               `json:"has_more_files"`
	HasMoreExternal   bool               `json:"has_more_external"`
}
