package repository

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"mizuki-archive/internal/model"
)

// TestMySQLProcessingOutbox 在独立测试库验证事务写入、补偿事件、租约与崩溃次数上限。
func TestMySQLProcessingOutbox(t *testing.T) {
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
	db, err := gorm.Open(mysql.Open(config.FormatDSN()), &gorm.Config{})
	if err != nil {
		t.Fatalf("连接隔离测试库失败: %v", err)
	}
	connection, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	ctx := context.Background()
	store := NewMySQL(db)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	transaction := db.Begin()
	if transaction.Error != nil {
		t.Fatal(transaction.Error)
	}
	t.Cleanup(func() { _ = transaction.Rollback().Error })
	store = NewMySQL(transaction)
	resourceID, _ := newProcessingID()
	resource := model.Resource{ID: resourceID, Name: "queue.txt", OriginalName: "queue.txt", Kind: "text", MIME: "text/plain", StorageKey: resourceID, SHA256: strings.Repeat("a", 64), CreatedAt: time.Now().UTC()}
	if err := store.SaveResource(ctx, resource); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	jobID, _ := newProcessingID()
	job := model.ProcessingJob{ID: jobID, ResourceID: resourceID, Type: model.ProcessingTypeExtractText, SourceSHA256: resource.SHA256, Status: model.ProcessingStatusPending, MaxAttempts: 3, AvailableAt: now, CreatedAt: now}
	if err := store.CreateProcessingJobWithOutbox(ctx, job); err != nil {
		t.Fatalf("事务写入任务及事件失败: %v", err)
	}
	if err := store.CreateProcessingJobWithOutbox(ctx, job); !errors.Is(err, ErrProcessingJobExists) {
		t.Fatalf("重复请求未幂等拒绝: %v", err)
	}
	event, err := store.ClaimProcessingOutbox(ctx, now.Add(time.Second))
	if err != nil || event.JobID != jobID {
		t.Fatalf("领取事务事件失败: %#v, %v", event, err)
	}
	if err := store.MarkProcessingOutboxPublished(ctx, event.ID, event.LeaseToken); err != nil {
		t.Fatal(err)
	}
	if err := store.FailProcessingOutbox(ctx, event.ID, event.LeaseToken, "重试"); !errors.Is(err, ErrJobLeaseLost) {
		t.Fatalf("旧租约不应覆盖已发布事件: %v", err)
	}
	// 已发布消息丢失时，退避窗口过后重新生成事件；下一轮不能重复生成。
	repaired, err := store.RepairProcessingOutbox(ctx, now.Add(6*time.Minute))
	if err != nil || !repaired {
		t.Fatalf("补偿事件失败: %t, %v", repaired, err)
	}
	repaired, err = store.RepairProcessingOutbox(ctx, now.Add(6*time.Minute))
	if err != nil || repaired {
		t.Fatalf("不应重复补偿: %t, %v", repaired, err)
	}
	if err := transaction.Table("processing_jobs").Where("id = ?", jobID).Updates(map[string]any{"status": model.ProcessingStatusProcessing, "attempts": 3, "lease_until": now.Add(-time.Minute)}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimProcessingJob(ctx, jobID, now); !errors.Is(err, ErrNoPendingJob) {
		t.Fatalf("执行次数耗尽仍被领取: %v", err)
	}
	var status string
	if err := transaction.Table("processing_jobs").Where("id = ?", jobID).Pluck("status", &status).Error; err != nil || status != model.ProcessingStatusFailed {
		t.Fatalf("任务未终止: %s, %v", status, err)
	}
}
