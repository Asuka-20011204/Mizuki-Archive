package model

// SearchResults 将站内文件与外部卡片分组，避免混淆原件是否已上传；分页分别限制两类结果。
type SearchResults struct {
	Files             []Resource         `json:"files"`
	ExternalResources []ExternalResource `json:"external_resources"`
	Page              int                `json:"page"`
	HasMoreFiles      bool               `json:"has_more_files"`
	HasMoreExternal   bool               `json:"has_more_external"`
}
