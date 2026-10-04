package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

// TestMySQLSetFavorite 仅在显式提供隔离测试库时运行，验证 GORM 对重复布尔更新和缺失资料的真实行为。
func TestMySQLSetFavorite(t *testing.T) {
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
	connection, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	// 清理回调只关闭本用例创建的连接，不删除测试库或其他资料。
	t.Cleanup(func() { _ = connection.Close() })
	ctx := context.Background()
	if err := NewMySQL(database).Migrate(ctx); err != nil {
		t.Fatalf("建立测试表失败: %v", err)
	}
	if err := NewMySQL(database).Migrate(ctx); err != nil {
		t.Fatalf("重复执行迁移失败: %v", err)
	}
	var migrationCount int64
	if err := database.Table("schema_migrations").Count(&migrationCount).Error; err != nil || migrationCount != 21 {
		t.Fatalf("迁移记录数量 = %d, error=%v", migrationCount, err)
	}
	transaction := database.Begin()
	if transaction.Error != nil {
		t.Fatal(transaction.Error)
	}
	// 清理回调撤销本用例插入的资料，测试表保留给后续集成测试复用。
	t.Cleanup(func() { _ = transaction.Rollback().Error })
	store := NewMySQL(transaction)
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		t.Fatal(err)
	}
	id := hex.EncodeToString(idBytes)
	resource := model.Resource{ID: id, Name: "example.txt", OriginalName: "example.txt", Kind: "text", MIME: "text/plain", StorageKey: id, SHA256: strings.Repeat("0", 64), CreatedAt: time.Now().UTC()}
	if err := store.SaveResource(ctx, resource); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetResource(ctx, id)
	if err != nil || loaded.Tags == nil || len(loaded.Tags) != 0 {
		t.Fatalf("无标签资料必须返回空数组，得到 %#v, %v", loaded.Tags, err)
	}
	for _, favorite := range []bool{true, true, false} {
		updated, err := store.SetFavorite(ctx, id, favorite)
		if err != nil || updated.Favorite != favorite {
			t.Fatalf("设置 favorite=%t 得到 %#v, %v", favorite, updated, err)
		}
	}
	if _, err := store.SetFavorite(ctx, strings.Repeat("f", 32), true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("缺失资料应返回 ErrNotFound，得到 %v", err)
	}
	updated, err := store.ReplaceResourceTags(ctx, id, []string{"go", "redis"})
	if err != nil || strings.Join(updated.Tags, ",") != "go,redis" {
		t.Fatalf("保存标签得到 %#v, %v", updated.Tags, err)
	}
	filtered, err := store.ListResources(ctx, model.ListQuery{Tag: "redis", Limit: 10})
	if err != nil || len(filtered) != 1 || filtered[0].ID != id {
		t.Fatalf("标签筛选得到 %#v, %v", filtered, err)
	}
	cleared, err := store.ReplaceResourceTags(ctx, id, nil)
	if err != nil || len(cleared.Tags) != 0 {
		t.Fatalf("清空标签得到 %#v, %v", cleared.Tags, err)
	}
	renamed, err := store.UpdateResourceName(ctx, id, "renamed.txt")
	if err != nil || renamed.Name != "renamed.txt" || renamed.OriginalName != "example.txt" {
		t.Fatalf("修改名称得到 %#v, %v", renamed, err)
	}
	if _, err := store.DeleteResource(ctx, id); err != nil {
		t.Fatalf("软删除失败: %v", err)
	}
	if _, err := store.DeleteResource(ctx, id); err != nil {
		t.Fatalf("重复软删除应可用于重试文件清理: %v", err)
	}
	if _, err := store.GetResource(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("软删除后仍可读取: %v", err)
	}
	resources, err := store.ListResources(ctx, model.ListQuery{Limit: 30})
	if err != nil {
		t.Fatalf("软删除后读取列表失败: %v", err)
	}
	for _, listed := range resources {
		if listed.ID == id {
			t.Fatalf("软删除资料仍在列表: %#v", listed)
		}
	}
}

// TestMySQLTextClaimSkipsImage 验证真实 MySQL 在加锁领取前排除图片，图片保留给通用槽位。
func TestMySQLTextClaimSkipsImage(t *testing.T) {
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
		t.Fatal("连接隔离库失败")
	}
	connection, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := NewMySQL(database)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, 2)
	t.Cleanup(func() { _ = database.Exec("DELETE FROM resources WHERE id IN ?", ids).Error })
	for _, kind := range []string{"image", "text"} {
		resourceID, idErr := newProcessingID()
		if idErr != nil {
			t.Fatal(idErr)
		}
		jobID, idErr := newProcessingID()
		if idErr != nil {
			t.Fatal(idErr)
		}
		resource := model.Resource{ID: resourceID, Name: kind, OriginalName: kind, Kind: kind, MIME: "application/octet-stream", StorageKey: resourceID, SHA256: strings.Repeat("a", 64), CreatedAt: time.Now().UTC()}
		if err := store.SaveResource(ctx, resource); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, resourceID)
		jobType := model.ProcessingTypeExtractText
		if kind == "image" {
			jobType = model.ProcessingTypeGenerateThumbnail
		}
		job := model.ProcessingJob{ID: jobID, ResourceID: resourceID, Type: jobType, SourceSHA256: resource.SHA256, Status: model.ProcessingStatusPending, MaxAttempts: 3, AvailableAt: time.Now().UTC(), CreatedAt: time.Now().UTC()}
		if err := store.CreateProcessingJob(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	textJob, err := store.ClaimNextTextProcessingJob(ctx, time.Now().UTC())
	if err != nil || textJob.Type != model.ProcessingTypeExtractText {
		t.Fatalf("纯文本槽位领取 = %s, %v", textJob.Type, err)
	}
	imageJob, err := store.ClaimNextProcessingJob(ctx, time.Now().UTC())
	if err != nil || imageJob.Type != model.ProcessingTypeGenerateThumbnail {
		t.Fatalf("图片槽位领取 = %s, %v", imageJob.Type, err)
	}
}

// TestMySQLOCRSearchIsPrivate 验证 OCR 正文能检索和持久化，但不能被其他账号读取。
func TestMySQLOCRSearchIsPrivate(t *testing.T) {
	dsn := os.Getenv("MIZUKI_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("仅在设置隔离测试库 DSN 时运行")
	}
	config, err := driver.ParseDSN(dsn)
	if err != nil || !strings.HasPrefix(config.DBName, "mizuki_test_") {
		t.Fatal("测试 DSN 必须指向 mizuki_test_ 前缀的独立数据库")
	}
	config.ParseTime, config.Loc = true, time.UTC
	database, err := gorm.Open(mysql.Open(config.FormatDSN()), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	if err := NewMySQL(database).Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	transaction := database.Begin()
	if transaction.Error != nil {
		t.Fatal(transaction.Error)
	}
	t.Cleanup(func() { _ = transaction.Rollback().Error })
	store := NewMySQL(transaction)
	now := time.Now().UTC()
	ownerID, err := newProcessingID()
	if err != nil {
		t.Fatal(err)
	}
	otherID, err := newProcessingID()
	if err != nil {
		t.Fatal(err)
	}
	for _, userID := range []string{ownerID, otherID} {
		if err := transaction.Table("users").Create(map[string]any{"id": userID, "username": "ocr_" + userID, "created_at": now}).Error; err != nil {
			t.Fatal(err)
		}
	}
	owner := WithUserID(context.Background(), ownerID)
	other := WithUserID(context.Background(), otherID)
	resourceID, err := newProcessingID()
	if err != nil {
		t.Fatal(err)
	}
	resource := model.Resource{ID: resourceID, Name: "scan.png", OriginalName: "scan.png", Kind: "image", MIME: "image/png", StorageKey: resourceID, SHA256: strings.Repeat("a", 64), CreatedAt: now}
	if err := store.SaveResource(owner, resource); err != nil {
		t.Fatal(err)
	}
	jobID, err := newProcessingID()
	if err != nil {
		t.Fatal(err)
	}
	job := model.ProcessingJob{ID: jobID, ResourceID: resourceID, Type: model.ProcessingTypeOCR, SourceSHA256: resource.SHA256, Status: model.ProcessingStatusProcessing, MaxAttempts: 3, AvailableAt: now, CreatedAt: now}
	if err := store.CreateProcessingJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	leaseToken, err := newProcessingID()
	if err != nil {
		t.Fatal(err)
	}
	if err := transaction.Table("processing_jobs").Where("id = ?", jobID).Update("lease_token", leaseToken).Error; err != nil {
		t.Fatal(err)
	}
	assetID, err := newProcessingID()
	if err != nil {
		t.Fatal(err)
	}
	asset := model.DerivedAsset{ID: assetID, JobID: jobID, ResourceID: resourceID, Kind: model.DerivedAssetOCR, Name: "scan.ocr.txt", StorageKey: assetID, MIME: "text/plain; charset=utf-8", Size: 14, SHA256: strings.Repeat("b", 64), ContentText: "SILVERSCAN 123", OCRPages: 2, OCRConfidence: 87.5, CreatedAt: now}
	if err := store.CompleteProcessingJob(context.Background(), jobID, leaseToken, asset); err != nil {
		t.Fatal(err)
	}
	owned, err := store.SearchFiles(owner, model.SearchFilter{Query: "SILVERSCAN"}, 10, 0)
	if err != nil || len(owned) != 1 || owned[0].ID != resourceID {
		t.Fatalf("本人无法检索 OCR 正文: %#v, %v", owned, err)
	}
	foreign, err := store.SearchFiles(other, model.SearchFilter{Query: "SILVERSCAN"}, 10, 0)
	if err != nil || len(foreign) != 0 {
		t.Fatalf("OCR 正文泄漏给其他账号: %#v, %v", foreign, err)
	}
	listed, err := store.ListResources(owner, model.ListQuery{Search: "SILVERSCAN", Limit: 10})
	if err != nil || len(listed) != 1 || listed[0].ID != resourceID {
		t.Fatalf("资料列表未命中 OCR 正文: %#v, %v", listed, err)
	}
	listed, err = store.ListResources(other, model.ListQuery{Search: "SILVERSCAN", Limit: 10})
	if err != nil || len(listed) != 0 {
		t.Fatalf("资料列表泄漏其他账号的 OCR 正文: %#v, %v", listed, err)
	}
	loaded, err := store.GetDerivedAsset(owner, assetID)
	if err != nil || loaded.Kind != model.DerivedAssetOCR || loaded.OCRPages != 2 || loaded.OCRConfidence != 87.5 {
		t.Fatalf("OCR 元数据持久化异常: %#v, %v", loaded, err)
	}
	if _, err := store.GetDerivedAsset(other, assetID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("其他账号读取 OCR 派生产物得到 %v", err)
	}
}
