package model

// InboxSelection 指向一条文件或外部卡片；既可用于收件箱整理，也可用于混合来源批量操作。
type InboxSelection struct {
	Source string `json:"source"`
	ID     string `json:"id"`
}
