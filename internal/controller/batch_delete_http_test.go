package controller

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
	"mizuki-archive/internal/service"
)

// batchDeleteHTTPStore 模拟事务在全部核对归属后才修改，真实事务另由 MySQL 集成测试覆盖。
type batchDeleteHTTPStore struct{ *archiveHTTPStore }

// BatchDeleteEntries 无权或缺失时返回同一种错误，合法批次才一次性隐藏全部条目。
func (store *batchDeleteHTTPStore) BatchDeleteEntries(ctx context.Context, items []model.InboxSelection) (repository.BatchDeletedFiles, error) {
	owner, _ := repository.UserIDFromContext(ctx)
	for _, item := range items {
		if item.Source == "file" {
			file, ok := store.resources[item.ID]
			if !ok || file.OwnerID != owner {
				return repository.BatchDeletedFiles{}, repository.ErrNotFound
			}
		} else if store.owners[item.ID] != owner || owner == "" {
			return repository.BatchDeletedFiles{}, repository.ErrNotFound
		}
	}
	files := []string{}
	for _, item := range items {
		if item.Source == "file" {
			files = append(files, item.ID)
			delete(store.resources, item.ID)
		} else {
			delete(store.cards, item.ID)
		}
	}
	return repository.BatchDeletedFiles{OriginalIDs: files}, nil
}

// ListPendingFileCleanup 让 HTTP 测试只检验请求边界，不运行后台清理循环。
func (store *batchDeleteHTTPStore) ListPendingFileCleanup(context.Context, int) ([]repository.PendingFileCleanup, error) {
	return nil, nil
}

// CompleteFileCleanup 模拟成功确认文件清理，真实数据库记录另由仓储测试验证。
func (store *batchDeleteHTTPStore) CompleteFileCleanup(context.Context, repository.PendingFileCleanup) error {
	return nil
}

// DeferFileCleanup 由服务层在存储暂时不可用时调用，HTTP 测试不模拟重试时间。
func (store *batchDeleteHTTPStore) DeferFileCleanup(context.Context, repository.PendingFileCleanup) error {
	return nil
}

// TestBatchDeleteHTTPBoundary 验证匿名、确认标识、严格 JSON、跨账号原子拒绝和成功数量。
func TestBatchDeleteHTTPBoundary(t *testing.T) {
	base := &multiUserHTTPStore{resources: map[string]model.Resource{}, sessions: map[string]string{}, users: map[string]model.User{}}
	external := &externalHTTPStore{multiUserHTTPStore: base, cards: map[string]model.ExternalResource{}, owners: map[string]string{}}
	store := &batchDeleteHTTPStore{archiveHTTPStore: &archiveHTTPStore{inboxHTTPStore: &inboxHTTPStore{externalHTTPStore: external}}}
	hash, err := bcrypt.GenerateFromPassword([]byte("test-only password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := service.NewAuth(store, "owner", hash)
	if err != nil {
		t.Fatal(err)
	}
	resources, err := service.NewResources(store, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	batch, err := service.NewBatchDelete(store, resources)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{Resources: resources, Auth: auth, BatchDelete: batch, Origin: "http://localhost:5173"})
	if err != nil {
		t.Fatal(err)
	}
	ownerA, ownerB := userCookie(t, auth, "user-a"), userCookie(t, auth, "user-b")
	fileID, cardID, foreignID := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32)
	store.resources[fileID] = model.Resource{ID: fileID, OwnerID: "user-a"}
	store.resources[foreignID] = model.Resource{ID: foreignID, OwnerID: "user-b"}
	store.cards[cardID] = model.ExternalResource{ID: cardID}
	store.owners[cardID] = "user-a"
	path := "/api/batch/delete"
	body := `{"confirm":"DELETE","items":[{"source":"file","id":"` + fileID + `"},{"source":"external","id":"` + cardID + `"}]}`
	if response := externalRequest(server, http.MethodPost, path, body, nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", response.Code)
	}
	for _, invalid := range []string{strings.Replace(body, "DELETE", "YES", 1), body + `{}`, body + strings.Repeat(" ", 9<<10), strings.Replace(body, `"confirm":"DELETE",`, "", 1), `{"confirm":"DELETE","items":[{"source":"file","id":"` + fileID + `"},{"source":"file","id":"` + fileID + `"}]}`} {
		if response := externalRequest(server, http.MethodPost, path, invalid, ownerA); response.Code != http.StatusBadRequest {
			t.Fatalf("invalid body: %d", response.Code)
		}
	}
	foreign := strings.Replace(body, cardID, foreignID, 1)
	foreign = strings.Replace(foreign, `"source":"external"`, `"source":"file"`, 1)
	if response := externalRequest(server, http.MethodPost, path, foreign, ownerA); response.Code != http.StatusNotFound || len(store.resources) != 2 {
		t.Fatalf("foreign batch modified data: %d", response.Code)
	}
	if response := externalRequest(server, http.MethodPost, path, body, ownerB); response.Code != http.StatusNotFound || len(store.cards) != 1 {
		t.Fatalf("foreign card batch modified data: %d", response.Code)
	}
	if response := externalRequest(server, http.MethodPost, path, body, ownerA); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"deleted":2`) {
		t.Fatalf("delete: %d %s", response.Code, response.Body.String())
	}
	if _, ok := store.resources[fileID]; ok {
		t.Fatal("file remained after successful batch")
	}
	if _, ok := store.cards[cardID]; ok {
		t.Fatal("card remained after successful batch")
	}
}
