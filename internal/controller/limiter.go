package controller

import (
	"net"
	"sync"
	"time"
)

type loginLimiter struct {
	mutex    sync.Mutex
	attempts map[string][]time.Time
}

// newLoginLimiter 目前只保护单进程登录入口；多实例部署前必须改为共享限流状态。
func newLoginLimiter() *loginLimiter {
	return &loginLimiter{attempts: map[string][]time.Time{}}
}

func clientAddress(remote string) string {
	address, _, err := net.SplitHostPort(remote)
	if err != nil {
		return remote
	}
	return address
}

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

func (limiter *loginLimiter) failed(address string) {
	limiter.mutex.Lock()
	defer limiter.mutex.Unlock()
	limiter.attempts[address] = append(limiter.attempts[address], time.Now())
}

func (limiter *loginLimiter) succeeded(address string) {
	limiter.mutex.Lock()
	defer limiter.mutex.Unlock()
	delete(limiter.attempts, address)
}
