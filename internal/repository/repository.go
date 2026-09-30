// Package repository 封装持久化边界；Service 只依赖 Store 行为，不直接构造 SQL。
package repository

import (
	"context"
	"errors"
	"time"

	"mizuki-archive/internal/model"
)

var ErrNotFound = errors.New("resource not found")
var ErrNoPendingJob = errors.New("no pending processing job")
var ErrJobLeaseLost = errors.New("processing job lease lost")
var ErrProcessingJobExists = errors.New("processing job already exists")
var ErrProcessingQueueFull = errors.New("processing queue is full")
var ErrUserExists = errors.New("user already exists")
var ErrSavedSearchConflict = errors.New("saved search name already exists")
var ErrSavedSearchLimit = errors.New("saved search view limit reached")
var ErrBatchTagLimit = errors.New("resource tag limit reached")
var ErrPortableExportLimit = errors.New("portable export exceeds row limit")

// PortableExportStore 在数据库快照中读取当前用户的完整可见元数据，不读取原件或验证码。
type PortableExportStore interface {
	ExportSnapshot(context.Context) (model.PortableArchive, error)
}

var ErrRelationExists = errors.New("resource relation already exists")
var ErrRelationLimit = errors.New("resource relation limit reached")

// RelationStore 为两类私有资料提供有界双向关联，创建时必须原子核验两个端点归属。
type RelationStore interface {
	CreateRelation(context.Context, model.InboxSelection, model.InboxSelection, string) (model.ResourceRelation, error)
	ListRelations(context.Context, model.InboxSelection) ([]model.ResourceRelation, error)
	DeleteRelation(context.Context, string) error
}

// InboxStore 同时读取用户的两类待整理条目，避免扩大已有文件与外部资源测试替身的接口。
type InboxStore interface {
	ListPendingResources(context.Context, int, int) ([]model.Resource, error)
	ListPendingExternalResources(context.Context, int, int) ([]model.ExternalResource, error)
	SetResourceOrganizationStatus(context.Context, string, string) error
	SetExternalOrganizationStatus(context.Context, string, string) error
	BatchSetOrganizationStatus(context.Context, []model.InboxSelection, string) error
}

// BatchTagStore 在同一事务中校验混合来源的归属，并只返回标签真正变化的条目。
type BatchTagStore interface {
	BatchUpdateTags(context.Context, []model.InboxSelection, string, string) ([]model.InboxSelection, error)
}

// BatchFavoriteStore 同时处理两类条目的收藏状态，并让调用方只对真实变化项显示反向入口。
type BatchFavoriteStore interface {
	BatchUpdateFavorites(context.Context, []model.InboxSelection, bool) ([]model.InboxSelection, error)
}

// ArchiveStore 负责两类资料的归档查询与原子恢复；归档不删除原件或改变整理状态。
type ArchiveStore interface {
	ListArchivedResources(context.Context, int, int) ([]model.Resource, error)
	ListArchivedExternalResources(context.Context, int, int) ([]model.ExternalResource, error)
	BatchSetArchived(context.Context, []model.InboxSelection, bool) ([]model.InboxSelection, error)
}

// BatchDeletedFiles 在数据库提交后交给 Service 清理原件与派生产物，不暴露给 HTTP 客户端。
type BatchDeletedFiles struct {
	OriginalIDs []string
	DerivedIDs  []string
}

// PendingFileCleanup 只含服务端生成的存储键和固定类型，不携带用户原始文件名或目录路径。
type PendingFileCleanup struct {
	Kind string
	ID   string
}

// BatchDeleteStore 原子核验两类资料并返回需要清理的受控文件 ID。
type BatchDeleteStore interface {
	BatchDeleteEntries(context.Context, []model.InboxSelection) (BatchDeletedFiles, error)
	ListPendingFileCleanup(context.Context, int) ([]PendingFileCleanup, error)
	CompleteFileCleanup(context.Context, PendingFileCleanup) error
	DeferFileCleanup(context.Context, PendingFileCleanup) error
}

// ExternalResourceStore 单独管理外部卡片，避免修改已有文件仓储和测试替身的契约。
type ExternalResourceStore interface {
	CreateExternalResource(context.Context, model.ExternalResource) error
	GetExternalResource(context.Context, string) (model.ExternalResource, error)
	ListExternalResources(context.Context, string) ([]model.ExternalResource, error)
	UpdateExternalResource(context.Context, model.ExternalResource) (model.ExternalResource, error)
	DeleteExternalResource(context.Context, string) error
}

// DuplicateStore 只查同账号可见的疑似重复项；不存在时返回 nil，不自动合并资料。
type DuplicateStore interface {
	FindFileDuplicate(context.Context, string, string) (*model.DuplicateHint, error)
	FindExternalDuplicate(context.Context, string, string) (*model.DuplicateHint, error)
}

// SearchStore 查询当前会话范围内的文件元数据、标签、正文及外部卡片线索。
type SearchStore interface {
	SearchFiles(context.Context, model.SearchFilter, int, int) ([]model.Resource, error)
	SearchExternal(context.Context, model.SearchFilter, int, int) ([]model.ExternalResource, error)
}

// ResourceNoteStore 只管理当前会话用户的文件注记；所有写入仍需在仓储层检查文件归属。
type ResourceNoteStore interface {
	GetResource(context.Context, string) (model.Resource, error)
	CreateResourceNote(context.Context, model.ResourceNote) error
	ListResourceNotes(context.Context, string) ([]model.ResourceNote, error)
	UpdateResourceNote(context.Context, model.ResourceNote) error
	DeleteResourceNote(context.Context, string, string) error
}

// SavedSearchStore 只存放已验证用户的检索条件；仓储必须约束数量并强制账号归属。
type SavedSearchStore interface {
	CreateSavedSearch(context.Context, model.SavedSearch) error
	ListSavedSearches(context.Context) ([]model.SavedSearch, error)
	DeleteSavedSearch(context.Context, string) error
}

// Store 包含首期资料和会话的持久化操作，测试可用内存实现替换 MySQL。
type Store interface {
	SaveResource(context.Context, model.Resource) error
	GetResource(context.Context, string) (model.Resource, error)
	SetFavorite(context.Context, string, bool) (model.Resource, error)
	ReplaceResourceTags(context.Context, string, []string) (model.Resource, error)
	ListTags(context.Context, string) ([]string, error)
	UpdateResourceName(context.Context, string, string) (model.Resource, error)
	DeleteResource(context.Context, string) (model.Resource, error)
	ListResources(context.Context, model.ListQuery) ([]model.Resource, error)
	SaveSession(context.Context, string, time.Time) error
	HasSession(context.Context, string) (bool, error)
	DeleteSession(context.Context, string) error
}

// IdentityStore 提供多用户身份和按用户归属的会话持久化；MySQL 实现必须保证唯一约束与原子绑定。
type IdentityStore interface {
	EnsureAdminUser(context.Context, string, []byte) (model.User, error)
	CreateUser(context.Context, string) (model.User, error)
	GetUserByEmail(context.Context, string) (model.User, error)
	GetUserByID(context.Context, string) (model.User, error)
	SaveUserSession(context.Context, string, string, time.Time) error
	GetSessionUser(context.Context, string) (string, bool, error)
	FinalizeOwnership(context.Context, string) error
}

// PhoneIdentityStore 在既有会话身份之上提供独立、唯一的手机号创建和查找。
type PhoneIdentityStore interface {
	IdentityStore
	CreatePhoneUser(context.Context, string) (model.User, error)
	GetUserByPhone(context.Context, string) (model.User, error)
}

type userIDContextKey struct{}

// WithUserID 把已由服务端会话确认的用户 ID 放入请求上下文，Controller 不接受客户端提交的所有者字段。
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDContextKey{}, userID)
}

// UserIDFromContext 读取服务端注入的用户 ID；缺失表示 Worker 或兼容旧测试上下文。
func UserIDFromContext(ctx context.Context) (string, bool) {
	userID, ok := ctx.Value(userIDContextKey{}).(string)
	return userID, ok && userID != ""
}

// EmailChallengeStore 负责邮箱验证码原子状态，账号创建由 IdentityStore 完成。
type EmailChallengeStore interface {
	ReserveEmailChallenge(context.Context, model.EmailChallenge, time.Time) (bool, error)
	ConsumeEmailChallenge(context.Context, string, string, string, time.Time) (bool, error)
	DeleteEmailChallenge(context.Context, string, string, string) error
}

// PhoneChallengeStore 负责数据库中的短信验证码限频、一次性消费和失败清理。
type PhoneChallengeStore interface {
	ReservePhoneChallenge(context.Context, model.PhoneChallenge, time.Time) (bool, error)
	ConsumePhoneChallenge(context.Context, string, string, string, time.Time) (bool, error)
	DeletePhoneChallenge(context.Context, string, string, string) error
}

// ProcessingStore 提供持久任务和派生产物的数据库边界，Worker 与 HTTP Service 共用它。
type ProcessingStore interface {
	FindReusableProcessingJob(context.Context, string, string, string) (model.ProcessingJob, error)
	CreateProcessingJob(context.Context, model.ProcessingJob) error
	GetProcessingJob(context.Context, string) (model.ProcessingJob, error)
	ListProcessingJobs(context.Context, string) ([]model.ProcessingJob, error)
	ClaimNextProcessingJob(context.Context, time.Time) (model.ProcessingJob, error)
	CompleteProcessingJob(context.Context, string, string, model.DerivedAsset) error
	FailProcessingJob(context.Context, string, string, string, *time.Time) error
	GetDerivedAsset(context.Context, string) (model.DerivedAsset, error)
}

// ProcessingOutboxStore 提供任务事件的事务写入、租约发布和失败隔离能力。
type ProcessingOutboxStore interface {
	CreateProcessingJobWithOutbox(context.Context, model.ProcessingJob) error
	ClaimProcessingOutbox(context.Context, time.Time) (model.ProcessingOutbox, error)
	MarkProcessingOutboxPublished(context.Context, string, string) error
	FailProcessingOutbox(context.Context, string, string, string) error
	RepairProcessingOutbox(context.Context, time.Time) (bool, error)
}

// ProcessingClaimStore 支持 RabbitMQ 按消息中的任务 ID 幂等领取任务。
type ProcessingClaimStore interface {
	ClaimProcessingJob(context.Context, string, time.Time) (model.ProcessingJob, error)
}

// ProcessingCapacityStore 原子限制待处理和执行中任务总数，适用于多个 API 进程并发提交。
type ProcessingCapacityStore interface {
	CreateProcessingJobWithinLimit(context.Context, model.ProcessingJob, int, bool) error
}
