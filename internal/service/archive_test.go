package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
)

// archiveFake 验证服务层校验与分页边界，不连接真实数据库。
type archiveFake struct {
	items    []model.InboxSelection
	archived bool
}

// ListArchivedResources 返回空文件列表，使分页测试关注用户身份和页码。
func (store *archiveFake) ListArchivedResources(context.Context, int, int) ([]model.Resource, error) {
	return []model.Resource{}, nil
}

// ListArchivedExternalResources 返回空卡片列表，使分页测试不依赖文件系统。
func (store *archiveFake) ListArchivedExternalResources(context.Context, int, int) ([]model.ExternalResource, error) {
	return []model.ExternalResource{}, nil
}

// BatchSetArchived 记录服务层传下的明确目标值。
func (store *archiveFake) BatchSetArchived(_ context.Context, items []model.InboxSelection, archived bool) ([]model.InboxSelection, error) {
	store.items, store.archived = items, archived
	return items, nil
}

// TestArchiveValidation 拒绝匿名、非法页码、重复和超量条目，确认恢复目标不会反转。
func TestArchiveValidation(t *testing.T) {
	store := &archiveFake{}
	archive, err := NewArchive(store)
	if err != nil {
		t.Fatal(err)
	}
	item := model.InboxSelection{Source: "file", ID: strings.Repeat("a", 32)}
	owner := repository.WithUserID(context.Background(), "owner")
	if _, err := archive.List(context.Background(), 1); !errors.Is(err, ErrInboxIdentity) {
		t.Fatalf("anonymous list: %v", err)
	}
	for _, page := range []int{0, -1, 10001} {
		if _, err := archive.List(owner, page); !errors.Is(err, ErrInvalidInboxInput) {
			t.Fatalf("invalid page %d: %v", page, err)
		}
	}
	if result, err := archive.List(owner, 1); err != nil || len(result.Files) != 0 {
		t.Fatalf("empty list: %+v %v", result, err)
	}
	if _, err := archive.Set(context.Background(), []model.InboxSelection{item}, true); !errors.Is(err, ErrInboxIdentity) {
		t.Fatalf("anonymous write: %v", err)
	}
	for _, items := range [][]model.InboxSelection{nil, {item, item}, {{Source: "file", ID: "bad"}}, make([]model.InboxSelection, 51)} {
		if _, err := archive.Set(owner, items, true); !errors.Is(err, ErrInvalidInboxInput) {
			t.Fatalf("invalid batch: %v", err)
		}
	}
	if _, err := archive.Set(owner, []model.InboxSelection{item}, true); err != nil || !store.archived {
		t.Fatalf("archive: %v", err)
	}
	if _, err := archive.Set(owner, []model.InboxSelection{item}, false); err != nil || store.archived {
		t.Fatalf("restore: %v", err)
	}
}
