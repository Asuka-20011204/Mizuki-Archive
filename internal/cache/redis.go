package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"mizuki-archive/internal/repository"
)

// Redis 将 Redis 故障降级交给 Service；自身只负责连接、序列化和原子操作。
type Redis struct {
	client *redis.Client
	prefix string
}

// NewRedis 从 URL 创建短连接超时的客户端，不把连接密码写入日志或错误文本。
func NewRedis(rawURL, prefix string) (*Redis, error) {
	options, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid Redis URL")
	}
	options.DialTimeout = 2 * time.Second
	options.ReadTimeout = 2 * time.Second
	options.WriteTimeout = 2 * time.Second
	options.PoolSize = 10
	if prefix == "" {
		prefix = "mizuki:v4:"
	}
	return &Redis{client: redis.NewClient(options), prefix: prefix}, nil
}

// Get 读取 JSON 缓存；未命中返回 false，Redis 故障返回错误供调用方降级。
func (cache *Redis) Get(ctx context.Context, key string, target any) (bool, error) {
	value, err := cache.client.Get(ctx, cache.key(key)).Bytes()
	if err == redis.Nil {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(value, target); err != nil {
		return false, fmt.Errorf("decode Redis cache: %w", err)
	}
	return true, nil
}

// Set 使用明确 TTL 写入可重建缓存，不缓存原始文件和完整提取正文。
func (cache *Redis) Set(ctx context.Context, key string, value any, ttl time.Duration) error {
	if ttl <= 0 {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode Redis cache: %w", err)
	}
	return cache.client.Set(ctx, cache.key(key), encoded, ttl).Err()
}

// Delete 删除精确缓存键；失效失败不会回滚已提交的 MySQL 事务。
func (cache *Redis) Delete(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	fullKeys := make([]string, 0, len(keys))
	for _, key := range keys {
		fullKeys = append(fullKeys, cache.key(key))
	}
	return cache.client.Del(ctx, fullKeys...).Err()
}

// DeleteByPrefix 使用 SCAN 删除命名空间，不阻塞 Redis 主线程，也不使用危险的 KEYS。
func (cache *Redis) DeleteByPrefix(ctx context.Context, prefix string) error {
	iterator := cache.client.Scan(ctx, 0, cache.key(prefix)+"*", 100).Iterator()
	for iterator.Next(ctx) {
		if err := cache.client.Del(ctx, iterator.Val()).Err(); err != nil {
			return err
		}
	}
	return iterator.Err()
}

// RecordRecent 只记录资源 ID 和时间戳，避免把资料正文或路径放进 Sorted Set。
func (cache *Redis) RecordRecent(ctx context.Context, resourceID string, accessedAt time.Time, limit int64) error {
	key := cache.recentKey(ctx)
	pipe := cache.client.TxPipeline()
	pipe.ZAdd(ctx, key, redis.Z{Score: float64(accessedAt.UnixMilli()), Member: resourceID})
	if limit > 0 {
		pipe.ZRemRangeByRank(ctx, key, 0, -limit-1)
	}
	pipe.Expire(ctx, key, 90*24*time.Hour)
	_, err := pipe.Exec(ctx)
	return err
}

// RemoveRecent 在资料软删除后清除最近访问痕迹；Redis 不可用时由数据到期兜底。
func (cache *Redis) RemoveRecent(ctx context.Context, resourceID string) error {
	return cache.client.ZRem(ctx, cache.recentKey(ctx), resourceID).Err()
}

// RecentIDs 按最近访问顺序返回资源 ID，资料详情仍由 MySQL 权限查询决定。
func (cache *Redis) RecentIDs(ctx context.Context, limit int64) ([]string, error) {
	values, err := cache.client.ZRevRange(ctx, cache.recentKey(ctx), 0, limit-1).Result()
	if err != nil {
		return nil, err
	}
	return values, nil
}

// Allow 使用 Redis Lua 脚本原子执行固定窗口限流，避免多 API 进程同时绕过计数。
func (cache *Redis) Allow(ctx context.Context, subject string, limit int, window time.Duration) (bool, error) {
	if limit <= 0 || window <= 0 {
		return false, nil
	}
	key := cache.key("rate:" + hashSubject(subject))
	result, err := cache.client.Eval(ctx, `local count = redis.call('INCR', KEYS[1]); if count == 1 then redis.call('EXPIRE', KEYS[1], ARGV[1]) end; return count`, []string{key}, int(window/time.Second)).Int64()
	if err != nil {
		return false, err
	}
	return result <= int64(limit), nil
}

// Close 关闭 Redis 连接池，服务停机时释放网络资源。
func (cache *Redis) Close() error { return cache.client.Close() }

// key 统一追加命名空间，避免不同版本或不同应用共享 Redis 时相互覆盖。
func (cache *Redis) key(value string) string { return cache.prefix + strings.TrimPrefix(value, ":") }

// recentKey 为每个会话用户使用独立 Sorted Set；缺少用户上下文时只允许内部调用使用 system 命名空间。
func (cache *Redis) recentKey(ctx context.Context) string {
	namespace := "system"
	if userID, ok := repository.UserIDFromContext(ctx); ok {
		namespace = "user:" + userID
	}
	return cache.key(namespace + ":recent-resources")
}

// hashSubject 避免把 IP、会话标识等限流输入直接暴露为 Redis key。
func hashSubject(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
