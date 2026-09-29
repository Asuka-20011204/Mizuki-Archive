package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sync/errgroup"
	"mizuki-archive/internal/cache"
	"mizuki-archive/internal/model"
	"mizuki-archive/internal/processing"
	"mizuki-archive/internal/repository"
)

const (
	// ProcessingMaxAttempts 限制同一个任务的自动重试次数，避免损坏资料无限消耗 Worker。
	ProcessingMaxAttempts = 3
	// ProcessingLeaseDuration 约束单次处理租约；进程崩溃后任务可再次领取。
	ProcessingLeaseDuration = 5 * time.Minute
	// ProcessingExecutionTimeout 防止单个损坏或超大 PDF 长时间占用单进程 Worker。
	ProcessingExecutionTimeout = 2 * time.Minute
	// DefaultProcessingCapacity 限制单个资料库同时等待或执行的任务总数。
	DefaultProcessingCapacity = 100
)

var (
	// ErrProcessingType 表示客户端请求了当前版本未开放的处理器。
	ErrProcessingType = errors.New("unsupported processing type")
	// ErrProcessingResource 表示当前资料格式不能执行所请求的处理。
	ErrProcessingResource = errors.New("resource cannot be processed")
	// ErrQueueNotConfigured 表示当前 Worker 没有可用的消息发布器，调用方应继续使用数据库回退模式。
	ErrQueueNotConfigured = errors.New("processing queue not configured")
	// ErrJobNotReady 表示任务仍处于别的 Worker 租约或退避窗口，消息不可提前确认。
	ErrJobNotReady = errors.New("processing job not ready")
)

// JobPublisher 只暴露发布任务 ID 的能力，Service 不依赖 RabbitMQ 客户端类型。
type JobPublisher interface {
	PublishJob(context.Context, string) error
}

// Processing 负责创建任务、执行 Worker 单次循环以及管理派生文件，不依赖 Gin。
type Processing struct {
	store          repository.Store
	jobs           repository.ProcessingStore
	dataDir        string
	cache          cache.Cache
	outboxEnabled  bool
	maxOutstanding int
}

// NewProcessing 校验任务存储和派生目录；目录不可用时拒绝启动处理功能。
func NewProcessing(store repository.Store, jobs repository.ProcessingStore, dataDir string) (*Processing, error) {
	return NewProcessingWithCache(store, jobs, dataDir, nil)
}

// NewProcessingWithCache 创建处理服务并注入可选 Redis；缓存只保存状态摘要，MySQL 仍是事实源。
func NewProcessingWithCache(store repository.Store, jobs repository.ProcessingStore, dataDir string, processingCache cache.Cache) (*Processing, error) {
	if store == nil || jobs == nil || dataDir == "" {
		return nil, errors.New("invalid processing configuration")
	}
	derivedDir := filepath.Join(dataDir, "derived")
	if err := os.MkdirAll(derivedDir, 0700); err != nil {
		return nil, fmt.Errorf("create derived directory: %w", err)
	}
	return &Processing{store: store, jobs: jobs, dataDir: dataDir, cache: processingCache, maxOutstanding: DefaultProcessingCapacity}, nil
}

// SetMaxOutstanding 在 API 启动时配置全局未完成任务配额，拒绝无界积压或异常配置。
func (service *Processing) SetMaxOutstanding(limit int) error {
	if limit < 1 || limit > 1000 {
		return errors.New("processing capacity must be between 1 and 1000")
	}
	service.maxOutstanding = limit
	return nil
}

// EnableOutbox 仅在服务启动装配 Rabbit 模式时调用；业务开始后不再切换投递方式。
func (service *Processing) EnableOutbox() error {
	if _, ok := service.jobs.(repository.ProcessingOutboxStore); !ok {
		return ErrQueueNotConfigured
	}
	service.outboxEnabled = true
	return nil
}

// CreateTextJob 手动为一份 PDF、TXT 或 Markdown 创建幂等文本提取任务。
func (service *Processing) CreateTextJob(ctx context.Context, resourceID string) (model.ProcessingJob, error) {
	return service.createJob(ctx, resourceID, model.ProcessingTypeExtractText)
}

// CreateThumbnailJob 手动为一份图片创建幂等缩略图任务，原图不会被覆盖。
func (service *Processing) CreateThumbnailJob(ctx context.Context, resourceID string) (model.ProcessingJob, error) {
	return service.createJob(ctx, resourceID, model.ProcessingTypeGenerateThumbnail)
}

// createJob 统一处理任务类型白名单、资源格式校验和重复点击幂等性。
func (service *Processing) createJob(ctx context.Context, resourceID, jobType string) (model.ProcessingJob, error) {
	if !validResourceID(resourceID) {
		return model.ProcessingJob{}, repository.ErrNotFound
	}
	if jobType != model.ProcessingTypeExtractText && jobType != model.ProcessingTypeGenerateThumbnail {
		return model.ProcessingJob{}, ErrProcessingType
	}
	resource, err := service.store.GetResource(ctx, resourceID)
	if err != nil {
		return model.ProcessingJob{}, err
	}
	if !isProcessable(resource.Kind, jobType) {
		return model.ProcessingJob{}, ErrProcessingResource
	}
	existing, err := service.jobs.FindReusableProcessingJob(ctx, resource.ID, jobType, resource.SHA256)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, repository.ErrNotFound) {
		return model.ProcessingJob{}, err
	}
	id, err := newProcessingID()
	if err != nil {
		return model.ProcessingJob{}, err
	}
	now := time.Now().UTC()
	job := model.ProcessingJob{ID: id, ResourceID: resource.ID, Type: jobType, SourceSHA256: resource.SHA256, Status: model.ProcessingStatusPending, MaxAttempts: ProcessingMaxAttempts, AvailableAt: now, CreatedAt: now}
	if err := service.createQueuedJob(ctx, job); errors.Is(err, repository.ErrProcessingJobExists) {
		return service.jobs.FindReusableProcessingJob(ctx, resource.ID, jobType, resource.SHA256)
	} else if err != nil {
		return model.ProcessingJob{}, err
	}
	service.invalidateJobCaches(ctx, job)
	return job, nil
}

// createQueuedJob 优先使用数据库事务容量门禁；内存测试仓储维持原有创建接口。
func (service *Processing) createQueuedJob(ctx context.Context, job model.ProcessingJob) error {
	if capacity, ok := service.jobs.(repository.ProcessingCapacityStore); ok {
		return capacity.CreateProcessingJobWithinLimit(ctx, job, service.maxOutstanding, service.outboxEnabled)
	}
	if service.outboxEnabled {
		outbox, ok := service.jobs.(repository.ProcessingOutboxStore)
		if !ok {
			return ErrQueueNotConfigured
		}
		return outbox.CreateProcessingJobWithOutbox(ctx, job)
	}
	return service.jobs.CreateProcessingJob(ctx, job)
}

// GetJob 读取任务详情并拒绝格式不合法的任务 ID，避免无效值进入数据库。
func (service *Processing) GetJob(ctx context.Context, id string) (model.ProcessingJob, error) {
	if !validResourceID(id) {
		return model.ProcessingJob{}, repository.ErrNotFound
	}
	var cached model.ProcessingJob
	if service.cache != nil {
		if hit, cacheErr := service.cache.Get(ctx, userCachePrefix(ctx)+"job:"+id, &cached); cacheErr == nil && hit {
			if _, resourceErr := service.store.GetResource(ctx, cached.ResourceID); resourceErr != nil {
				return model.ProcessingJob{}, resourceErr
			}
			return cached, nil
		}
	}
	job, err := service.jobs.GetProcessingJob(ctx, id)
	if err != nil {
		return model.ProcessingJob{}, err
	}
	if _, err := service.store.GetResource(ctx, job.ResourceID); err != nil {
		return model.ProcessingJob{}, err
	}
	if service.cache != nil {
		_ = service.cache.Set(ctx, userCachePrefix(ctx)+"job:"+id, job, 3*time.Second)
	}
	return job, nil
}

// ListJobs 返回资料最近的处理记录，调用方先确认资料仍对当前用户可见。
func (service *Processing) ListJobs(ctx context.Context, resourceID string) ([]model.ProcessingJob, error) {
	if !validResourceID(resourceID) {
		return nil, repository.ErrNotFound
	}
	if _, err := service.store.GetResource(ctx, resourceID); err != nil {
		return nil, err
	}
	key := userCachePrefix(ctx) + "resource-jobs:" + resourceID
	if service.cache != nil {
		var cached []model.ProcessingJob
		if hit, err := service.cache.Get(ctx, key, &cached); err == nil && hit {
			return cached, nil
		}
	}
	jobs, err := service.jobs.ListProcessingJobs(ctx, resourceID)
	if err != nil {
		return nil, err
	}
	if service.cache != nil {
		_ = service.cache.Set(ctx, key, jobs, 3*time.Second)
	}
	return jobs, nil
}

// GetAsset 读取派生产物元数据，下载接口随后用同一个服务打开受控文件。
func (service *Processing) GetAsset(ctx context.Context, id string) (model.DerivedAsset, error) {
	if !validResourceID(id) {
		return model.DerivedAsset{}, repository.ErrNotFound
	}
	asset, err := service.jobs.GetDerivedAsset(ctx, id)
	if err != nil {
		return model.DerivedAsset{}, err
	}
	if _, err := service.store.GetResource(ctx, asset.ResourceID); err != nil {
		return model.DerivedAsset{}, err
	}
	return asset, nil
}

// OpenAsset 只允许打开服务端生成的派生存储键，绝不使用用户提供的文件名拼接路径。
func (service *Processing) OpenAsset(asset model.DerivedAsset) (*os.File, error) {
	if !validResourceID(asset.StorageKey) || asset.StorageKey != asset.ID {
		return nil, ErrFileUnavailable
	}
	file, err := os.Open(filepath.Join(service.dataDir, "derived", asset.StorageKey))
	if err != nil {
		return nil, fmt.Errorf("open derived asset: %w", err)
	}
	return file, nil
}

// RunOnce 领取并执行一个任务；没有可执行任务时返回 false，不把空队列当成错误。
func (service *Processing) RunOnce(ctx context.Context) (bool, error) {
	job, err := service.jobs.ClaimNextProcessingJob(ctx, time.Now().UTC())
	if errors.Is(err, repository.ErrNoPendingJob) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, service.runClaimedJob(ctx, job)
}

// RunJob 按消息中的任务 ID 领取并执行任务；重复消息在数据库状态检查中幂等结束。
func (service *Processing) RunJob(ctx context.Context, jobID string) error {
	claimer, ok := service.jobs.(repository.ProcessingClaimStore)
	if !ok {
		return ErrQueueNotConfigured
	}
	job, err := claimer.ClaimProcessingJob(ctx, jobID, time.Now().UTC())
	if errors.Is(err, repository.ErrNoPendingJob) {
		current, readErr := service.jobs.GetProcessingJob(ctx, jobID)
		if errors.Is(readErr, repository.ErrNotFound) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
		if current.Status == model.ProcessingStatusSucceeded || current.Status == model.ProcessingStatusFailed {
			return nil
		}
		return ErrJobNotReady
	}
	if err != nil {
		return err
	}
	return service.runClaimedJob(ctx, job)
}

// RunOutboxOnce 发布一条事务性 Outbox 事件；发布确认后才将事件标为已发布。
func (service *Processing) RunOutboxOnce(ctx context.Context, publisher JobPublisher) (bool, error) {
	if publisher == nil {
		return false, ErrQueueNotConfigured
	}
	outbox, ok := service.jobs.(repository.ProcessingOutboxStore)
	if !ok {
		return false, ErrQueueNotConfigured
	}
	event, err := outbox.ClaimProcessingOutbox(ctx, time.Now().UTC())
	if errors.Is(err, repository.ErrNoPendingJob) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := publisher.PublishJob(ctx, event.JobID); err != nil {
		return true, outbox.FailProcessingOutbox(ctx, event.ID, event.LeaseToken, "RabbitMQ 发布失败")
	}
	return true, outbox.MarkProcessingOutboxPublished(ctx, event.ID, event.LeaseToken)
}

// runClaimedJob 执行已经持有租约的任务，并把资料处理错误转换为安全的业务状态。
func (service *Processing) runClaimedJob(ctx context.Context, job model.ProcessingJob) error {
	resource, err := service.store.GetResource(ctx, job.ResourceID)
	if err != nil {
		return service.failPermanent(ctx, job, "来源资料不存在或已删除")
	}
	// Worker 通过资源元数据取得归属，只用于失效对应用户缓存，不接受消息中的用户字段。
	cacheContext := repository.WithUserID(ctx, resource.OwnerID)
	defer service.invalidateJobCaches(cacheContext, job)
	if resource.SHA256 != job.SourceSHA256 || resource.StorageKey != resource.ID {
		return service.failPermanent(ctx, job, "来源资料已变化，任务结果已作废")
	}
	workContext, cancel := context.WithTimeout(ctx, ProcessingExecutionTimeout)
	defer cancel()
	content, assetKind, assetName, assetMIME, contentText, err := service.processJob(workContext, resource, job)
	if err != nil {
		return service.failPermanent(ctx, job, processingErrorMessage(job.Type, err))
	}
	asset, temporaryPath, err := service.writeDerivedAsset(resource, job, content, assetKind, assetName, assetMIME, contentText)
	if err != nil {
		return service.retryClaimedJob(ctx, job, "派生产物暂时无法写入")
	}
	if err := service.jobs.CompleteProcessingJob(ctx, job.ID, job.LeaseToken, asset); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	return nil
}

// invalidateJobCaches 清理任务详情和资料任务列表，保证 Worker 状态变化不会长期停留在旧缓存。
func (service *Processing) invalidateJobCaches(ctx context.Context, job model.ProcessingJob) {
	if service.cache == nil {
		return
	}
	prefix := userCachePrefix(ctx)
	_ = service.cache.Delete(ctx, prefix+"job:"+job.ID, prefix+"resource-jobs:"+job.ResourceID)
	_ = service.cache.DeleteByPrefix(ctx, prefix+"resource-list:")
}

// RepairOutbox 在发布者循环中补写老任务或丢失投递确认后的事件；已完成和仍有有效租约的任务不补。
func (service *Processing) RepairOutbox(ctx context.Context) (bool, error) {
	outbox, ok := service.jobs.(repository.ProcessingOutboxStore)
	if !ok {
		return false, ErrQueueNotConfigured
	}
	return outbox.RepairProcessingOutbox(ctx, time.Now().UTC())
}

// retryClaimedJob 将文件系统暂时失败重新放回数据库和 Outbox；没有 Outbox 时保留旧 Worker 的租约恢复语义。
func (service *Processing) retryClaimedJob(ctx context.Context, job model.ProcessingJob, message string) error {
	retryAt := time.Now().UTC().Add(time.Duration(job.Attempts*job.Attempts) * time.Second)
	return service.jobs.FailProcessingJob(ctx, job.ID, job.LeaseToken, message, &retryAt)
}

// RunLoop 有积压时连续领取，只有队列空闲才按间隔轮询，避免每个已处理任务额外等待一秒。
func (service *Processing) RunLoop(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = time.Second
	}
	for {
		processed, err := service.RunOnce(ctx)
		if err != nil {
			return err
		}
		if !processed {
			if err := waitForProcessingTick(ctx, interval); err != nil {
				return err
			}
		}
	}
}

// RunPool 以固定数量的数据库 Worker 竞争领取任务；任一 Worker 出错即取消同组，停机等待全部退出。
func (service *Processing) RunPool(ctx context.Context, workers int, interval time.Duration) error {
	if workers < 1 || workers > 4 {
		return errors.New("processing worker count must be between 1 and 4")
	}
	group, workerContext := errgroup.WithContext(ctx)
	for index := 0; index < workers; index++ {
		group.Go(func() error { return service.RunLoop(workerContext, interval) })
	}
	return group.Wait()
}

// waitForProcessingTick 在可取消定时器上等待，避免 Worker 停止时遗留睡眠协程。
func waitForProcessingTick(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// processJob 根据任务类型调用对应处理器，并返回派生产物的业务元数据。
func (service *Processing) processJob(ctx context.Context, resource model.Resource, job model.ProcessingJob) ([]byte, string, string, string, string, error) {
	sourcePath := filepath.Join(service.dataDir, resource.StorageKey)
	switch job.Type {
	case model.ProcessingTypeExtractText:
		content, err := processing.ExtractText(ctx, resource, sourcePath)
		return content, model.DerivedAssetText, resource.Name + ".extracted.txt", "text/plain; charset=utf-8", string(content), err
	case model.ProcessingTypeGenerateThumbnail:
		content, err := processing.GenerateThumbnail(ctx, resource, sourcePath)
		return content, model.DerivedAssetThumbnail, resource.Name + ".thumbnail.png", "image/png", "", err
	default:
		return nil, "", "", "", "", ErrProcessingType
	}
}

// writeDerivedAsset 原子写入派生文件并计算哈希；数据库提交前保留临时文件名以便失败清理。
func (service *Processing) writeDerivedAsset(resource model.Resource, job model.ProcessingJob, content []byte, assetKind, assetName, assetMIME, contentText string) (model.DerivedAsset, string, error) {
	derivedDir := filepath.Join(service.dataDir, "derived")
	temporary, err := os.CreateTemp(derivedDir, "pending-*")
	if err != nil {
		return model.DerivedAsset{}, "", fmt.Errorf("create derived temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer temporary.Close()
	if _, err := io.Copy(temporary, bytes.NewReader(content)); err != nil {
		_ = os.Remove(temporaryPath)
		return model.DerivedAsset{}, "", fmt.Errorf("write derived asset: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = os.Remove(temporaryPath)
		return model.DerivedAsset{}, "", fmt.Errorf("sync derived asset: %w", err)
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return model.DerivedAsset{}, "", fmt.Errorf("close derived asset: %w", err)
	}
	assetID, err := newProcessingID()
	if err != nil {
		_ = os.Remove(temporaryPath)
		return model.DerivedAsset{}, "", err
	}
	finalPath := filepath.Join(derivedDir, assetID)
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		_ = os.Remove(temporaryPath)
		return model.DerivedAsset{}, "", fmt.Errorf("commit derived asset: %w", err)
	}
	digest := sha256.Sum256(content)
	asset := model.DerivedAsset{ID: assetID, JobID: job.ID, ResourceID: resource.ID, Kind: assetKind, Name: assetName, StorageKey: assetID, MIME: assetMIME, Size: int64(len(content)), SHA256: hex.EncodeToString(digest[:]), ContentText: contentText, CreatedAt: time.Now().UTC()}
	return asset, finalPath, nil
}

// failPermanent 将处理器明确报告的错误压缩成用户可理解的摘要，不保存路径和堆栈。
func (service *Processing) failPermanent(ctx context.Context, job model.ProcessingJob, message string) error {
	return service.jobs.FailProcessingJob(ctx, job.ID, job.LeaseToken, message, nil)
}

// isProcessable 定义每种处理器允许的输入类型，避免任务请求绕过资料格式边界。
func isProcessable(kind, jobType string) bool {
	switch jobType {
	case model.ProcessingTypeExtractText:
		return kind == "pdf" || kind == "text" || kind == "markdown"
	case model.ProcessingTypeGenerateThumbnail:
		return kind == "image"
	default:
		return false
	}
}

// newProcessingID 生成不含资料名和路径信息的任务或派生产物 ID。
func newProcessingID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate processing ID: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

// processingErrorMessage 把底层解析错误映射为不暴露本地路径的提示。
func processingErrorMessage(jobType string, err error) string {
	switch {
	case errors.Is(err, processing.ErrUnsupportedType):
		if jobType == model.ProcessingTypeGenerateThumbnail {
			return "当前资料格式不支持缩略图生成"
		}
		return "当前资料格式不支持文本提取"
	case errors.Is(err, processing.ErrOutputTooLarge):
		return "提取结果超过 10 MB 限制"
	case errors.Is(err, processing.ErrInvalidText):
		return "提取结果不是合法 UTF-8 文本"
	case errors.Is(err, processing.ErrImageTooLarge):
		return "图片尺寸超过缩略图处理限制"
	case errors.Is(err, processing.ErrInvalidImage):
		return "图片内容无法识别"
	case errors.Is(err, ErrProcessingType):
		return "当前处理类型未开放"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "任务执行被取消或超时"
	default:
		return "资料内容无法解析"
	}
}
