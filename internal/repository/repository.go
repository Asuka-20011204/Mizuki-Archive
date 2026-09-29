// Package repository 封装持久化边界；Service 只依赖 Store 行为，不直接构造 SQL。
package repository

import (
	"context"
	"errors"
	"time"

	"mizuki-archive/internal/model"
)

var ErrNotFound = errors.New("resource not found")

// Store 包含首期资料和会话的持久化操作，测试可用内存实现替换 MySQL。
type Store interface {
	SaveResource(context.Context, model.Resource) error
	GetResource(context.Context, string) (model.Resource, error)
	SetFavorite(context.Context, string, bool) (model.Resource, error)
	ListResources(context.Context, model.ListQuery) ([]model.Resource, error)
	SaveSession(context.Context, string, time.Time) error
	HasSession(context.Context, string) (bool, error)
	DeleteSession(context.Context, string) error
}
