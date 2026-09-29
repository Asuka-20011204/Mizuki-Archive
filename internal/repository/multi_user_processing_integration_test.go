package repository

import (
	"context"
	"errors"
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

// openProcessingIsolationDatabase 打开显式指定的 mizuki_test_ 隔离库，避免测试误连开发数据库。
func openProcessingIsolationDatabase(t *testing.T) (*gorm.DB, *MySQL) {
	t.Helper()
	dsn := os.Getenv("MIZUKI_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("设置 MIZUKI_TEST_MYSQL_DSN 指向隔离测试库后运行")
	}
	config, err := driver.ParseDSN(dsn)
	if err != nil || !strings.HasPrefix(config.DBName, "mizuki_test_") {
		t.Fatal("测试 DSN 必须指向 mizuki_test_ 前缀的独立数据库")
	}
	config.ParseTime = true
	config.Loc = time.UTC
	database, err := gorm.Open(mysql.Open(config.FormatDSN()), &gorm.Config{})
	if err != nil {
		t.Fatalf("连接测试数据库失败: %v", err)
	}
	sqlDatabase, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDatabase.Close() })
	store := NewMySQL(database)
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("建立测试表失败: %v", err)
	}
	return database, store
}

// mustProcessingIsolationID 为每个隔离资料和任务生成不会与已有测试数据冲突的 ID。
func mustProcessingIsolationID(t *testing.T) string {
	t.Helper()
	id, err := newProcessingID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// TestMySQLProcessingOwnershipIsolation 验证真实 GORM 查询会沿资源归属链隔离任务和派生产物。
func TestMySQLProcessingOwnershipIsolation(t *testing.T) {
	database, store := openProcessingIsolationDatabase(t)
	baseContext := context.Background()
	userAContext := WithUserID(baseContext, "user-a")
	userBContext := WithUserID(baseContext, "user-b")
	now := time.Now().UTC()
	resourceA := model.Resource{ID: mustProcessingIsolationID(t), OwnerID: "user-a", Name: "a.txt", OriginalName: "a.txt", Kind: "text", MIME: "text/plain", StorageKey: mustProcessingIsolationID(t), SHA256: strings.Repeat("a", 64), CreatedAt: now}
	resourceB := model.Resource{ID: mustProcessingIsolationID(t), OwnerID: "user-b", Name: "b.txt", OriginalName: "b.txt", Kind: "text", MIME: "text/plain", StorageKey: mustProcessingIsolationID(t), SHA256: strings.Repeat("b", 64), CreatedAt: now}
	if err := store.SaveResource(userAContext, resourceA); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveResource(userBContext, resourceB); err != nil {
		t.Fatal(err)
	}
	jobA := model.ProcessingJob{ID: mustProcessingIsolationID(t), ResourceID: resourceA.ID, Type: model.ProcessingTypeExtractText, SourceSHA256: resourceA.SHA256, Status: model.ProcessingStatusPending, MaxAttempts: 3, AvailableAt: now, CreatedAt: now}
	jobB := model.ProcessingJob{ID: mustProcessingIsolationID(t), ResourceID: resourceB.ID, Type: model.ProcessingTypeExtractText, SourceSHA256: resourceB.SHA256, Status: model.ProcessingStatusPending, MaxAttempts: 3, AvailableAt: now, CreatedAt: now}
	assetID := mustProcessingIsolationID(t)
	t.Cleanup(func() {
		_ = database.Exec("DELETE FROM derived_assets WHERE id = ?", assetID).Error
		_ = database.Exec("DELETE FROM processing_jobs WHERE id IN (?, ?)", jobA.ID, jobB.ID).Error
		_ = database.Exec("DELETE FROM resources WHERE id IN (?, ?)", resourceA.ID, resourceB.ID).Error
	})
	if err := store.CreateProcessingJob(userAContext, jobA); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateProcessingJob(userBContext, jobB); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetProcessingJob(userAContext, jobB.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("user A read user B job: %v", err)
	}
	if jobs, err := store.ListProcessingJobs(userAContext, resourceB.ID); err != nil || len(jobs) != 0 {
		t.Fatalf("user A listed user B jobs: jobs=%#v err=%v", jobs, err)
	}
	if _, err := store.FindReusableProcessingJob(userAContext, resourceB.ID, jobB.Type, jobB.SourceSHA256); !errors.Is(err, ErrNotFound) {
		t.Fatalf("user A reused user B job: %v", err)
	}
	claimed, err := store.ClaimProcessingJob(baseContext, jobB.ID, now)
	if err != nil {
		t.Fatalf("claim user B job: %v", err)
	}
	asset := model.DerivedAsset{ID: assetID, JobID: jobB.ID, ResourceID: resourceB.ID, Kind: model.DerivedAssetText, Name: "b.txt.extracted.txt", StorageKey: mustProcessingIsolationID(t), MIME: "text/plain", Size: 12, SHA256: strings.Repeat("c", 64), ContentText: "private user b", CreatedAt: now}
	if err := store.CompleteProcessingJob(baseContext, jobB.ID, claimed.LeaseToken, asset); err != nil {
		t.Fatalf("complete user B job: %v", err)
	}
	if _, err := store.GetProcessingJob(userAContext, jobB.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("user A read succeeded user B job: %v", err)
	}
	if _, err := store.GetDerivedAsset(userAContext, asset.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("user A read user B asset: %v", err)
	}
	loadedJob, err := store.GetProcessingJob(userBContext, jobB.ID)
	if err != nil || loadedJob.Asset == nil || loadedJob.Asset.ID != asset.ID {
		t.Fatalf("user B could not read own asset: job=%#v err=%v", loadedJob, err)
	}
	if _, err := store.GetProcessingJob(userBContext, jobA.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("user B read user A job: %v", err)
	}
}

// TestMySQLEmailChallengeConcurrentConsumption 验证真实数据库行锁保证同一验证码并发最多成功消费一次。
func TestMySQLEmailChallengeConcurrentConsumption(t *testing.T) {
	_, store := openProcessingIsolationDatabase(t)
	now := time.Now().UTC()
	challenge := model.EmailChallenge{Email: "race@example.com", Purpose: "register", Digest: strings.Repeat("f", 64), ExpiresAt: now.Add(time.Minute), NextRequestAt: now}
	reserved, err := store.ReserveEmailChallenge(context.Background(), challenge, now)
	if err != nil || !reserved {
		t.Fatalf("reserve challenge failed: reserved=%t err=%v", reserved, err)
	}
	results := make(chan bool, 2)
	errorsFound := make(chan error, 2)
	var waitGroup sync.WaitGroup
	waitGroup.Add(2)
	for range 2 {
		go func() {
			defer waitGroup.Done()
			accepted, consumeErr := store.ConsumeEmailChallenge(context.Background(), challenge.Email, challenge.Purpose, challenge.Digest, now)
			results <- accepted
			errorsFound <- consumeErr
		}()
	}
	waitGroup.Wait()
	close(results)
	close(errorsFound)
	acceptedCount := 0
	for accepted := range results {
		if accepted {
			acceptedCount++
		}
	}
	for consumeErr := range errorsFound {
		if consumeErr != nil {
			t.Fatal(consumeErr)
		}
	}
	if acceptedCount != 1 {
		t.Fatalf("concurrent challenge accepted %d times, want exactly once", acceptedCount)
	}
}

// TestMySQLFinalizeOwnershipBackfillsLegacyRows 验证旧资料和旧会话的空归属会被管理员用户一次性接管。
func TestMySQLFinalizeOwnershipBackfillsLegacyRows(t *testing.T) {
	database, store := openProcessingIsolationDatabase(t)
	legacyResourceID := mustProcessingIsolationID(t)
	legacyTokenHash := strings.Repeat("d", 64)
	now := time.Now().UTC()
	if err := database.Exec(`INSERT INTO resources (id, user_id, name, original_name, kind, mime, size_bytes, sha256, storage_key, favorite, created_at) VALUES (?, NULL, ?, ?, ?, ?, ?, ?, ?, FALSE, ?)`, legacyResourceID, "legacy.txt", "legacy.txt", "text", "text/plain", 6, strings.Repeat("e", 64), legacyResourceID, now).Error; err != nil {
		t.Fatalf("insert legacy resource: %v", err)
	}
	if err := database.Exec(`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (?, NULL, ?)`, legacyTokenHash, now.Add(time.Hour)).Error; err != nil {
		t.Fatalf("insert legacy session: %v", err)
	}
	admin, err := store.EnsureAdminUser(context.Background(), "legacy-"+legacyResourceID[:8], []byte("test-password-hash"))
	if err != nil {
		t.Fatalf("create migration user: %v", err)
	}
	if err := store.FinalizeOwnership(context.Background(), admin.ID); err != nil {
		t.Fatalf("finalize ownership: %v", err)
	}
	var resourceOwner string
	if err := database.Table("resources").Where("id = ?", legacyResourceID).Pluck("user_id", &resourceOwner).Error; err != nil {
		t.Fatal(err)
	}
	if resourceOwner != admin.ID {
		t.Fatalf("legacy resource owner = %q, want %q", resourceOwner, admin.ID)
	}
	var sessionOwner string
	if err := database.Table("sessions").Where("token_hash = ?", legacyTokenHash).Pluck("user_id", &sessionOwner).Error; err != nil {
		t.Fatal(err)
	}
	if sessionOwner != admin.ID {
		t.Fatalf("legacy session owner = %q, want %q", sessionOwner, admin.ID)
	}
	if err := store.FinalizeOwnership(context.Background(), admin.ID); err != nil {
		t.Fatalf("repeat ownership finalization: %v", err)
	}
}
