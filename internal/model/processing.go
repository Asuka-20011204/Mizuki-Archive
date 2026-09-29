package model

import "time"

const (
	// ProcessingTypeExtractText 表示从 PDF、TXT 或 Markdown 生成可检索的纯文本副本。
	ProcessingTypeExtractText = "extract_text"
	// ProcessingTypeGenerateThumbnail 表示从图片生成受控尺寸的 PNG 缩略图。
	ProcessingTypeGenerateThumbnail = "generate_thumbnail"

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
	// DerivedAssetThumbnail 表示由图片原件生成的 PNG 缩略图派生文件。
	DerivedAssetThumbnail = "thumbnail"

	// OutboxStatusPending 表示任务事件等待发布到 RabbitMQ。
	OutboxStatusPending = "pending"
	// OutboxStatusPublishing 表示某个发布者暂时持有事件租约。
	OutboxStatusPublishing = "publishing"
	// OutboxStatusPublished 表示 RabbitMQ 已返回发布确认。
	OutboxStatusPublished = "published"
	// OutboxStatusDead 表示超过发布重试上限，等待人工排查。
	OutboxStatusDead = "dead"
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

// ProcessingOutbox 描述一条只包含任务 ID 的待发布事件，不携带原始文件或提取正文。
type ProcessingOutbox struct {
	ID          string     `json:"id"`
	JobID       string     `json:"job_id"`
	Status      string     `json:"status"`
	Attempts    int        `json:"attempts"`
	AvailableAt time.Time  `json:"available_at"`
	LeaseUntil  *time.Time `json:"-"`
	LeaseToken  string     `json:"-"`
	LastError   string     `json:"last_error,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
}
