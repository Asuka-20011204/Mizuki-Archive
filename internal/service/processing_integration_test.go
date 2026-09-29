package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	amqp "github.com/rabbitmq/amqp091-go"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"mizuki-archive/internal/cache"
	"mizuki-archive/internal/model"
	"mizuki-archive/internal/queue"
	"mizuki-archive/internal/repository"
)

// TestProcessingQueueEndToEnd 在隔离 MySQL、RabbitMQ 和 Redis 中验证实际处理闭环与重复投递。
func TestProcessingQueueEndToEnd(t *testing.T) {
	dsn := os.Getenv("MIZUKI_TEST_MYSQL_DSN")
	brokerURL := os.Getenv("MIZUKI_TEST_RABBITMQ_URL")
	redisURL := os.Getenv("MIZUKI_TEST_REDIS_URL")
	if dsn == "" || brokerURL == "" || redisURL == "" {
		t.Skip("需要三个隔离测试依赖的显式地址")
	}
	config, err := driver.ParseDSN(dsn)
	if err != nil || !strings.HasPrefix(config.DBName, "mizuki_test_") {
		t.Fatal("测试 DSN 必须指向 mizuki_test_ 前缀的独立数据库")
	}
	config.ParseTime = true
	config.Loc = time.UTC
	database, err := gorm.Open(mysql.Open(config.FormatDSN()), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	store := repository.NewMySQL(database)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		t.Fatal(err)
	}
	resourceID := hex.EncodeToString(idBytes)
	content := []byte("Mizuki V3 V4 独立验收关键词\n")
	if err := os.WriteFile(filepath.Join(dataDir, resourceID), content, 0600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(content)
	resource := model.Resource{ID: resourceID, Name: "test.txt", OriginalName: "test.txt", Kind: "text", MIME: "text/plain", Size: int64(len(content)), StorageKey: resourceID, SHA256: hex.EncodeToString(hash[:]), CreatedAt: time.Now().UTC()}
	if err := store.SaveResource(ctx, resource); err != nil {
		t.Fatal(err)
	}
	defer database.WithContext(context.Background()).Exec("DELETE FROM resources WHERE id = ?", resourceID)
	sharedCache, err := cache.NewRedis(redisURL, "mizuki:e2e:"+resourceID+":")
	if err != nil {
		t.Fatal(err)
	}
	defer sharedCache.Close()
	processor, err := NewProcessingWithCache(store, store, dataDir, sharedCache)
	if err != nil {
		t.Fatal(err)
	}
	if err := processor.EnableOutbox(); err != nil {
		t.Fatal(err)
	}
	store.EnableOutbox()
	resources, err := NewResourcesWithCache(store, dataDir, sharedCache)
	if err != nil {
		t.Fatal(err)
	}
	job, err := processor.CreateTextJob(ctx, resourceID)
	if err != nil {
		t.Fatal(err)
	}
	name := "mizuki.e2e." + resourceID
	broker, err := queue.NewRabbitMQ(brokerURL, name, name+".jobs", name+".dead")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = broker.Close()
		connection, err := amqp.Dial(brokerURL)
		if err != nil {
			return
		}
		defer connection.Close()
		channel, err := connection.Channel()
		if err != nil {
			return
		}
		defer channel.Close()
		_, _ = channel.QueueDelete(name+".jobs", false, false, false)
		_, _ = channel.QueueDelete(name+".dead", false, false, false)
		_ = channel.ExchangeDelete(name, false, false)
	})
	results := make(chan error, 2)
	go func() {
		_ = broker.ConsumeJobs(ctx, func(ctx context.Context, jobID string) (bool, error) {
			if jobID != job.ID {
				results <- errors.New("收到不匹配的任务 ID")
				return true, nil
			}
			err := processor.RunJob(ctx, jobID)
			results <- err
			return err == nil, err
		}, 1)
	}()
	if published, err := processor.RunOutboxOnce(ctx, broker); err != nil || !published {
		t.Fatalf("Outbox 发布失败: %t, %v", published, err)
	}
	select {
	case err := <-results:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("任务没有在限时内消费")
	}
	completed, err := processor.GetJob(ctx, job.ID)
	if err != nil || completed.Status != model.ProcessingStatusSucceeded || completed.Asset == nil {
		t.Fatalf("任务未生成派生文件: %#v, %v", completed, err)
	}
	file, err := processor.OpenAsset(*completed.Asset)
	if err != nil {
		t.Fatal(err)
	}
	stat, err := file.Stat()
	_ = file.Close()
	if err != nil || stat.Size() != int64(len(content)) {
		t.Fatalf("派生文件大小不匹配: %v, %v", stat, err)
	}
	if err := broker.PublishJob(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-results:
		if err != nil {
			t.Fatalf("重复消息处理失败: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("重复消息未被消费")
	}
	// prefetch=1 时，第三条只有在上一条 ACK 后才能送达同一消费者。
	// 这样不只证明处理器回调执行，还证明前两条消息均释放了投递窗口。
	if err := broker.PublishJob(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-results:
		if err != nil {
			t.Fatalf("ACK 后重复投递失败: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("同一消费者的第二次 ACK 未释放投递窗口")
	}
	var count int64
	if err := database.Table("derived_assets").Where("job_id = ?", job.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("重复消息创建了额外产物: %d, %v", count, err)
	}
	baseline, err := NewProcessing(store, store, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	// 两组相同次数的串行读取仅用于同环境基线对照，不推断生产吞吐或缓存命中率。
	for _, measured := range []struct {
		name    string
		service *Processing
	}{
		{"MySQL 无缓存", baseline}, {"Redis 短缓存", processor},
	} {
		started := time.Now()
		for attempt := 0; attempt < 100; attempt++ {
			if _, err := measured.service.GetJob(ctx, job.ID); err != nil {
				t.Fatalf("%s 读取失败: %v", measured.name, err)
			}
		}
		t.Logf("%s: 100 次串行任务详情读取耗时 %s", measured.name, time.Since(started))
	}
	if _, err := resources.Get(ctx, resourceID); err != nil {
		t.Fatal(err)
	}
	if recent, err := resources.Recent(ctx, 10); err != nil || len(recent) != 1 {
		t.Fatalf("最近访问未记录: %d, %v", len(recent), err)
	}
	// Redis 故障不能影响 MySQL 任务状态和资料列表的读取。
	_ = sharedCache.Close()
	if _, err := processor.GetJob(ctx, job.ID); err != nil {
		t.Fatalf("Redis 故障后任务状态未回源: %v", err)
	}
	if _, err := resources.List(ctx, model.ListQuery{Limit: 10}); err != nil {
		t.Fatalf("Redis 故障后资料列表不可用: %v", err)
	}
}

// TestProcessingPoolRecoversExpiredLease 模拟旧进程领取后崩溃，验证并发 Worker 重启后恢复任务且只产出一份文件。
func TestProcessingPoolRecoversExpiredLease(t *testing.T) {
	dsn := os.Getenv("MIZUKI_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("仅在设置隔离 MySQL 地址时运行")
	}
	config, err := driver.ParseDSN(dsn)
	if err != nil || !strings.HasPrefix(config.DBName, "mizuki_test_") {
		t.Fatal("测试 DSN 必须指向 mizuki_test_ 前缀的独立数据库")
	}
	config.ParseTime = true
	config.Loc = time.UTC
	database, err := gorm.Open(mysql.Open(config.FormatDSN()), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store := repository.NewMySQL(database)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		t.Fatal(err)
	}
	resourceID := hex.EncodeToString(idBytes)
	content := []byte("Mizuki Worker Pool 恢复测试")
	if err := os.WriteFile(filepath.Join(dataDir, resourceID), content, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	resource := model.Resource{ID: resourceID, Name: "recovery.txt", OriginalName: "recovery.txt", Kind: "text", MIME: "text/plain", Size: int64(len(content)), StorageKey: resourceID, SHA256: hex.EncodeToString(digest[:]), CreatedAt: time.Now().UTC()}
	if err := store.SaveResource(ctx, resource); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = database.WithContext(context.Background()).Exec("DELETE FROM resources WHERE id = ?", resourceID).Error
	})
	processor, err := NewProcessing(store, store, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	job, err := processor.CreateTextJob(ctx, resourceID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimProcessingJob(ctx, job.ID, time.Now().UTC().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	// 旧进程已消失，只留下过期租约；下一进程必须凭新令牌领取并完成。
	if err := database.Table("processing_jobs").Where("id = ?", job.ID).Update("lease_until", time.Now().UTC().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	poolCtx, stop := context.WithCancel(ctx)
	finished := make(chan error, 1)
	go func() { finished <- processor.RunPool(poolCtx, 2, 20*time.Millisecond) }()
	defer func() { stop(); <-finished }()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("重启后的任务未在期限内完成")
		case <-ticker.C:
			completed, readErr := processor.GetJob(ctx, job.ID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if completed.Status == model.ProcessingStatusSucceeded {
				if completed.Attempts != 2 || completed.Asset == nil {
					t.Fatalf("重启恢复状态不正确: %#v", completed)
				}
				var count int64
				if err := database.Table("derived_assets").Where("job_id = ?", job.ID).Count(&count).Error; err != nil || count != 1 {
					t.Fatalf("并发恢复生成了重复产物: %d, %v", count, err)
				}
				return
			}
		}
	}
}
