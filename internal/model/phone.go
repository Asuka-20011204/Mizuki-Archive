package model

import "time"

// PhoneChallenge 只保存手机号、验证码摘要和时间窗口；明文验证码不落库。
type PhoneChallenge struct {
	Phone         string
	Purpose       string
	Digest        string
	ExpiresAt     time.Time
	NextRequestAt time.Time
}
