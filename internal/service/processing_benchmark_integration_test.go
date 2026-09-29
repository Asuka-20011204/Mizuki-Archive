package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
)

const (
	v7FixtureCount = 24
	v7FixtureBytes = 128 << 10
)

// TestV7ProcessingWorkload 在空的隔离库中执行固定合成负载；默认跳过，避免污染普通测试和个人资料。
func TestV7ProcessingWorkload(t *testing.T) {
	if os.Getenv("MIZUKI_RUN_BENCHMARK") != "1" {
		t.Skip("显式设置 MIZUKI_RUN_BENCHMARK=1 才运行负载测试")
	}
	config, err := driver.ParseDSN(os.Getenv("MIZUKI_TEST_MYSQL_DSN"))
	if err != nil || !strings.HasPrefix(config.DBName, "mizuki_test_v7_") {
		t.Fatal("负载测试仅允许独立 mizuki_test_v7_ 前缀数据库")
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
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	store := repository.NewMySQL(database)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var existing int64
	if err := database.Table("resources").Count(&existing).Error; err != nil || existing != 0 {
		t.Fatalf("负载测试库必须为空，现有资料=%d，错误=%v", existing, err)
	}
	for _, workers := range []int{1, 2} {
		t.Run(fmt.Sprintf("workers_%d", workers), func(t *testing.T) {
			processor, jobIDs := seedV7ProcessingFixture(t, ctx, store, database)
			measureV7Processing(t, ctx, database, processor, jobIDs, workers)
		})
	}
}

// seedV7ProcessingFixture 准备每组完全相同的文本长度和数量，仅使用随机内部 ID 隔离持久记录。
func seedV7ProcessingFixture(t *testing.T, ctx context.Context, store *repository.MySQL, database *gorm.DB) (*Processing, []string) {
	t.Helper()
	dataDir := t.TempDir()
	processor, err := NewProcessing(store, store, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte(strings.Repeat("mizuki benchmark text. ", v7FixtureBytes/23+1)[:v7FixtureBytes])
	resourceIDs := make([]string, 0, v7FixtureCount)
	jobIDs := make([]string, 0, v7FixtureCount)
	t.Cleanup(func() {
		if len(resourceIDs) != 0 {
			_ = database.Exec("DELETE FROM resources WHERE id IN ?", resourceIDs).Error
		}
	})
	for index := 0; index < v7FixtureCount; index++ {
		idBytes := make([]byte, 16)
		if _, err := rand.Read(idBytes); err != nil {
			t.Fatal(err)
		}
		resourceID := hex.EncodeToString(idBytes)
		if err := os.WriteFile(filepath.Join(dataDir, resourceID), content, 0o600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(content)
		resource := model.Resource{ID: resourceID, Name: fmt.Sprintf("v7-%02d.txt", index), OriginalName: "synthetic.txt", Kind: "text", MIME: "text/plain", Size: int64(len(content)), StorageKey: resourceID, SHA256: hex.EncodeToString(digest[:]), CreatedAt: time.Now().UTC()}
		if err := store.SaveResource(ctx, resource); err != nil {
			t.Fatal(err)
		}
		resourceIDs = append(resourceIDs, resourceID)
		job, err := processor.CreateTextJob(ctx, resourceID)
		if err != nil {
			t.Fatal(err)
		}
		jobIDs = append(jobIDs, job.ID)
	}
	return processor, jobIDs
}

// measureV7Processing 只计 Worker 从满积压到全部成功的时间，抽样进程堆峰值与完成数。
func measureV7Processing(t *testing.T, ctx context.Context, database *gorm.DB, processor *Processing, jobIDs []string, workers int) {
	t.Helper()
	runtime.GC()
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	baselineAllocated := memory.TotalAlloc
	peakHeap := memory.HeapAlloc
	poolCtx, stop := context.WithCancel(ctx)
	finished := make(chan error, 1)
	consumed := false
	started := time.Now()
	go func() { finished <- processor.RunPool(poolCtx, workers, time.Second) }()
	defer func() {
		stop()
		if !consumed {
			<-finished
		}
	}()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("负载超时或任务未完成")
		case err := <-finished:
			consumed = true
			t.Fatalf("Worker 未完成任务即退出: %v", err)
		case <-ticker.C:
			runtime.ReadMemStats(&memory)
			if memory.HeapAlloc > peakHeap {
				peakHeap = memory.HeapAlloc
			}
			var completed int64
			if err := database.Table("processing_jobs").Where("id IN ? AND status = ?", jobIDs, model.ProcessingStatusSucceeded).Count(&completed).Error; err != nil {
				t.Fatal(err)
			}
			if completed != int64(len(jobIDs)) {
				continue
			}
			elapsed := time.Since(started)
			t.Logf("V7 workers=%d jobs=%d bytes_each=%d elapsed_ms=%d throughput_jobs_s=%.2f peak_heap_mib=%.2f allocated_mib=%.2f", workers, len(jobIDs), v7FixtureBytes, elapsed.Milliseconds(), float64(len(jobIDs))/elapsed.Seconds(), float64(peakHeap)/(1<<20), float64(memory.TotalAlloc-baselineAllocated)/(1<<20))
			stop()
			err := <-finished
			consumed = true
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Worker 退出异常: %v", err)
			}
			return
		}
	}
}
