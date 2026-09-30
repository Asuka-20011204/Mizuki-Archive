package model

import "time"

// ExternalResource 是未上传原件的资源卡片；Location 只供用户查看，服务端不访问该地址。
type ExternalResource struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Location     string    `json:"location"`
	ResourceType string    `json:"resource_type"`
	Version      string    `json:"version"`
	Note         string    `json:"note"`
	Status       string    `json:"status"`
	Tags         []string  `json:"tags"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}
