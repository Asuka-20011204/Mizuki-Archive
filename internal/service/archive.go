package service

import (
	"context"
	"errors"

	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
)

// ArchivePage 将归档文件和外部卡片分开分页，避免把外部位置冒充本站原件。
type ArchivePage struct {
	Files             []model.Resource         `json:"files"`
	ExternalResources []model.ExternalResource `json:"external_resources"`
	Page              int                      `json:"page"`
	HasMore           bool                     `json:"has_more"`
}

// Archive 处理归档与恢复，不触碰原件、标签、整理或链接状态。
type Archive struct{ store repository.ArchiveStore }

// NewArchive 仅在提供用户范围仓储时开放归档入口。
func NewArchive(store repository.ArchiveStore) (*Archive, error) {
	if store == nil {
		return nil, errors.New("missing archive store")
	}
	return &Archive{store: store}, nil
}

// List 限制用户、页码和每类结果长度，额外取一项判定下一页。
func (archive *Archive) List(ctx context.Context, page int) (ArchivePage, error) {
	if _, ok := repository.UserIDFromContext(ctx); !ok {
		return ArchivePage{}, ErrInboxIdentity
	}
	if page < 1 || page > 10000 {
		return ArchivePage{}, ErrInvalidInboxInput
	}
	offset := (page - 1) * inboxPageSize
	files, err := archive.store.ListArchivedResources(ctx, inboxPageSize+1, offset)
	if err != nil {
		return ArchivePage{}, err
	}
	cards, err := archive.store.ListArchivedExternalResources(ctx, inboxPageSize+1, offset)
	if err != nil {
		return ArchivePage{}, err
	}
	more := len(files) > inboxPageSize || len(cards) > inboxPageSize
	if len(files) > inboxPageSize {
		files = files[:inboxPageSize]
	}
	if len(cards) > inboxPageSize {
		cards = cards[:inboxPageSize]
	}
	return ArchivePage{Files: files, ExternalResources: cards, Page: page, HasMore: more}, nil
}

// Set 在服务端归属范围内批量设置归档目标，返回真实变化项供单次反向操作。
func (archive *Archive) Set(ctx context.Context, items []model.InboxSelection, archived bool) ([]model.InboxSelection, error) {
	if _, ok := repository.UserIDFromContext(ctx); !ok {
		return nil, ErrInboxIdentity
	}
	if err := validateBatchSelections(items); err != nil {
		return nil, err
	}
	return archive.store.BatchSetArchived(ctx, items, archived)
}
