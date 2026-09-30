package service

import (
	"context"
	"errors"

	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
)

var ErrInboxIdentity = errors.New("missing verified inbox identity")
var ErrInvalidInboxInput = errors.New("invalid inbox input")

const inboxPageSize = 50

// InboxPage 分开呈现本地文件和外部卡片，避免误把外部链接当作站内下载。
type InboxPage struct {
	Files             []model.Resource         `json:"files"`
	ExternalResources []model.ExternalResource `json:"external_resources"`
	Page              int                      `json:"page"`
	HasMore           bool                     `json:"has_more"`
}

// Inbox 收拢两类资料的待整理视图，但不改变文件处理或外部链接状态。
type Inbox struct {
	store repository.InboxStore
}

// NewInbox 要求明确提供仓储依赖，不允许无持久化配置时打开入口。
func NewInbox(store repository.InboxStore) (*Inbox, error) {
	if store == nil {
		return nil, errors.New("missing inbox store")
	}
	return &Inbox{store: store}, nil
}

// List 对两类资料分别分页，检查会话身份并以额外一条结果判断是否还有下一页。
func (inbox *Inbox) List(ctx context.Context, page int) (InboxPage, error) {
	if _, ok := repository.UserIDFromContext(ctx); !ok {
		return InboxPage{}, ErrInboxIdentity
	}
	if page < 1 || page > 10000 {
		return InboxPage{}, ErrInvalidInboxInput
	}
	offset := (page - 1) * inboxPageSize
	files, err := inbox.store.ListPendingResources(ctx, inboxPageSize+1, offset)
	if err != nil {
		return InboxPage{}, err
	}
	externals, err := inbox.store.ListPendingExternalResources(ctx, inboxPageSize+1, offset)
	if err != nil {
		return InboxPage{}, err
	}
	hasMore := len(files) > inboxPageSize || len(externals) > inboxPageSize
	if len(files) > inboxPageSize {
		files = files[:inboxPageSize]
	}
	if len(externals) > inboxPageSize {
		externals = externals[:inboxPageSize]
	}
	return InboxPage{Files: files, ExternalResources: externals, Page: page, HasMore: hasMore}, nil
}

// SetStatus 仅允许服务端已验证用户在待整理与已整理间切换，非法来源或 ID 按无效输入处理。
func (inbox *Inbox) SetStatus(ctx context.Context, source, id, status string) error {
	if _, ok := repository.UserIDFromContext(ctx); !ok {
		return ErrInboxIdentity
	}
	if !validResourceID(id) || (status != "pending" && status != "organized") {
		return ErrInvalidInboxInput
	}
	switch source {
	case "file":
		return inbox.store.SetResourceOrganizationStatus(ctx, id, status)
	case "external":
		return inbox.store.SetExternalOrganizationStatus(ctx, id, status)
	default:
		return ErrInvalidInboxInput
	}
}

// BatchSetStatus 先拒绝空批次、重复项和超量请求，再由仓储在一个事务中检查归属并更新。
func (inbox *Inbox) BatchSetStatus(ctx context.Context, items []model.InboxSelection, status string) error {
	if _, ok := repository.UserIDFromContext(ctx); !ok {
		return ErrInboxIdentity
	}
	if (status != "pending" && status != "organized") || validateBatchSelections(items) != nil {
		return ErrInvalidInboxInput
	}
	return inbox.store.BatchSetOrganizationStatus(ctx, items, status)
}
