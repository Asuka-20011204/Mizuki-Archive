package model

import "time"

// PortableRelation 保存关联的两个稳定标识，不复制目标名称或外部文件。
type PortableRelation struct {
	ID        string         `json:"id"`
	Left      InboxSelection `json:"left"`
	Right     InboxSelection `json:"right"`
	CreatedAt time.Time      `json:"created_at"`
}

// PortableArchive 是可离线阅读的账号资料清单；不包含上传原件、派生产物或认证数据。
type PortableArchive struct {
	Format            string             `json:"format"`
	Version           int                `json:"version"`
	ExportedAt        time.Time          `json:"exported_at"`
	Files             []Resource         `json:"files"`
	ExternalResources []ExternalResource `json:"external_resources"`
	Notes             []ResourceNote     `json:"notes"`
	Relations         []PortableRelation `json:"relations"`
}
