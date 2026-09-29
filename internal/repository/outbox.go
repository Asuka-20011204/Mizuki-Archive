package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"mizuki-archive/internal/model"
)

// processingOutboxRow 隔离 Outbox 持久化字段，消息正文只在发布时由 JobID 生成。
type processingOutboxRow struct {
	ID          string     `gorm:"column:id;primaryKey"`
	JobID       string     `gorm:"column:job_id"`
	Status      string     `gorm:"column:status"`
	Attempts    int        `gorm:"column:attempts"`
	AvailableAt time.Time  `gorm:"column:available_at"`
	LeaseUntil  *time.Time `gorm:"column:lease_until"`
	LeaseToken  string     `gorm:"column:lease_token"`
	LastError   string     `gorm:"column:last_error"`
	CreatedAt   time.Time  `gorm:"column:created_at"`
	PublishedAt *time.Time `gorm:"column:published_at"`
}

// processingOutboxFromRow 将 Outbox 数据库行转换为领域模型。
func processingOutboxFromRow(row processingOutboxRow) model.ProcessingOutbox {
	return model.ProcessingOutbox{ID: row.ID, JobID: row.JobID, Status: row.Status, Attempts: row.Attempts, AvailableAt: row.AvailableAt, LeaseUntil: row.LeaseUntil, LeaseToken: row.LeaseToken, LastError: row.LastError, CreatedAt: row.CreatedAt, PublishedAt: row.PublishedAt}
}

// ClaimProcessingOutbox 领取一个待发布事件；发布者崩溃后，过期租约可以再次发布。
func (store *MySQL) ClaimProcessingOutbox(ctx context.Context, now time.Time) (model.ProcessingOutbox, error) {
	leaseToken, err := newProcessingID()
	if err != nil {
		return model.ProcessingOutbox{}, err
	}
	leaseUntil := now.Add(time.Minute)
	var event model.ProcessingOutbox
	err = store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row processingOutboxRow
		query := tx.Table("processing_outbox").Where("(status = ? AND available_at <= ?) OR (status = ? AND lease_until IS NOT NULL AND lease_until <= ?)", model.OutboxStatusPending, now, model.OutboxStatusPublishing, now).Order("available_at ASC").Order("created_at ASC").Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Take(&row)
		if errors.Is(query.Error, gorm.ErrRecordNotFound) {
			return ErrNoPendingJob
		}
		if query.Error != nil {
			return fmt.Errorf("claim processing outbox: %w", query.Error)
		}
		if err := tx.Table("processing_outbox").Where("id = ?", row.ID).Updates(map[string]any{"status": model.OutboxStatusPublishing, "attempts": gorm.Expr("attempts + 1"), "lease_until": leaseUntil, "lease_token": leaseToken, "last_error": ""}).Error; err != nil {
			return fmt.Errorf("mark processing outbox publishing: %w", err)
		}
		row.Status = model.OutboxStatusPublishing
		row.Attempts++
		row.LeaseUntil = &leaseUntil
		row.LeaseToken = leaseToken
		row.LastError = ""
		event = processingOutboxFromRow(row)
		return nil
	})
	return event, err
}

// MarkProcessingOutboxPublished 只有持有租约的发布者才能确认事件，重复确认保持幂等。
func (store *MySQL) MarkProcessingOutboxPublished(ctx context.Context, id, leaseToken string) error {
	now := time.Now().UTC()
	result := store.db.WithContext(ctx).Table("processing_outbox").Where("id = ? AND status = ? AND lease_token = ?", id, model.OutboxStatusPublishing, leaseToken).Updates(map[string]any{"status": model.OutboxStatusPublished, "lease_until": nil, "lease_token": nil, "published_at": now, "last_error": ""})
	if result.Error != nil {
		return fmt.Errorf("mark processing outbox published: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ErrJobLeaseLost
	}
	return nil
}

// FailProcessingOutbox 为发布失败安排上限一分钟的退避；Broker 故障不能让已创建的任务永久失败。
func (store *MySQL) FailProcessingOutbox(ctx context.Context, id, leaseToken, message string) error {
	return store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row processingOutboxRow
		if err := tx.Table("processing_outbox").Where("id = ? AND status = ? AND lease_token = ?", id, model.OutboxStatusPublishing, leaseToken).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrJobLeaseLost
			}
			return fmt.Errorf("load processing outbox failure: %w", err)
		}
		backoff := time.Duration(row.Attempts*row.Attempts) * time.Second
		if backoff > time.Minute {
			backoff = time.Minute
		}
		result := tx.Table("processing_outbox").Where("id = ? AND status = ? AND lease_token = ?", id, model.OutboxStatusPublishing, leaseToken).Updates(map[string]any{"status": model.OutboxStatusPending, "available_at": time.Now().UTC().Add(backoff), "lease_until": nil, "lease_token": nil, "last_error": message})
		if result.Error != nil {
			return fmt.Errorf("retry processing outbox: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ErrJobLeaseLost
		}
		return nil
	})
}

// RepairProcessingOutbox 为旧版本未投递任务和发布后丢失的消息补写事件，每个任务至少间隔五分钟再补。
func (store *MySQL) RepairProcessingOutbox(ctx context.Context, now time.Time) (bool, error) {
	repairID, err := newProcessingID()
	if err != nil {
		return false, err
	}
	repaired := false
	err = store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row struct{ ID string }
		threshold := now.Add(-5 * time.Minute)
		query := tx.Table("processing_jobs AS job").Select("job.id").
			Where("(job.status = ? AND job.available_at <= ?) OR (job.status = ? AND job.lease_until IS NOT NULL AND job.lease_until <= ?)", model.ProcessingStatusPending, now, model.ProcessingStatusProcessing, now).
			Where("NOT EXISTS (SELECT 1 FROM processing_outbox existing WHERE existing.job_id = job.id AND (existing.status IN ? OR existing.created_at > ?))", []string{model.OutboxStatusPending, model.OutboxStatusPublishing}, threshold).
			Order("job.created_at ASC").Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Take(&row)
		if errors.Is(query.Error, gorm.ErrRecordNotFound) {
			return nil
		}
		if query.Error != nil {
			return fmt.Errorf("find stranded processing job: %w", query.Error)
		}
		event := processingOutboxRow{ID: repairID, JobID: row.ID, Status: model.OutboxStatusPending, AvailableAt: now, CreatedAt: now}
		if err := tx.Table("processing_outbox").Create(&event).Error; err != nil {
			return fmt.Errorf("repair processing outbox: %w", err)
		}
		repaired = true
		return nil
	})
	return repaired, err
}
