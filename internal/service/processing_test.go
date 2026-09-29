package service

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
)

// processingFakeStore 模拟持久任务状态，测试 Service 时不依赖 MySQL 锁语义。
type processingFakeStore struct {
	jobs      map[string]model.ProcessingJob
	assets    map[string]model.DerivedAsset
	resources map[string]model.Resource
}

// FindReusableProcessingJob 返回同一来源的可复用任务，模拟数据库幂等查询。
func (store *processingFakeStore) FindReusableProcessingJob(_ context.Context, resourceID, jobType, sourceSHA256 string) (model.ProcessingJob, error) {
	for _, job := range store.jobs {
		if job.ResourceID == resourceID && job.Type == jobType && job.SourceSHA256 == sourceSHA256 && job.Status != model.ProcessingStatusFailed {
			if asset, ok := store.assets[job.ID]; ok {
				job.Asset = &asset
			}
			return job, nil
		}
	}
	return model.ProcessingJob{}, repository.ErrNotFound
}

// CreateProcessingJob 保存新任务并保留初始状态。
func (store *processingFakeStore) CreateProcessingJob(_ context.Context, job model.ProcessingJob) error {
	store.jobs[job.ID] = job
	return nil
}

// GetProcessingJob 返回任务详情和已生成资产。
func (store *processingFakeStore) GetProcessingJob(_ context.Context, id string) (model.ProcessingJob, error) {
	job, ok := store.jobs[id]
	if !ok {
		return model.ProcessingJob{}, repository.ErrNotFound
	}
	if asset, exists := store.assets[id]; exists {
		job.Asset = &asset
	}
	return job, nil
}

// ListProcessingJobs 返回指定资料的任务历史。
func (store *processingFakeStore) ListProcessingJobs(_ context.Context, resourceID string) ([]model.ProcessingJob, error) {
	result := make([]model.ProcessingJob, 0)
	for _, job := range store.jobs {
		if job.ResourceID == resourceID {
			result = append(result, job)
		}
	}
	return result, nil
}

// ClaimNextProcessingJob 领取第一个待处理任务并写入测试租约令牌。
func (store *processingFakeStore) ClaimNextProcessingJob(_ context.Context, _ time.Time) (model.ProcessingJob, error) {
	for id, job := range store.jobs {
		if job.Status != model.ProcessingStatusPending {
			continue
		}
		job.Status = model.ProcessingStatusProcessing
		job.Attempts++
		job.LeaseToken = "lease-token"
		store.jobs[id] = job
		return job, nil
	}
	return model.ProcessingJob{}, repository.ErrNoPendingJob
}

// CompleteProcessingJob 保存资产并将任务置为成功，模拟数据库事务的最终结果。
func (store *processingFakeStore) CompleteProcessingJob(_ context.Context, id, leaseToken string, asset model.DerivedAsset) error {
	job, ok := store.jobs[id]
	if !ok || job.LeaseToken != leaseToken || job.Status != model.ProcessingStatusProcessing {
		return repository.ErrJobLeaseLost
	}
	job.Status = model.ProcessingStatusSucceeded
	job.Asset = &asset
	store.jobs[id] = job
	store.assets[asset.ID] = asset
	return nil
}

// FailProcessingJob 将任务置为失败，测试永久失败不会无限重试。
func (store *processingFakeStore) FailProcessingJob(_ context.Context, id, leaseToken, message string, _ *time.Time) error {
	job, ok := store.jobs[id]
	if !ok || job.LeaseToken != leaseToken {
		return repository.ErrJobLeaseLost
	}
	job.Status = model.ProcessingStatusFailed
	job.LastError = message
	store.jobs[id] = job
	return nil
}

// GetDerivedAsset 返回已经保存的派生产物元数据。
func (store *processingFakeStore) GetDerivedAsset(_ context.Context, id string) (model.DerivedAsset, error) {
	asset, ok := store.assets[id]
	if !ok {
		return model.DerivedAsset{}, repository.ErrNotFound
	}
	return asset, nil
}

// TestCreateTextJobIsIdempotent 验证同一资料重复点击只得到同一待处理任务。
func TestCreateTextJobIsIdempotent(t *testing.T) {
	store := newFakeStore()
	resource := model.Resource{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Kind: "text", SHA256: "source-hash", StorageKey: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Name: "note.txt"}
	store.resources[resource.ID] = resource
	jobs := &processingFakeStore{jobs: map[string]model.ProcessingJob{}, assets: map[string]model.DerivedAsset{}, resources: store.resources}
	processor, err := NewProcessing(store, jobs, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first, err := processor.CreateTextJob(context.Background(), resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := processor.CreateTextJob(context.Background(), resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || len(jobs.jobs) != 1 {
		t.Fatalf("jobs=%d first=%s second=%s", len(jobs.jobs), first.ID, second.ID)
	}
}

// TestRunOnceExtractsTextAndStoresDerivedAsset 验证 Worker 一次循环会生成独立文本产物并完成任务。
func TestRunOnceExtractsTextAndStoresDerivedAsset(t *testing.T) {
	dataDir := t.TempDir()
	resourceID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	resource := model.Resource{ID: resourceID, Kind: "markdown", Name: "memo.md", StorageKey: resourceID, SHA256: "source-hash"}
	if err := os.WriteFile(filepath.Join(dataDir, resourceID), []byte("# hello\nkeyword"), 0o600); err != nil {
		t.Fatal(err)
	}
	resourceStore := newFakeStore()
	resourceStore.resources[resourceID] = resource
	jobs := &processingFakeStore{jobs: map[string]model.ProcessingJob{}, assets: map[string]model.DerivedAsset{}, resources: resourceStore.resources}
	job := model.ProcessingJob{ID: "cccccccccccccccccccccccccccccccc", ResourceID: resourceID, Type: model.ProcessingTypeExtractText, SourceSHA256: resource.SHA256, Status: model.ProcessingStatusPending, MaxAttempts: 3, AvailableAt: time.Now().UTC(), CreatedAt: time.Now().UTC()}
	jobs.jobs[job.ID] = job
	processor, err := NewProcessing(resourceStore, jobs, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := processor.RunOnce(context.Background())
	if err != nil || !claimed {
		t.Fatalf("claimed=%t error=%v", claimed, err)
	}
	completed, err := jobs.GetProcessingJob(context.Background(), job.ID)
	if err != nil || completed.Status != model.ProcessingStatusSucceeded || completed.Asset == nil {
		t.Fatalf("completed=%#v error=%v", completed, err)
	}
	assetFile, err := processor.OpenAsset(*completed.Asset)
	if err != nil {
		t.Fatal(err)
	}
	defer assetFile.Close()
	content, err := os.ReadFile(filepath.Join(dataDir, "derived", completed.Asset.ID))
	if err != nil || string(content) != "# hello\nkeyword" {
		t.Fatalf("asset content=%q error=%v", content, err)
	}
}

// TestRunOnceGeneratesThumbnailAndStoresDerivedAsset 验证图片任务沿用同一租约流程生成 PNG 派生产物。
func TestRunOnceGeneratesThumbnailAndStoresDerivedAsset(t *testing.T) {
	dataDir := t.TempDir()
	resourceID := "ffffffffffffffffffffffffffffffff"
	resource := model.Resource{ID: resourceID, Kind: "image", Name: "cover.png", StorageKey: resourceID, SHA256: "image-hash"}
	imageFile, err := os.Create(filepath.Join(dataDir, resourceID))
	if err != nil {
		t.Fatal(err)
	}
	source := image.NewRGBA(image.Rect(0, 0, 1200, 600))
	for y := 0; y < source.Bounds().Dy(); y++ {
		for x := 0; x < source.Bounds().Dx(); x++ {
			source.SetRGBA(x, y, color.RGBA{R: 40, G: uint8(x % 255), B: uint8(y % 255), A: 255})
		}
	}
	if err := png.Encode(imageFile, source); err != nil {
		_ = imageFile.Close()
		t.Fatal(err)
	}
	if err := imageFile.Close(); err != nil {
		t.Fatal(err)
	}

	resourceStore := newFakeStore()
	resourceStore.resources[resourceID] = resource
	jobs := &processingFakeStore{jobs: map[string]model.ProcessingJob{}, assets: map[string]model.DerivedAsset{}, resources: resourceStore.resources}
	job := model.ProcessingJob{ID: "11111111111111111111111111111111", ResourceID: resourceID, Type: model.ProcessingTypeGenerateThumbnail, SourceSHA256: resource.SHA256, Status: model.ProcessingStatusPending, MaxAttempts: 3, AvailableAt: time.Now().UTC(), CreatedAt: time.Now().UTC()}
	jobs.jobs[job.ID] = job
	processor, err := NewProcessing(resourceStore, jobs, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := processor.RunOnce(context.Background()); err != nil || !claimed {
		t.Fatalf("claimed=%t error=%v", claimed, err)
	}
	completed, err := jobs.GetProcessingJob(context.Background(), job.ID)
	if err != nil || completed.Status != model.ProcessingStatusSucceeded || completed.Asset == nil {
		t.Fatalf("completed=%#v error=%v", completed, err)
	}
	if completed.Asset.Kind != model.DerivedAssetThumbnail || completed.Asset.MIME != "image/png" {
		t.Fatalf("asset=%#v", completed.Asset)
	}
	content, err := os.ReadFile(filepath.Join(dataDir, "derived", completed.Asset.ID))
	if err != nil {
		t.Fatal(err)
	}
	decoded, _, err := image.Decode(bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Bounds().Dx() != 640 || decoded.Bounds().Dy() != 320 {
		t.Fatalf("thumbnail size = %v", decoded.Bounds())
	}
}

// TestRunOnceMarksUnsupportedResourceFailed 验证不支持的来源不会被 Worker 反复领取。
func TestRunOnceMarksUnsupportedResourceFailed(t *testing.T) {
	resourceStore := newFakeStore()
	resourceID := "dddddddddddddddddddddddddddddddd"
	resourceStore.resources[resourceID] = model.Resource{ID: resourceID, Kind: "image", StorageKey: resourceID, SHA256: "image-hash"}
	jobs := &processingFakeStore{jobs: map[string]model.ProcessingJob{}, assets: map[string]model.DerivedAsset{}, resources: resourceStore.resources}
	job := model.ProcessingJob{ID: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", ResourceID: resourceID, Type: model.ProcessingTypeExtractText, SourceSHA256: "image-hash", Status: model.ProcessingStatusPending, MaxAttempts: 3, AvailableAt: time.Now().UTC(), CreatedAt: time.Now().UTC()}
	jobs.jobs[job.ID] = job
	processor, err := NewProcessing(resourceStore, jobs, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := processor.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if jobs.jobs[job.ID].Status != model.ProcessingStatusFailed || jobs.jobs[job.ID].LastError == "" {
		t.Fatalf("failed job=%#v", jobs.jobs[job.ID])
	}
}
