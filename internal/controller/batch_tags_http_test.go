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

// batchTagsHTTPStore 只模拟事务的全有或全无行为，用来验证 HTTP 边界而非替代真实 MySQL 测试。
type batchTagsHTTPStore struct {
	*inboxHTTPStore
	changed []model.InboxSelection
}

// BatchUpdateTags 先逐项核验当前登录用户，再返回真实变化项；越权不产生部分结果。
func (store *batchTagsHTTPStore) BatchUpdateTags(ctx context.Context, items []model.InboxSelection, _, _ string) ([]model.InboxSelection, error) {
	owner, _ := repository.UserIDFromContext(ctx)
	for _, item := range items {
		if item.Source == "file" {
			file, ok := store.resources[item.ID]
			if !ok || file.OwnerID != owner {
				return nil, repository.ErrNotFound
			}
		} else if store.owners[item.ID] != owner || owner == "" {
			return nil, repository.ErrNotFound
		}
	}
	return store.changed, nil
}

// BatchUpdateFavorites 复用同一归属检查，模拟收藏接口的全批校验和变化返回。
func (store *batchTagsHTTPStore) BatchUpdateFavorites(ctx context.Context, items []model.InboxSelection, _ bool) ([]model.InboxSelection, error) {
	owner, _ := repository.UserIDFromContext(ctx)
	for _, item := range items {
		if item.Source == "file" {
			file, ok := store.resources[item.ID]
			if !ok || file.OwnerID != owner {
				return nil, repository.ErrNotFound
			}
		} else if store.owners[item.ID] != owner || owner == "" {
			return nil, repository.ErrNotFound
		}
	}
	return items, nil
}

// TestBatchTagsHTTPBoundary 验证匿名、越权混合批次、无效输入、变化数量和请求限流。
func TestBatchTagsHTTPBoundary(t *testing.T) {
	base := &multiUserHTTPStore{resources: map[string]model.Resource{}, sessions: map[string]string{}, users: map[string]model.User{}}
	external := &externalHTTPStore{multiUserHTTPStore: base, cards: map[string]model.ExternalResource{}, owners: map[string]string{}}
	store := &batchTagsHTTPStore{inboxHTTPStore: &inboxHTTPStore{externalHTTPStore: external}}
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
	batch, err := service.NewBatchTags(store)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{Resources: resources, Auth: auth, BatchTags: batch, Origin: "http://localhost:5173"})
	if err != nil {
		t.Fatal(err)
	}
	ownerA := userCookie(t, auth, "user-a")
	ownerB := userCookie(t, auth, "user-b")
	fileID, cardID := strings.Repeat("a", 32), strings.Repeat("b", 32)
	store.resources[fileID] = model.Resource{ID: fileID, OwnerID: "user-a"}
	store.owners[cardID] = "user-b"
	path := "/api/batch/tags"
	valid := `{"mode":"add","tag":"学习","items":[{"source":"file","id":"` + fileID + `"}]}`
	if response := externalRequest(server, http.MethodPatch, path, valid, nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", response.Code)
	}
	mixed := `{"mode":"add","tag":"学习","items":[{"source":"file","id":"` + fileID + `"},{"source":"external","id":"` + cardID + `"}]}`
	if response := externalRequest(server, http.MethodPatch, path, mixed, ownerA); response.Code != http.StatusNotFound {
		t.Fatalf("mixed owner: %d", response.Code)
	}
	if response := externalRequest(server, http.MethodPatch, path, valid, ownerB); response.Code != http.StatusNotFound {
		t.Fatalf("foreign file: %d", response.Code)
	}
	for _, body := range []string{`{"items":[]}`, `{"mode":"add","tag":"学习","items":[{"source":"file","id":"` + fileID + `"},{"source":"file","id":"` + fileID + `"}]}`, valid + ` {}`, valid + strings.Repeat(" ", 9<<10)} {
		if response := externalRequest(server, http.MethodPatch, path, body, ownerA); response.Code != http.StatusBadRequest {
			t.Fatalf("invalid input: %d", response.Code)
		}
	}
	store.changed = []model.InboxSelection{{Source: "file", ID: fileID}}
	if response := externalRequest(server, http.MethodPatch, path, valid, ownerA); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"count":1`) || !strings.Contains(response.Body.String(), `"source":"file","id":"`+fileID+`"`) {
		t.Fatalf("changed: %d %s", response.Code, response.Body.String())
	}
	for attempt := 0; attempt < 21; attempt++ {
		response := externalRequest(server, http.MethodPatch, path, valid, ownerB)
		if attempt == 20 && response.Code != http.StatusTooManyRequests {
			t.Fatalf("rate limit: %d", response.Code)
		}
	}
}

// TestBatchFavoritesHTTPBoundary 验证收藏接口的匿名、混合越权、明确目标和尾随 JSON 拒绝。
func TestBatchFavoritesHTTPBoundary(t *testing.T) {
	base := &multiUserHTTPStore{resources: map[string]model.Resource{}, sessions: map[string]string{}, users: map[string]model.User{}}
	external := &externalHTTPStore{multiUserHTTPStore: base, cards: map[string]model.ExternalResource{}, owners: map[string]string{}}
	store := &batchTagsHTTPStore{inboxHTTPStore: &inboxHTTPStore{externalHTTPStore: external}}
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
	favorites, err := service.NewBatchFavorites(store)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{Resources: resources, Auth: auth, BatchFavorites: favorites, Origin: "http://localhost:5173"})
	if err != nil {
		t.Fatal(err)
	}
	ownerA, ownerB := userCookie(t, auth, "user-a"), userCookie(t, auth, "user-b")
	fileID, cardID := strings.Repeat("c", 32), strings.Repeat("d", 32)
	store.resources[fileID] = model.Resource{ID: fileID, OwnerID: "user-a"}
	store.owners[cardID] = "user-b"
	path := "/api/batch/favorites"
	valid := `{"favorite":true,"items":[{"source":"file","id":"` + fileID + `"}]}`
	if response := externalRequest(server, http.MethodPatch, path, valid, nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", response.Code)
	}
	mixed := `{"favorite":true,"items":[{"source":"file","id":"` + fileID + `"},{"source":"external","id":"` + cardID + `"}]}`
	if response := externalRequest(server, http.MethodPatch, path, mixed, ownerA); response.Code != http.StatusNotFound {
		t.Fatalf("mixed owner: %d", response.Code)
	}
	if response := externalRequest(server, http.MethodPatch, path, valid+` {}`, ownerA); response.Code != http.StatusBadRequest {
		t.Fatalf("trailing json: %d", response.Code)
	}
	for _, body := range []string{valid + strings.Repeat(" ", 9<<10), `{"favorite":null,"items":[{"source":"file","id":"` + fileID + `"}]}`, `{"favorite":"yes","items":[{"source":"file","id":"` + fileID + `"}]}`, `{"favorite":true,"items":[{"source":"file","id":"` + fileID + `"},{"source":"file","id":"` + fileID + `"}]}`} {
		if response := externalRequest(server, http.MethodPatch, path, body, ownerA); response.Code != http.StatusBadRequest {
			t.Fatalf("invalid favorite request: %d %s", response.Code, response.Body.String())
		}
	}
	if response := externalRequest(server, http.MethodPatch, path, valid, ownerA); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"count":1`) {
		t.Fatalf("valid: %d %s", response.Code, response.Body.String())
	}
	if response := externalRequest(server, http.MethodPatch, path, `{"items":[{"source":"file","id":"`+fileID+`"}]}`, ownerA); response.Code != http.StatusBadRequest {
		t.Fatalf("missing target: %d", response.Code)
	}
	for attempt := 0; attempt < 21; attempt++ {
		response := externalRequest(server, http.MethodPatch, path, valid, ownerB)
		if attempt == 20 && response.Code != http.StatusTooManyRequests {
			t.Fatalf("rate limit: %d", response.Code)
		}
	}
}
