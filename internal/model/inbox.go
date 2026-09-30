package model

// InboxSelection 指向一条待整理资料；来源明确区分站内文件与外部卡片。
type InboxSelection struct {
	Source string `json:"source"`
	ID     string `json:"id"`
}
