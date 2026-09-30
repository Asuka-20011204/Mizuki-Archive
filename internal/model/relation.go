package model

import "time"

// ResourceRelation 表示当前条目关联的另一条私有资料；列表只返回可见目标的名称，不暴露内部归属字段。
type ResourceRelation struct {
	ID        string         `json:"id"`
	Target    InboxSelection `json:"target"`
	Name      string         `json:"name"`
	CreatedAt time.Time      `json:"created_at"`
}
