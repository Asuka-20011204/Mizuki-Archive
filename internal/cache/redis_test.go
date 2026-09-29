package cache

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"mizuki-archive/internal/repository"
)

// TestNewRedisRejectsInvalidURL 不把无效连接字符串中的凭据内容写入错误文本。
func TestNewRedisRejectsInvalidURL(t *testing.T) {
	_, err := NewRedis("not-a-url-secret", "")
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("无效 URL 的错误未脱敏: %v", err)
	}
}

// TestRedisOperations 在显式隔离命名空间验证 TTL、限流及最近访问删除，不清空共享数据库。
func TestRedisOperations(t *testing.T) {
	url := os.Getenv("MIZUKI_TEST_REDIS_URL")
	if url == "" {
		t.Skip("仅在设置隔离 Redis 地址时运行")
	}
	client, err := NewRedis(url, "mizuki:test:"+time.Now().UTC().Format("150405.000000000")+":")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx := context.Background()
	var data struct{ Name string }
	if hit, err := client.Get(ctx, "metadata", &data); err != nil || hit {
		t.Fatalf("预期缓存未命中: %t, %v", hit, err)
	}
	if err := client.Set(ctx, "metadata", struct{ Name string }{"资料"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if hit, err := client.Get(ctx, "metadata", &data); err != nil || !hit || data.Name != "资料" {
		t.Fatalf("缓存读取失败: %t, %v, %#v", hit, err, data)
	}
	if err := client.Set(ctx, "invalid", make(chan int), time.Minute); err == nil {
		t.Fatal("不支持 JSON 的数据不应写入缓存")
	}
	if err := client.client.Set(ctx, client.key("malformed"), "not-json", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get(ctx, "malformed", &data); err == nil {
		t.Fatal("非法 JSON 缓存必须返回解码错误")
	}
	if err := client.Set(ctx, "group:first", "a", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := client.Set(ctx, "group:second", "b", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteByPrefix(ctx, "group:"); err != nil {
		t.Fatal(err)
	}
	var removed string
	if hit, err := client.Get(ctx, "group:first", &removed); err != nil || hit {
		t.Fatalf("前缀失效后仍有缓存: %t, %v", hit, err)
	}
	if err := client.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.Set(ctx, "no-ttl", "ignored", 0); err != nil {
		t.Fatal(err)
	}
	if hit, err := client.Get(ctx, "no-ttl", &removed); err != nil || hit {
		t.Fatalf("零 TTL 不应保留缓存: %t, %v", hit, err)
	}
	if allowed, err := client.Allow(ctx, "login:loopback", 1, time.Minute); err != nil || !allowed {
		t.Fatalf("首次限流结果错误: %t, %v", allowed, err)
	}
	if allowed, err := client.Allow(ctx, "login:loopback", 1, time.Minute); err != nil || allowed {
		t.Fatalf("第二次应被限流: %t, %v", allowed, err)
	}
	if err := client.RecordRecent(ctx, "resource-id", time.Now(), 20); err != nil {
		t.Fatal(err)
	}
	if ids, err := client.RecentIDs(ctx, 20); err != nil || len(ids) != 1 || ids[0] != "resource-id" {
		t.Fatalf("最近访问未读取到资料 ID: %v, %v", ids, err)
	}
	if err := client.RemoveRecent(ctx, "resource-id"); err != nil {
		t.Fatal(err)
	}
	if ids, err := client.RecentIDs(ctx, 20); err != nil || len(ids) != 0 {
		t.Fatalf("删除后仍保留访问记录: %v, %v", ids, err)
	}
	userAContext := repository.WithUserID(ctx, "user-a")
	userBContext := repository.WithUserID(ctx, "user-b")
	if err := client.RecordRecent(userAContext, "resource-a", time.Now(), 20); err != nil {
		t.Fatal(err)
	}
	if err := client.RecordRecent(userBContext, "resource-b", time.Now(), 20); err != nil {
		t.Fatal(err)
	}
	if ids, err := client.RecentIDs(userAContext, 20); err != nil || len(ids) != 1 || ids[0] != "resource-a" {
		t.Fatalf("user A recent namespace leaked: %v, %v", ids, err)
	}
	if ids, err := client.RecentIDs(userBContext, 20); err != nil || len(ids) != 1 || ids[0] != "resource-b" {
		t.Fatalf("user B recent namespace leaked: %v, %v", ids, err)
	}
	if err := client.RemoveRecent(userAContext, "resource-a"); err != nil {
		t.Fatal(err)
	}
	if ids, err := client.RecentIDs(userBContext, 20); err != nil || len(ids) != 1 || ids[0] != "resource-b" {
		t.Fatalf("user A removal affected user B recent namespace: %v, %v", ids, err)
	}
	if err := client.Delete(ctx, "metadata"); err != nil {
		t.Fatal(err)
	}
}
