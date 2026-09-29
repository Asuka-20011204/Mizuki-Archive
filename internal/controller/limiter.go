package controller

import (
	"net"
	"sync"
	"time"
)

// loginLimiter 在单进程内按客户端地址记录失败时间；多实例部署需改用共享状态。
type loginLimiter struct {
	mutex    sync.Mutex
	attempts map[string][]time.Time
}

// requestLimiter 在单进程内限制验证码等高成本请求；共享 Redis 不可用时仍能保护当前实例。
type requestLimiter struct {
	mutex    sync.Mutex
	requests map[string][]time.Time
}

// newLoginLimiter 目前只保护单进程登录入口；多实例部署前必须改为共享限流状态。
func newLoginLimiter() *loginLimiter {
	return &loginLimiter{attempts: map[string][]time.Time{}}
}

// newRequestLimiter 创建按地址和接口分别计数的本地固定窗口限流器。
func newRequestLimiter() *requestLimiter {
	return &requestLimiter{requests: map[string][]time.Time{}}
}

// clientAddress 从网络地址提取客户端 IP；不信任可由请求方伪造的转发头。
func clientAddress(remote string) string {
	address, _, err := net.SplitHostPort(remote)
	if err != nil {
		return remote
	}
	return address
}

// allowed 清除过期失败记录，并限制单 IP 尝试次数及全局记录桶数。
func (limiter *loginLimiter) allowed(address string) bool {
	limiter.mutex.Lock()
	defer limiter.mutex.Unlock()
	// 每次检查时顺手移除过期窗口，避免长期运行后尝试记录无限增长。
	cutoff := time.Now().Add(-15 * time.Minute)
	for key, attempts := range limiter.attempts {
		active := attempts[:0]
		for _, attempted := range attempts {
			if attempted.After(cutoff) {
				active = append(active, attempted)
			}
		}
		if len(active) == 0 {
			delete(limiter.attempts, key)
		} else {
			limiter.attempts[key] = active
		}
	}
	// 限制未知地址的总桶数；达到上限后已有地址仍按自身失败次数判断。
	return len(limiter.attempts[address]) < 5 && (len(limiter.attempts) < 10000 || limiter.attempts[address] != nil)
}

// failed 记录一次密码核验失败，供同一 IP 的后续请求限流。
func (limiter *loginLimiter) failed(address string) {
	limiter.mutex.Lock()
	defer limiter.mutex.Unlock()
	limiter.attempts[address] = append(limiter.attempts[address], time.Now())
}

// succeeded 清除该 IP 的失败窗口，避免成功登录后继续被旧失败记录限制。
func (limiter *loginLimiter) succeeded(address string) {
	limiter.mutex.Lock()
	defer limiter.mutex.Unlock()
	delete(limiter.attempts, address)
}

// allowedRequests 清理过期请求并判断当前键是否还能发起高成本请求。
func (limiter *requestLimiter) allowedRequests(key string, limit int, window time.Duration) bool {
	limiter.mutex.Lock()
	defer limiter.mutex.Unlock()
	now := time.Now()
	cutoff := now.Add(-window)
	for existingKey, requests := range limiter.requests {
		active := requests[:0]
		for _, requestedAt := range requests {
			if requestedAt.After(cutoff) {
				active = append(active, requestedAt)
			}
		}
		if len(active) == 0 {
			delete(limiter.requests, existingKey)
			continue
		}
		limiter.requests[existingKey] = active
	}
	if _, exists := limiter.requests[key]; !exists && len(limiter.requests) >= 10000 {
		return false
	}
	active := limiter.requests[key][:0]
	for _, requestedAt := range limiter.requests[key] {
		if requestedAt.After(cutoff) {
			active = append(active, requestedAt)
		}
	}
	if len(active) >= limit {
		limiter.requests[key] = active
		return false
	}
	limiter.requests[key] = append(active, now)
	return true
}
