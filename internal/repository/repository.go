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
