package model

import "time"

// EmailChallenge 只携带不可逆的验证码摘要和时限；明文验证码不会进入数据库。
type EmailChallenge struct {
	Email         string
	Purpose       string
	Digest        string
	ExpiresAt     time.Time
	NextRequestAt time.Time
}
