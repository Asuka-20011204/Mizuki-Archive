package model

import "time"

// ResourceNote 是当前用户对已上传文件的私人注记；页码仅适用于 PDF。
type ResourceNote struct {
	ID         string    `json:"id"`
	ResourceID string    `json:"resource_id"`
	PageNumber *int      `json:"page_number,omitempty"`
	Excerpt    string    `json:"excerpt"`
	Content    string    `json:"content"`
	Source     string    `json:"source"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}
