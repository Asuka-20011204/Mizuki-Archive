package service

import (
	"bytes"
	"context"
	"errors"
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

// ClaimNextTextProcessingJob 复用假仓储的领取逻辑，满足多槽位 Worker 的类型领取能力契约。
func (store *processingFakeStore) ClaimNextTextProcessingJob(ctx context.Context, now time.Time) (model.ProcessingJob, error) {
	return store.ClaimNextProcessingJob(ctx, now)
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

// blockingClaimStore 用可取消的领取阻塞点观测同时工作的数据库 Worker 数量。
type blockingClaimStore struct {
	repository.ProcessingStore
	entered chan struct{}
}

// completionSignalStore 在真实任务完成后发信号，避免测试在另一个协程读取非并发安全的假仓储。
type completionSignalStore struct {
	*processingFakeStore
	completed chan struct{}
}

// CompleteProcessingJob 转发最终提交并告知测试，无需读取正在变化的任务映射。
func (store *completionSignalStore) CompleteProcessingJob(ctx context.Context, id, token string, asset model.DerivedAsset) error {
	if err := store.processingFakeStore.CompleteProcessingJob(ctx, id, token, asset); err != nil {
		return err
	}
	store.completed <- struct{}{}
	return nil
}

// TestRunLoopDrainsBacklogWithoutIdlePause 验证有积压时连续处理，间隔只用于空队列轮询。
func TestRunLoopDrainsBacklogWithoutIdlePause(t *testing.T) {
	resourceStore := newFakeStore()
	dataDir := t.TempDir()
	jobs := &completionSignalStore{processingFakeStore: &processingFakeStore{jobs: map[string]model.ProcessingJob{}, assets: map[string]model.DerivedAsset{}, resources: resourceStore.resources}, completed: make(chan struct{}, 2)}
	for _, id := range []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"} {
		if err := os.WriteFile(filepath.Join(dataDir, id), []byte("synthetic text"), 0o600); err != nil {
			t.Fatal(err)
		}
		resourceStore.resources[id] = model.Resource{ID: id, Name: "test.txt", Kind: "text", StorageKey: id, SHA256: "fixture-hash"}
		jobs.jobs[id] = model.ProcessingJob{ID: id, ResourceID: id, Type: model.ProcessingTypeExtractText, SourceSHA256: "fixture-hash", Status: model.ProcessingStatusPending, MaxAttempts: 3}
	}
	processor, err := NewProcessing(resourceStore, jobs, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- processor.RunLoop(ctx, 2*time.Second) }()
	defer func() { stop(); <-finished }()
	for count := 0; count < 2; count++ {
		select {
		case <-jobs.completed:
		case <-time.After(time.Second):
			t.Fatal("有积压时不应等待完整轮询间隔")
		}
	}
}

// ClaimNextProcessingJob 只报告领取尝试，不修改共享测试数据。
func (store *blockingClaimStore) ClaimNextProcessingJob(ctx context.Context, _ time.Time) (model.ProcessingJob, error) {
	select {
	case store.entered <- struct{}{}:
	case <-ctx.Done():
		return model.ProcessingJob{}, ctx.Err()
	}
	<-ctx.Done()
	return model.ProcessingJob{}, ctx.Err()
}

// ClaimNextTextProcessingJob 让测试替身显式声明支持图片安全领取，避免测试绕过生产门禁。
func (store *blockingClaimStore) ClaimNextTextProcessingJob(ctx context.Context, now time.Time) (model.ProcessingJob, error) {
	return store.ClaimNextProcessingJob(ctx, now)
}

// TestRunPoolBoundedAndCancelable 验证池大小限制同时领取数且取消后所有 Worker 退出。
func TestRunPoolBoundedAndCancelable(t *testing.T) {
	store := &blockingClaimStore{entered: make(chan struct{}, 4)}
	processor := &Processing{jobs: store}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- processor.RunPool(ctx, 2, time.Millisecond) }()
	for count := 0; count < 2; count++ {
		select {
		case <-store.entered:
		case <-ctx.Done():
			t.Fatal("两个 Worker 未同时领取")
		}
	}
	select {
	case <-store.entered:
		t.Fatal("Worker 数量超出配置上限")
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("退出错误 = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("取消后 Worker 未退出")
	}
}

// imageClaimStore 记录通用领取和纯文本领取的调用，模拟数据库按类型分流。
type imageClaimStore struct {
	repository.ProcessingStore
	allClaims  chan struct{}
	textClaims chan struct{}
}

// ClaimNextProcessingJob 表示允许领取图片的唯一槽位。
func (store *imageClaimStore) ClaimNextProcessingJob(ctx context.Context, _ time.Time) (model.ProcessingJob, error) {
	store.allClaims <- struct{}{}
	<-ctx.Done()
	return model.ProcessingJob{}, ctx.Err()
}

// ClaimNextTextProcessingJob 表示其余槽位只领取文本任务。
func (store *imageClaimStore) ClaimNextTextProcessingJob(ctx context.Context, _ time.Time) (model.ProcessingJob, error) {
	store.textClaims <- struct{}{}
	<-ctx.Done()
	return model.ProcessingJob{}, ctx.Err()
}

// TestRunPoolLimitsImageEligibleClaims 验证四槽位中只有一个能领取图片，取消后全部退出。
func TestRunPoolLimitsImageEligibleClaims(t *testing.T) {
	store := &imageClaimStore{allClaims: make(chan struct{}, 4), textClaims: make(chan struct{}, 4)}
	processor := &Processing{jobs: store}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- processor.RunPool(ctx, 4, time.Millisecond) }()
	var imageClaims, textClaims int
	for imageClaims+textClaims < 4 {
		select {
		case <-store.allClaims:
			imageClaims++
		case <-store.textClaims:
			textClaims++
		case <-time.After(time.Second):
			t.Fatal("Worker 未按时领取任务")
		}
	}
	if imageClaims != 1 || textClaims != 3 {
		t.Fatalf("通用领取=%d，纯文本领取=%d", imageClaims, textClaims)
	}
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("退出错误 = %v", err)
	}
}

// blockingImageJobStore 让第一条图片消息停在领取前，观察第二条消息是否抢占租约。
type blockingImageJobStore struct {
	repository.ProcessingStore
	entered chan struct{}
	release chan struct{}
	claims  chan struct{}
}

// GetProcessingJob 提供待处理图片任务的类型，供消息消费门禁读取。
func (store *blockingImageJobStore) GetProcessingJob(_ context.Context, id string) (model.ProcessingJob, error) {
	return model.ProcessingJob{ID: id, Type: model.ProcessingTypeGenerateThumbnail, Status: model.ProcessingStatusPending}, nil
}

// ClaimProcessingJob 记录真正的数据库领取次数，并等待测试放行。
func (store *blockingImageJobStore) ClaimProcessingJob(ctx context.Context, _ string, _ time.Time) (model.ProcessingJob, error) {
	store.claims <- struct{}{}
	close(store.entered)
	select {
	case <-store.release:
		return model.ProcessingJob{}, repository.ErrNoPendingJob
	case <-ctx.Done():
		return model.ProcessingJob{}, ctx.Err()
	}
}

// TestRabbitImageGateBeforeClaim 验证繁忙图片消息没有消耗数据库尝试次数，供 Broker 延迟重排。
func TestRabbitImageGateBeforeClaim(t *testing.T) {
	store := &blockingImageJobStore{entered: make(chan struct{}), release: make(chan struct{}), claims: make(chan struct{}, 2)}
	processor, err := NewProcessing(newFakeStore(), store, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first := make(chan error, 1)
	go func() { first <- processor.RunJob(context.Background(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") }()
	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("第一条消息未进入领取阶段")
	}
	if err := processor.RunJob(context.Background(), "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"); !errors.Is(err, ErrJobNotReady) {
		t.Fatalf("图片槽位繁忙时应等待 Broker 重排，得到 %v", err)
	}
	if len(store.claims) != 1 {
		t.Fatalf("图片领取次数 = %d", len(store.claims))
	}
	close(store.release)
	if err := <-first; !errors.Is(err, ErrJobNotReady) {
		t.Fatalf("第一条模拟租约状态 = %v", err)
	}
}
