// Package model 存放跨 Controller、Service、Repository 传递的业务数据，不依赖 Gin 或 GORM。
package model

import "time"

// Resource 描述资料元数据。原始文件保存在受控目录，JSON 不暴露内部存储键。
type Resource struct {
	ID           string    `json:"id"`
	OwnerID      string    `json:"-"`
	Name         string    `json:"name"`
	OriginalName string    `json:"original_name"`
	Kind         string    `json:"kind"`
	MIME         string    `json:"mime"`
	Size         int64     `json:"size"`
	SHA256       string    `json:"sha256"`
	StorageKey   string    `json:"-"`
	Favorite     bool      `json:"favorite"`
	Tags         []string  `json:"tags"`
	CreatedAt    time.Time `json:"created_at"`
}

// ListQuery 由 Controller 校验后传给 Service，再交给 Repository 做分页查询。
type ListQuery struct {
	Search string
	Kind   string
	Tag    string
	Limit  int
	Offset int
}
