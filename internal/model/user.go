package model

import "time"

// User 表示一个独立的资料库用户；密码哈希只在服务端内部流转，不对外序列化。
type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	Email        string    `json:"email"`
	PasswordHash []byte    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}
