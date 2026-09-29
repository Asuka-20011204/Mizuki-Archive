package model

import "time"

const (
	// ProcessingTypeExtractText 表示从 PDF、TXT 或 Markdown 生成可检索的纯文本副本。
	ProcessingTypeExtractText = "extract_text"

	// ProcessingStatusPending 表示任务已创建但尚未被 Worker 领取。
	ProcessingStatusPending = "pending"
	// ProcessingStatusProcessing 表示任务已被某个 Worker 领取并持有租约。
	ProcessingStatusProcessing = "processing"
	// ProcessingStatusSucceeded 表示派生文件和索引内容都已成功保存。
	ProcessingStatusSucceeded = "succeeded"
	// ProcessingStatusFailed 表示任务已不可重试或达到最大尝试次数。
	ProcessingStatusFailed = "failed"

	// DerivedAssetText 表示由原件提取出的 UTF-8 纯文本派生文件。
	DerivedAssetText = "extracted_text"
)

// ProcessingJob 描述一次手动触发的资料处理任务和当前状态。
type ProcessingJob struct {
	ID           string        `json:"id"`
	ResourceID   string        `json:"resource_id"`
	Type         string        `json:"type"`
	SourceSHA256 string        `json:"source_sha256"`
	Status       string        `json:"status"`
	Attempts     int           `json:"attempts"`
	MaxAttempts  int           `json:"max_attempts"`
	AvailableAt  time.Time     `json:"available_at"`
	LeaseUntil   *time.Time    `json:"lease_until,omitempty"`
	LeaseToken   string        `json:"-"`
	LastError    string        `json:"last_error,omitempty"`
	StartedAt    *time.Time    `json:"started_at,omitempty"`
	FinishedAt   *time.Time    `json:"finished_at,omitempty"`
	CreatedAt    time.Time     `json:"created_at"`
	Asset        *DerivedAsset `json:"asset,omitempty"`
}

// DerivedAsset 描述从原件生成、可单独下载和检索的派生文件。
type DerivedAsset struct {
	ID          string    `json:"id"`
	JobID       string    `json:"job_id"`
	ResourceID  string    `json:"resource_id"`
	Kind        string    `json:"kind"`
	Name        string    `json:"name"`
	StorageKey  string    `json:"-"`
	MIME        string    `json:"mime"`
	Size        int64     `json:"size"`
	SHA256      string    `json:"sha256"`
	CreatedAt   time.Time `json:"created_at"`
	ContentText string    `json:"-"`
}

// ProcessingSummary 是资料详情中用于显示的任务摘要，避免列表接口携带大字段。
type ProcessingSummary struct {
	Jobs []ProcessingJob `json:"jobs"`
}
