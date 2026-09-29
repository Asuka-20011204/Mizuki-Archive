package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"mizuki-archive/internal/model"
)

// processingJobRow 隔离任务表字段与 API 模型，避免数据库时间指针和 JSON 结构互相污染。
type processingJobRow struct {
	ID           string     `gorm:"column:id;primaryKey"`
	ResourceID   string     `gorm:"column:resource_id"`
	Type         string     `gorm:"column:type"`
	SourceSHA256 string     `gorm:"column:source_sha256"`
	Status       string     `gorm:"column:status"`
	Attempts     int        `gorm:"column:attempts"`
	MaxAttempts  int        `gorm:"column:max_attempts"`
	AvailableAt  time.Time  `gorm:"column:available_at"`
	LeaseUntil   *time.Time `gorm:"column:lease_until"`
	LeaseToken   string     `gorm:"column:lease_token"`
	LastError    string     `gorm:"column:last_error"`
	StartedAt    *time.Time `gorm:"column:started_at"`
	FinishedAt   *time.Time `gorm:"column:finished_at"`
	CreatedAt    time.Time  `gorm:"column:created_at"`
}

// derivedAssetRow 隔离派生文件元数据和用于关键词检索的内容列。
type derivedAssetRow struct {
	ID          string    `gorm:"column:id;primaryKey"`
	JobID       string    `gorm:"column:job_id"`
	ResourceID  string    `gorm:"column:resource_id"`
	Kind        string    `gorm:"column:kind"`
	Name        string    `gorm:"column:name"`
	StorageKey  string    `gorm:"column:storage_key"`
	MIME        string    `gorm:"column:mime"`
	Size        int64     `gorm:"column:size_bytes"`
	SHA256      string    `gorm:"column:sha256"`
	ContentText string    `gorm:"column:content_text"`
	CreatedAt   time.Time `gorm:"column:created_at"`
}

// processingJobFromRow 将任务数据库行转换为可供 Controller 和 Worker 使用的模型。
func processingJobFromRow(row processingJobRow) model.ProcessingJob {
	return model.ProcessingJob{ID: row.ID, ResourceID: row.ResourceID, Type: row.Type, SourceSHA256: row.SourceSHA256, Status: row.Status, Attempts: row.Attempts, MaxAttempts: row.MaxAttempts, AvailableAt: row.AvailableAt, LeaseUntil: row.LeaseUntil, LeaseToken: row.LeaseToken, LastError: row.LastError, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt, CreatedAt: row.CreatedAt}
}

// derivedAssetFromRow 将派生文件数据库行转换为对外模型，并保留内容列只供仓储检索使用。
func derivedAssetFromRow(row derivedAssetRow) model.DerivedAsset {
	return model.DerivedAsset{ID: row.ID, JobID: row.JobID, ResourceID: row.ResourceID, Kind: row.Kind, Name: row.Name, StorageKey: row.StorageKey, MIME: row.MIME, Size: row.Size, SHA256: row.SHA256, ContentText: row.ContentText, CreatedAt: row.CreatedAt}
}

// newProcessingID 生成任务、租约和派生产物共用的随机十六进制 ID。
func newProcessingID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate processing ID: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

// FindReusableProcessingJob 查找同一原件的未完成或已成功任务，实现重复点击的幂等行为。
func (store *MySQL) FindReusableProcessingJob(ctx context.Context, resourceID, jobType, sourceSHA256 string) (model.ProcessingJob, error) {
	var row processingJobRow
	err := store.db.WithContext(ctx).Table("processing_jobs").Where("resource_id = ? AND type = ? AND source_sha256 = ? AND status IN ?", resourceID, jobType, sourceSHA256, []string{model.ProcessingStatusPending, model.ProcessingStatusProcessing, model.ProcessingStatusSucceeded}).Order("CASE status WHEN 'succeeded' THEN 0 WHEN 'processing' THEN 1 ELSE 2 END").Order("created_at DESC").Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.ProcessingJob{}, ErrNotFound
	}
	if err != nil {
		return model.ProcessingJob{}, fmt.Errorf("find reusable processing job: %w", err)
	}
	job := processingJobFromRow(row)
	if job.Status == model.ProcessingStatusSucceeded {
		asset, assetErr := store.loadAssetForJob(ctx, job.ID)
		if assetErr != nil {
			return model.ProcessingJob{}, assetErr
		}
		job.Asset = &asset
	}
	return job, nil
}

// CreateProcessingJob 写入待处理任务；调用方已经完成资料类型和来源哈希校验。
func (store *MySQL) CreateProcessingJob(ctx context.Context, job model.ProcessingJob) error {
	row := processingJobRow{ID: job.ID, ResourceID: job.ResourceID, Type: job.Type, SourceSHA256: job.SourceSHA256, Status: job.Status, Attempts: job.Attempts, MaxAttempts: job.MaxAttempts, AvailableAt: job.AvailableAt, CreatedAt: job.CreatedAt}
	if err := store.db.WithContext(ctx).Table("processing_jobs").Create(&row).Error; err != nil {
		return fmt.Errorf("create processing job: %w", err)
	}
	return nil
}

// CreateProcessingJobWithOutbox 在同一个 MySQL 事务中写入任务和初始投递事件，避免任务已创建但消息未生成。
func (store *MySQL) CreateProcessingJobWithOutbox(ctx context.Context, job model.ProcessingJob) error {
	eventID, err := newProcessingID()
	if err != nil {
		return err
	}
	return store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var resource struct{ ID string }
		if err := tx.Table("resources").Where("id = ? AND deleted_at IS NULL", job.ResourceID).Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Take(&resource).Error; err != nil {
			return fmt.Errorf("lock processing resource: %w", err)
		}
		var count int64
		if err := tx.Table("processing_jobs").Where("resource_id = ? AND type = ? AND source_sha256 = ? AND status IN ?", job.ResourceID, job.Type, job.SourceSHA256, []string{model.ProcessingStatusPending, model.ProcessingStatusProcessing, model.ProcessingStatusSucceeded}).Count(&count).Error; err != nil {
			return fmt.Errorf("check duplicate processing job: %w", err)
		}
		if count != 0 {
			return ErrProcessingJobExists
		}
		row := processingJobRow{ID: job.ID, ResourceID: job.ResourceID, Type: job.Type, SourceSHA256: job.SourceSHA256, Status: job.Status, Attempts: job.Attempts, MaxAttempts: job.MaxAttempts, AvailableAt: job.AvailableAt, CreatedAt: job.CreatedAt}
		if err := tx.Table("processing_jobs").Create(&row).Error; err != nil {
			return fmt.Errorf("create processing job with outbox: %w", err)
		}
		outbox := processingOutboxRow{ID: eventID, JobID: job.ID, Status: model.OutboxStatusPending, AvailableAt: job.AvailableAt, CreatedAt: job.CreatedAt}
		if err := tx.Table("processing_outbox").Create(&outbox).Error; err != nil {
			return fmt.Errorf("create processing outbox: %w", err)
		}
		return nil
	})
}

// CreateProcessingJobWithinLimit 锁定容量行后检查幂等和全局未完成数，再同事务写任务与可选事件。
func (store *MySQL) CreateProcessingJobWithinLimit(ctx context.Context, job model.ProcessingJob, limit int, withOutbox bool) error {
	if limit < 1 {
		return errors.New("processing capacity limit must be positive")
	}
	return store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := checkProcessingCapacity(tx, job, limit); err != nil {
			return err
		}
		row := processingJobRow{ID: job.ID, ResourceID: job.ResourceID, Type: job.Type, SourceSHA256: job.SourceSHA256, Status: job.Status, Attempts: job.Attempts, MaxAttempts: job.MaxAttempts, AvailableAt: job.AvailableAt, CreatedAt: job.CreatedAt}
		if err := tx.Table("processing_jobs").Create(&row).Error; err != nil {
			return fmt.Errorf("create processing job within capacity: %w", err)
		}
		if !withOutbox {
			return nil
		}
		eventID, err := newProcessingID()
		if err != nil {
			return err
		}
		event := processingOutboxRow{ID: eventID, JobID: job.ID, Status: model.OutboxStatusPending, AvailableAt: job.AvailableAt, CreatedAt: job.CreatedAt}
		if err := tx.Table("processing_outbox").Create(&event).Error; err != nil {
			return fmt.Errorf("create processing outbox within capacity: %w", err)
		}
		return nil
	})
}

// checkProcessingCapacity 在锁住全局容量行和来源资料后先识别重复请求，再统计未完成任务。
func checkProcessingCapacity(tx *gorm.DB, job model.ProcessingJob, limit int) error {
	var capacity struct{ ID int }
	result := tx.Table("processing_capacity").Where("id = ?", 1).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&capacity)
	if result.Error != nil {
		return fmt.Errorf("lock processing capacity: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return errors.New("processing capacity guard missing")
	}
	var resource struct{ ID string }
	if err := tx.Table("resources").Where("id = ? AND deleted_at IS NULL", job.ResourceID).Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Take(&resource).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return fmt.Errorf("lock processing resource: %w", err)
	}
	var duplicates int64
	if err := tx.Table("processing_jobs").Where("resource_id = ? AND type = ? AND source_sha256 = ? AND status IN ?", job.ResourceID, job.Type, job.SourceSHA256, []string{model.ProcessingStatusPending, model.ProcessingStatusProcessing, model.ProcessingStatusSucceeded}).Count(&duplicates).Error; err != nil {
		return fmt.Errorf("check duplicate processing job: %w", err)
	}
	if duplicates != 0 {
		return ErrProcessingJobExists
	}
	var outstanding int64
	if err := tx.Table("processing_jobs").Where("status IN ?", []string{model.ProcessingStatusPending, model.ProcessingStatusProcessing}).Count(&outstanding).Error; err != nil {
		return fmt.Errorf("count outstanding processing jobs: %w", err)
	}
	if outstanding >= int64(limit) {
		return ErrProcessingQueueFull
	}
	return nil
}

// GetProcessingJob 获取单个任务并在成功时附带派生产物元数据。
func (store *MySQL) GetProcessingJob(ctx context.Context, id string) (model.ProcessingJob, error) {
	var row processingJobRow
	err := store.db.WithContext(ctx).Table("processing_jobs").Where("id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.ProcessingJob{}, ErrNotFound
	}
	if err != nil {
		return model.ProcessingJob{}, fmt.Errorf("get processing job: %w", err)
	}
	job := processingJobFromRow(row)
	if job.Status == model.ProcessingStatusSucceeded {
		asset, assetErr := store.loadAssetForJob(ctx, job.ID)
		if assetErr != nil {
			return model.ProcessingJob{}, assetErr
		}
		job.Asset = &asset
	}
	return job, nil
}

// ListProcessingJobs 按创建时间倒序返回资料的任务历史，成功任务附带下载元数据。
func (store *MySQL) ListProcessingJobs(ctx context.Context, resourceID string) ([]model.ProcessingJob, error) {
	var rows []processingJobRow
	if err := store.db.WithContext(ctx).Table("processing_jobs").Where("resource_id = ?", resourceID).Order("created_at DESC").Limit(20).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list processing jobs: %w", err)
	}
	jobs := make([]model.ProcessingJob, 0, len(rows))
	for _, row := range rows {
		job := processingJobFromRow(row)
		if job.Status == model.ProcessingStatusSucceeded {
			asset, err := store.loadAssetForJob(ctx, job.ID)
			if err != nil {
				return nil, err
			}
			job.Asset = &asset
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

// ClaimNextProcessingJob 在事务和行锁内领取一个到期任务，避免多个 Worker 重复处理同一原件。
func (store *MySQL) ClaimNextProcessingJob(ctx context.Context, now time.Time) (model.ProcessingJob, error) {
	leaseToken, err := newProcessingID()
	if err != nil {
		return model.ProcessingJob{}, err
	}
	leaseUntil := now.Add(5 * time.Minute)
	var job model.ProcessingJob
	err = store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row processingJobRow
		query := tx.Table("processing_jobs").Where("(status = ? AND available_at <= ?) OR (status = ? AND lease_until IS NOT NULL AND lease_until <= ?)", model.ProcessingStatusPending, now, model.ProcessingStatusProcessing, now).Order("available_at ASC").Order("created_at ASC").Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Take(&row)
		if errors.Is(query.Error, gorm.ErrRecordNotFound) {
			return ErrNoPendingJob
		}
		if query.Error != nil {
			return fmt.Errorf("claim processing job: %w", query.Error)
		}
		if row.Attempts >= row.MaxAttempts {
			return failExhaustedProcessingJob(tx, row.ID, now)
		}
		started := now
		updates := map[string]any{"status": model.ProcessingStatusProcessing, "attempts": gorm.Expr("attempts + 1"), "lease_until": leaseUntil, "lease_token": leaseToken, "started_at": started, "finished_at": nil, "last_error": ""}
		if err := tx.Table("processing_jobs").Where("id = ?", row.ID).Updates(updates).Error; err != nil {
			return fmt.Errorf("mark processing job: %w", err)
		}
		row.Status = model.ProcessingStatusProcessing
		row.Attempts++
		row.LeaseUntil = &leaseUntil
		row.LeaseToken = leaseToken
		row.StartedAt = &started
		row.FinishedAt = nil
		row.LastError = ""
		job = processingJobFromRow(row)
		return nil
	})
	if err == nil && job.ID == "" {
		return model.ProcessingJob{}, ErrNoPendingJob
	}
	return job, err
}

// ClaimProcessingJob 按 RabbitMQ 消息中的任务 ID 获取租约；重复消息在任务已完成时会被安全忽略。
func (store *MySQL) ClaimProcessingJob(ctx context.Context, id string, now time.Time) (model.ProcessingJob, error) {
	leaseToken, err := newProcessingID()
	if err != nil {
		return model.ProcessingJob{}, err
	}
	leaseUntil := now.Add(5 * time.Minute)
	var job model.ProcessingJob
	err = store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row processingJobRow
		query := tx.Table("processing_jobs").Where("id = ? AND ((status = ? AND available_at <= ?) OR (status = ? AND lease_until IS NOT NULL AND lease_until <= ?))", id, model.ProcessingStatusPending, now, model.ProcessingStatusProcessing, now).Clauses(clause.Locking{Strength: "UPDATE"}).Take(&row)
		if errors.Is(query.Error, gorm.ErrRecordNotFound) {
			return ErrNoPendingJob
		}
		if query.Error != nil {
			return fmt.Errorf("claim processing job by ID: %w", query.Error)
		}
		if row.Attempts >= row.MaxAttempts {
			return failExhaustedProcessingJob(tx, row.ID, now)
		}
		started := now
		updates := map[string]any{"status": model.ProcessingStatusProcessing, "attempts": gorm.Expr("attempts + 1"), "lease_until": leaseUntil, "lease_token": leaseToken, "started_at": started, "finished_at": nil, "last_error": ""}
		if err := tx.Table("processing_jobs").Where("id = ?", row.ID).Updates(updates).Error; err != nil {
			return fmt.Errorf("mark processing job by ID: %w", err)
		}
		row.Status = model.ProcessingStatusProcessing
		row.Attempts++
		row.LeaseUntil = &leaseUntil
		row.LeaseToken = leaseToken
		row.StartedAt = &started
		row.FinishedAt = nil
		row.LastError = ""
		job = processingJobFromRow(row)
		return nil
	})
	if err == nil && job.ID == "" {
		return model.ProcessingJob{}, ErrNoPendingJob
	}
	return job, err
}

// failExhaustedProcessingJob 将多次崩溃后耗尽执行次数的任务终止，避免租约过期后无限重跑。
func failExhaustedProcessingJob(tx *gorm.DB, id string, now time.Time) error {
	if err := tx.Table("processing_jobs").Where("id = ?", id).Updates(map[string]any{"status": model.ProcessingStatusFailed, "lease_until": nil, "lease_token": nil, "finished_at": now, "last_error": "处理次数已用尽"}).Error; err != nil {
		return fmt.Errorf("finish exhausted processing job: %w", err)
	}
	return nil
}

// CompleteProcessingJob 在租约校验通过后原子保存派生产物元数据并结束任务。
func (store *MySQL) CompleteProcessingJob(ctx context.Context, jobID, leaseToken string, asset model.DerivedAsset) error {
	return store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row processingJobRow
		if err := tx.Table("processing_jobs").Where("id = ?", jobID).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrJobLeaseLost
			}
			return fmt.Errorf("load processing job for completion: %w", err)
		}
		if row.Status == model.ProcessingStatusSucceeded {
			return nil
		}
		if row.Status != model.ProcessingStatusProcessing || row.LeaseToken != leaseToken {
			return ErrJobLeaseLost
		}
		assetRow := derivedAssetRow{ID: asset.ID, JobID: asset.JobID, ResourceID: asset.ResourceID, Kind: asset.Kind, Name: asset.Name, StorageKey: asset.StorageKey, MIME: asset.MIME, Size: asset.Size, SHA256: asset.SHA256, ContentText: asset.ContentText, CreatedAt: asset.CreatedAt}
		if err := tx.Table("derived_assets").Create(&assetRow).Error; err != nil {
			return fmt.Errorf("save derived asset: %w", err)
		}
		finished := time.Now().UTC()
		update := tx.Table("processing_jobs").Where("id = ? AND status = ? AND lease_token = ?", jobID, model.ProcessingStatusProcessing, leaseToken).Updates(map[string]any{"status": model.ProcessingStatusSucceeded, "lease_until": nil, "lease_token": nil, "finished_at": finished, "last_error": ""})
		if update.Error != nil {
			return fmt.Errorf("complete processing job: %w", update.Error)
		}
		if update.RowsAffected != 1 {
			return ErrJobLeaseLost
		}
		return nil
	})
}

// FailProcessingJob 记录脱敏错误；retryAt 非空且仍有次数时重新排队，否则终止任务。
func (store *MySQL) FailProcessingJob(ctx context.Context, jobID, leaseToken, message string, retryAt *time.Time) error {
	return store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row processingJobRow
		if err := tx.Table("processing_jobs").Where("id = ?", jobID).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrJobLeaseLost
			}
			return fmt.Errorf("load processing job for failure: %w", err)
		}
		if row.Status != model.ProcessingStatusProcessing || row.LeaseToken != leaseToken {
			return ErrJobLeaseLost
		}
		status := model.ProcessingStatusFailed
		availableAt := row.AvailableAt
		if retryAt != nil && row.Attempts < row.MaxAttempts {
			status = model.ProcessingStatusPending
			availableAt = *retryAt
		}
		finishedAt := time.Now().UTC()
		updates := map[string]any{"status": status, "available_at": availableAt, "lease_until": nil, "lease_token": nil, "last_error": message, "finished_at": finishedAt}
		if status == model.ProcessingStatusPending {
			updates["finished_at"] = nil
		}
		result := tx.Table("processing_jobs").Where("id = ? AND status = ? AND lease_token = ?", jobID, model.ProcessingStatusProcessing, leaseToken).Updates(updates)
		if result.Error != nil {
			return fmt.Errorf("fail processing job: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrJobLeaseLost
		}
		if status == model.ProcessingStatusPending && store.outboxEnabled {
			eventID, err := newProcessingID()
			if err != nil {
				return err
			}
			outbox := processingOutboxRow{ID: eventID, JobID: jobID, Status: model.OutboxStatusPending, AvailableAt: availableAt, CreatedAt: finishedAt}
			if err := tx.Table("processing_outbox").Create(&outbox).Error; err != nil {
				return fmt.Errorf("create retry processing outbox: %w", err)
			}
		}
		return nil
	})
}

// GetDerivedAsset 获取受权限保护的派生文件元数据，不返回数据库中的文本索引正文。
func (store *MySQL) GetDerivedAsset(ctx context.Context, id string) (model.DerivedAsset, error) {
	var row derivedAssetRow
	err := store.db.WithContext(ctx).Table("derived_assets").Where("id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.DerivedAsset{}, ErrNotFound
	}
	if err != nil {
		return model.DerivedAsset{}, fmt.Errorf("get derived asset: %w", err)
	}
	return derivedAssetFromRow(row), nil
}

// loadAssetForJob 查询一个任务的成功派生产物，缺失时返回持久化错误而不是伪造成功结果。
func (store *MySQL) loadAssetForJob(ctx context.Context, jobID string) (model.DerivedAsset, error) {
	var row derivedAssetRow
	err := store.db.WithContext(ctx).Table("derived_assets").Where("job_id = ?", jobID).Order("created_at DESC").Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.DerivedAsset{}, errors.New("derived asset missing for succeeded job")
	}
	if err != nil {
		return model.DerivedAsset{}, fmt.Errorf("load derived asset: %w", err)
	}
	return derivedAssetFromRow(row), nil
}
