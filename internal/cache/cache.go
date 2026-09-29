// Package cache 定义可丢失的热点缓存、最近访问和共享限流边界。
package cache

import (
	"context"
	"errors"
	"time"
)

// ErrMiss 表示缓存没有对应值，调用方应回源 MySQL。
var ErrMiss = errors.New("cache miss")

// Cache 只描述业务需要的行为，Service 不依赖 Redis 客户端类型。
type Cache interface {
	Get(context.Context, string, any) (bool, error)
	Set(context.Context, string, any, time.Duration) error
	Delete(context.Context, ...string) error
	DeleteByPrefix(context.Context, string) error
	RecordRecent(context.Context, string, time.Time, int64) error
	RemoveRecent(context.Context, string) error
	RecentIDs(context.Context, int64) ([]string, error)
	Allow(context.Context, string, int, time.Duration) (bool, error)
	Close() error
}

// RateLimiter 是跨进程请求限流的最小接口，Redis 不可用时调用方可降级。
type RateLimiter interface {
	Allow(context.Context, string, int, time.Duration) (bool, error)
}
