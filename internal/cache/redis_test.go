package cache

import (
	"context"
	"os"
	"testing"
	"time"
)

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
	if allowed, err := client.Allow(ctx, "login:loopback", 1, time.Minute); err != nil || !allowed {
		t.Fatalf("首次限流结果错误: %t, %v", allowed, err)
	}
	if allowed, err := client.Allow(ctx, "login:loopback", 1, time.Minute); err != nil || allowed {
		t.Fatalf("第二次应被限流: %t, %v", allowed, err)
	}
	if err := client.RecordRecent(ctx, "resource-id", time.Now(), 20); err != nil {
		t.Fatal(err)
	}
	if err := client.RemoveRecent(ctx, "resource-id"); err != nil {
		t.Fatal(err)
	}
	if ids, err := client.RecentIDs(ctx, 20); err != nil || len(ids) != 0 {
		t.Fatalf("删除后仍保留访问记录: %v, %v", ids, err)
	}
	if err := client.Delete(ctx, "metadata"); err != nil {
		t.Fatal(err)
	}
}
