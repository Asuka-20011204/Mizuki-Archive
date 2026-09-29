package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"mizuki-archive/internal/model"
)

// TestMySQLProcessingCapacity 验证隔离库中的并发提交共享同一容量上限，完成后可重新放行。
func TestMySQLProcessingCapacity(t *testing.T) {
	dsn := os.Getenv("MIZUKI_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("仅在设置隔离测试库 DSN 时运行")
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
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store := NewMySQL(database)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	jobs := make([]model.ProcessingJob, 5)
	resourceIDs := make([]string, 0, len(jobs))
	t.Cleanup(func() {
		_ = database.WithContext(context.Background()).Exec("DELETE FROM resources WHERE id IN ?", resourceIDs).Error
	})
	for index := range jobs {
		resourceID, idErr := newProcessingID()
		if idErr != nil {
			t.Fatal(idErr)
		}
		jobID, idErr := newProcessingID()
		if idErr != nil {
			t.Fatal(idErr)
		}
		resourceIDs = append(resourceIDs, resourceID)
		resource := model.Resource{ID: resourceID, Name: fmt.Sprintf("capacity-%d.txt", index), OriginalName: "capacity.txt", Kind: "text", MIME: "text/plain", StorageKey: resourceID, SHA256: strings.Repeat("a", 64), CreatedAt: time.Now().UTC()}
		if err := store.SaveResource(ctx, resource); err != nil {
			t.Fatal(err)
		}
		jobs[index] = model.ProcessingJob{ID: jobID, ResourceID: resourceID, Type: model.ProcessingTypeExtractText, SourceSHA256: resource.SHA256, Status: model.ProcessingStatusPending, MaxAttempts: 3, AvailableAt: time.Now().UTC(), CreatedAt: time.Now().UTC()}
	}
	start := make(chan struct{})
	results := make(chan error, len(jobs))
	var workers sync.WaitGroup
	for _, job := range jobs {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			results <- store.CreateProcessingJobWithinLimit(ctx, job, 2, true)
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	accepted, rejected := 0, 0
	for err := range results {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, ErrProcessingQueueFull):
			rejected++
		default:
			t.Fatalf("并发提交失败: %v", err)
		}
	}
	if accepted != 2 || rejected != 3 {
		t.Fatalf("并发配额: accepted=%d rejected=%d", accepted, rejected)
	}
	var count int64
	if err := database.Table("processing_outbox").Where("job_id IN ?", processingTestJobIDs(jobs)).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("Outbox 与已接收任务不一致: %d, %v", count, err)
	}
	for _, job := range jobs {
		if _, err := store.GetProcessingJob(ctx, job.ID); err == nil {
			duplicate := job
			duplicate.ID, err = newProcessingID()
			if err != nil {
				t.Fatal(err)
			}
			if err := store.CreateProcessingJobWithinLimit(ctx, duplicate, 2, true); !errors.Is(err, ErrProcessingJobExists) {
				t.Fatalf("队列满时重复请求应先识别幂等任务: %v", err)
			}
			claimed, err := store.ClaimProcessingJob(ctx, job.ID, time.Now().UTC().Add(time.Second))
			if err != nil {
				t.Fatal(err)
			}
			if err := store.FailProcessingJob(ctx, job.ID, claimed.LeaseToken, "测试释放配额", nil); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	for _, job := range jobs {
		if _, err := store.GetProcessingJob(ctx, job.ID); errors.Is(err, ErrNotFound) {
			if err := store.CreateProcessingJobWithinLimit(ctx, job, 2, false); err != nil {
				t.Fatalf("释放配额后数据库模式仍无法提交: %v", err)
			}
			break
		}
	}
	if err := database.Table("processing_outbox").Where("job_id IN ?", processingTestJobIDs(jobs)).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("数据库模式意外写入 Outbox: %d, %v", count, err)
	}
}

// processingTestJobIDs 收集隔离用例的任务 ID，用于核对事件数。
func processingTestJobIDs(jobs []model.ProcessingJob) []string {
	ids := make([]string, 0, len(jobs))
	for _, job := range jobs {
		ids = append(ids, job.ID)
	}
	return ids
}
