package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
)

// batchDeleteFake 记录服务层只向持久层提交已校验的选择，并模拟事务返回的文件 ID。
type batchDeleteFake struct {
	items   []model.InboxSelection
	files   repository.BatchDeletedFiles
	err     error
	pending []repository.PendingFileCleanup
}

// BatchDeleteEntries 返回成功提交的文件 ID，错误时不应触发文件清理。
func (store *batchDeleteFake) BatchDeleteEntries(_ context.Context, items []model.InboxSelection) (repository.BatchDeletedFiles, error) {
	store.items = items
	if store.err == nil {
		for _, id := range store.files.OriginalIDs {
			store.pending = append(store.pending, repository.PendingFileCleanup{Kind: "original", ID: id})
		}
		for _, id := range store.files.DerivedIDs {
			store.pending = append(store.pending, repository.PendingFileCleanup{Kind: "derived", ID: id})
		}
	}
	return store.files, store.err
}

// ListPendingFileCleanup 返回尚未完成的清理项，供测试覆盖下次重试。
func (store *batchDeleteFake) ListPendingFileCleanup(_ context.Context, limit int) ([]repository.PendingFileCleanup, error) {
	if len(store.pending) < limit {
		limit = len(store.pending)
	}
	return append([]repository.PendingFileCleanup(nil), store.pending[:limit]...), nil
}

// CompleteFileCleanup 仅从测试账本移除已成功删除文件的那一项。
func (store *batchDeleteFake) CompleteFileCleanup(_ context.Context, entry repository.PendingFileCleanup) error {
	for index, existing := range store.pending {
		if existing == entry {
			store.pending = append(store.pending[:index], store.pending[index+1:]...)
			break
		}
	}
	return nil
}

// DeferFileCleanup 记录失败项可重试，不从测试账本中误删。
func (store *batchDeleteFake) DeferFileCleanup(context.Context, repository.PendingFileCleanup) error {
	return nil
}

// TestBatchDeleteValidation 拒绝匿名、非法和重复条目，不调用持久层。
func TestBatchDeleteValidation(t *testing.T) {
	store := &batchDeleteFake{}
	batch, err := NewBatchDelete(store, &Resources{dataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	item := model.InboxSelection{Source: "file", ID: strings.Repeat("a", 32)}
	owner := repository.WithUserID(context.Background(), "owner")
	if _, err := batch.Apply(context.Background(), []model.InboxSelection{item}); !errors.Is(err, ErrInboxIdentity) {
		t.Fatalf("anonymous: %v", err)
	}
	for _, items := range [][]model.InboxSelection{nil, {item, item}, {{Source: "file", ID: "bad"}}, make([]model.InboxSelection, 51)} {
		if _, err := batch.Apply(owner, items); !errors.Is(err, ErrInvalidInboxInput) {
			t.Fatalf("invalid items: %v", err)
		}
	}
	if len(store.items) != 0 {
		t.Fatal("invalid batch reached repository")
	}
}

// TestBatchDeleteCleanup 在事务成功后清理站内原件；清理失败仍报告已提交数和待清理数。
func TestBatchDeleteCleanup(t *testing.T) {
	root := t.TempDir()
	first, second := strings.Repeat("a", 32), strings.Repeat("b", 32)
	if err := os.WriteFile(filepath.Join(root, first), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, second), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, second, "keep"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	derivedID := strings.Repeat("d", 32)
	if err := os.Mkdir(filepath.Join(root, "derived"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "derived", derivedID), []byte("derived fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	store := &batchDeleteFake{files: repository.BatchDeletedFiles{OriginalIDs: []string{first, second}, DerivedIDs: []string{derivedID}}}
	batch, err := NewBatchDelete(store, &Resources{dataDir: root})
	if err != nil {
		t.Fatal(err)
	}
	items := []model.InboxSelection{{Source: "file", ID: first}, {Source: "file", ID: second}, {Source: "external", ID: strings.Repeat("c", 32)}}
	result, err := batch.Apply(repository.WithUserID(context.Background(), "owner"), items)
	if err != nil || result.Deleted != 3 || result.CleanupPending != 1 {
		t.Fatalf("batch result: %+v %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(root, first)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("first file not removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "derived", derivedID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("derived asset not removed: %v", err)
	}
	if len(store.pending) != 1 || store.pending[0].ID != second {
		t.Fatalf("failed cleanup lost durable retry: %+v", store.pending)
	}
	if err := os.Remove(filepath.Join(root, second, "keep")); err != nil {
		t.Fatal(err)
	}
	completed, pending, err := batch.DrainPendingCleanup(context.Background(), 10)
	if err != nil || completed != 1 || pending != 0 || len(store.pending) != 0 {
		t.Fatalf("retry pending cleanup: %d %d %+v %v", completed, pending, store.pending, err)
	}
	store.err = repository.ErrNotFound
	if _, err := batch.Apply(repository.WithUserID(context.Background(), "owner"), items); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("rejected batch: %v", err)
	}
}
