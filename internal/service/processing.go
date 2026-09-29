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
)

var (
	// ErrProcessingType 表示客户端请求了当前版本未开放的处理器。
	ErrProcessingType = errors.New("unsupported processing type")
	// ErrProcessingResource 表示当前资料格式不能执行所请求的处理。
	ErrProcessingResource = errors.New("resource cannot be processed")
)

// Processing 负责创建任务、执行 Worker 单次循环以及管理派生文件，不依赖 Gin。
type Processing struct {
	store   repository.Store
	jobs    repository.ProcessingStore
	dataDir string
}

// NewProcessing 校验任务存储和派生目录；目录不可用时拒绝启动处理功能。
func NewProcessing(store repository.Store, jobs repository.ProcessingStore, dataDir string) (*Processing, error) {
	if store == nil || jobs == nil || dataDir == "" {
		return nil, errors.New("invalid processing configuration")
	}
	derivedDir := filepath.Join(dataDir, "derived")
	if err := os.MkdirAll(derivedDir, 0700); err != nil {
		return nil, fmt.Errorf("create derived directory: %w", err)
	}
	return &Processing{store: store, jobs: jobs, dataDir: dataDir}, nil
}

// CreateTextJob 手动为一份 PDF、TXT 或 Markdown 创建幂等文本提取任务。
func (service *Processing) CreateTextJob(ctx context.Context, resourceID string) (model.ProcessingJob, error) {
	if !validResourceID(resourceID) {
		return model.ProcessingJob{}, repository.ErrNotFound
	}
	resource, err := service.store.GetResource(ctx, resourceID)
	if err != nil {
		return model.ProcessingJob{}, err
	}
	if !isTextProcessable(resource.Kind) {
		return model.ProcessingJob{}, ErrProcessingResource
	}
	existing, err := service.jobs.FindReusableProcessingJob(ctx, resource.ID, model.ProcessingTypeExtractText, resource.SHA256)
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
	job := model.ProcessingJob{ID: id, ResourceID: resource.ID, Type: model.ProcessingTypeExtractText, SourceSHA256: resource.SHA256, Status: model.ProcessingStatusPending, MaxAttempts: ProcessingMaxAttempts, AvailableAt: now, CreatedAt: now}
	if err := service.jobs.CreateProcessingJob(ctx, job); err != nil {
		return model.ProcessingJob{}, err
	}
	return job, nil
}

// GetJob 读取任务详情并拒绝格式不合法的任务 ID，避免无效值进入数据库。
func (service *Processing) GetJob(ctx context.Context, id string) (model.ProcessingJob, error) {
	if !validResourceID(id) {
		return model.ProcessingJob{}, repository.ErrNotFound
	}
	job, err := service.jobs.GetProcessingJob(ctx, id)
	if err != nil {
		return model.ProcessingJob{}, err
	}
	if _, err := service.store.GetResource(ctx, job.ResourceID); err != nil {
		return model.ProcessingJob{}, err
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
	return service.jobs.ListProcessingJobs(ctx, resourceID)
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
	resource, err := service.store.GetResource(ctx, job.ResourceID)
	if err != nil {
		return true, service.failPermanent(ctx, job, "来源资料不存在或已删除")
	}
	if resource.SHA256 != job.SourceSHA256 || resource.StorageKey != resource.ID {
		return true, service.failPermanent(ctx, job, "来源资料已变化，任务结果已作废")
	}
	workContext, cancel := context.WithTimeout(ctx, ProcessingExecutionTimeout)
	defer cancel()
	content, err := processing.ExtractText(workContext, resource, filepath.Join(service.dataDir, resource.StorageKey))
	if err != nil {
		return true, service.failPermanent(ctx, job, processingErrorMessage(err))
	}
	asset, temporaryPath, err := service.writeDerivedAsset(resource, job, content)
	if err != nil {
		return true, err
	}
	if err := service.jobs.CompleteProcessingJob(ctx, job.ID, job.LeaseToken, asset); err != nil {
		_ = os.Remove(temporaryPath)
		return true, err
	}
	return true, nil
}

// RunLoop 按固定间隔执行单 Worker 循环；停止信号由 cmd/worker 的 Context 传入。
func (service *Processing) RunLoop(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = time.Second
	}
	for {
		_, err := service.RunOnce(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		if err := waitForProcessingTick(ctx, interval); err != nil {
			return err
		}
	}
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

// writeDerivedAsset 原子写入派生文本并计算哈希；数据库提交前保留临时文件名以便失败清理。
func (service *Processing) writeDerivedAsset(resource model.Resource, job model.ProcessingJob, content []byte) (model.DerivedAsset, string, error) {
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
	asset := model.DerivedAsset{ID: assetID, JobID: job.ID, ResourceID: resource.ID, Kind: model.DerivedAssetText, Name: resource.Name + ".extracted.txt", StorageKey: assetID, MIME: "text/plain; charset=utf-8", Size: int64(len(content)), SHA256: hex.EncodeToString(digest[:]), ContentText: string(content), CreatedAt: time.Now().UTC()}
	return asset, finalPath, nil
}

// failPermanent 将处理器明确报告的错误压缩成用户可理解的摘要，不保存路径和堆栈。
func (service *Processing) failPermanent(ctx context.Context, job model.ProcessingJob, message string) error {
	return service.jobs.FailProcessingJob(ctx, job.ID, job.LeaseToken, message, nil)
}

// isTextProcessable 定义 V2 首个处理器允许的输入类型，图片缩略图留到后续独立处理器。
func isTextProcessable(kind string) bool {
	return kind == "pdf" || kind == "text" || kind == "markdown"
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
func processingErrorMessage(err error) string {
	switch {
	case errors.Is(err, processing.ErrUnsupportedType):
		return "当前资料格式不支持文本提取"
	case errors.Is(err, processing.ErrOutputTooLarge):
		return "提取结果超过 10 MB 限制"
	case errors.Is(err, processing.ErrInvalidText):
		return "提取结果不是合法 UTF-8 文本"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "任务执行被取消或超时"
	default:
		return "资料内容无法解析"
	}
}
