package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
	"mizuki-archive/internal/service"
)

type searchHTTPStore struct{ *externalHTTPStore }

type savedSearchHTTPStore struct {
	*searchHTTPStore
	views      map[string]model.SavedSearch
	viewOwners map[string]string
}

// CreateSavedSearch 仅在测试中存当前登录用户的视图，不复制检索结果。
func (store *savedSearchHTTPStore) CreateSavedSearch(ctx context.Context, view model.SavedSearch) error {
	owner, _ := repository.UserIDFromContext(ctx)
	store.views[view.ID] = view
	store.viewOwners[view.ID] = owner
	return nil
}

// ListSavedSearches 模拟视图归属过滤，验证 HTTP 层不会泄漏别人的筛选条件。
func (store *savedSearchHTTPStore) ListSavedSearches(ctx context.Context) ([]model.SavedSearch, error) {
	owner, _ := repository.UserIDFromContext(ctx)
	result := []model.SavedSearch{}
	for id, view := range store.views {
		if store.viewOwners[id] == owner {
			result = append(result, view)
		}
	}
	return result, nil
}

// DeleteSavedSearch 模拟跨用户与不存在统一返回未找到。
func (store *savedSearchHTTPStore) DeleteSavedSearch(ctx context.Context, id string) error {
	owner, _ := repository.UserIDFromContext(ctx)
	if store.viewOwners[id] != owner {
		return repository.ErrNotFound
	}
	delete(store.views, id)
	delete(store.viewOwners, id)
	return nil
}

// SearchFiles 只返回当前登录用户匹配的文件，模拟持久层的用户范围查询。
func (store *searchHTTPStore) SearchFiles(ctx context.Context, filter model.SearchFilter, _, _ int) ([]model.Resource, error) {
	owner, _ := repository.UserIDFromContext(ctx)
	items := []model.Resource{}
	for _, item := range store.resources {
		if item.OwnerID == owner && strings.Contains(item.Name, filter.Query) {
			items = append(items, item)
		}
	}
	return items, nil
}

// SearchExternal 只返回当前登录用户匹配的外部卡片，避免通过搜索枚举他人内容。
func (store *searchHTTPStore) SearchExternal(ctx context.Context, filter model.SearchFilter, _, _ int) ([]model.ExternalResource, error) {
	owner, _ := repository.UserIDFromContext(ctx)
	items := []model.ExternalResource{}
	for id, item := range store.cards {
		if store.owners[id] == owner && strings.Contains(item.Note, filter.Query) {
			items = append(items, item)
		}
	}
	return items, nil
}

// TestUnifiedSearchHTTP 验证会话、无效输入、返回来源分组与跨用户私有数据边界。
func TestUnifiedSearchHTTP(t *testing.T) {
	base := &multiUserHTTPStore{resources: map[string]model.Resource{}, sessions: map[string]string{}, users: map[string]model.User{}}
	store := &savedSearchHTTPStore{searchHTTPStore: &searchHTTPStore{externalHTTPStore: &externalHTTPStore{multiUserHTTPStore: base, cards: map[string]model.ExternalResource{}, owners: map[string]string{}}}, views: map[string]model.SavedSearch{}, viewOwners: map[string]string{}}
	hash, err := bcrypt.GenerateFromPassword([]byte("correct horse battery staple"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := service.NewAuth(store, "owner", hash)
	if err != nil {
		t.Fatal(err)
	}
	files, err := service.NewResources(store, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	search, err := service.NewSearch(store)
	if err != nil {
		t.Fatal(err)
	}
	views, err := service.NewSavedSearches(store)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{Resources: files, Search: search, SavedSearches: views, Auth: auth, Origin: "http://localhost:5173"})
	if err != nil {
		t.Fatal(err)
	}
	store.resources["file-a"] = model.Resource{ID: "file-a", OwnerID: "user-a", Name: "课程文件"}
	store.resources["file-b"] = model.Resource{ID: "file-b", OwnerID: "user-b", Name: "课程私有"}
	store.cards["card-a"] = model.ExternalResource{ID: "card-a", Title: "课外卡片", Note: "课程笔记"}
	store.owners["card-a"] = "user-a"
	store.cards["card-b"] = model.ExternalResource{ID: "card-b", Title: "他人卡片", Note: "课程笔记"}
	store.owners["card-b"] = "user-b"
	if response := externalRequest(server, http.MethodGet, "/api/search?q=课程", "", nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous search: %d", response.Code)
	}
	owner := userCookie(t, auth, "user-a")
	for _, path := range []string{"/api/search?q=", "/api/search?q=课程&page=0", "/api/search?q=课程&page=1001", "/api/search?q=课程%0A"} {
		if response := externalRequest(server, http.MethodGet, path, "", owner); response.Code != http.StatusBadRequest {
			t.Fatalf("invalid search %s: %d", path, response.Code)
		}
	}
	response := externalRequest(server, http.MethodGet, "/api/search?q=课程", "", owner)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "file-a") || !strings.Contains(response.Body.String(), "card-a") || strings.Contains(response.Body.String(), "file-b") || strings.Contains(response.Body.String(), "card-b") {
		t.Fatalf("private grouped search: %d %s", response.Code, response.Body.String())
	}
	// 高频查询只限制当前账号；另一账号仍可独立搜索自己的资料。
	limited := false
	for attempt := 0; attempt < 35; attempt++ {
		if result := externalRequest(server, http.MethodGet, "/api/search?q=课程", "", owner); result.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("repeated search was not rate limited")
	}
	other := userCookie(t, auth, "user-b")
	if result := externalRequest(server, http.MethodGet, "/api/search?q=课程", "", other); result.Code != http.StatusOK || strings.Contains(result.Body.String(), "file-a") {
		t.Fatalf("other user's search after rate limit: %d %s", result.Code, result.Body.String())
	}
	if result := externalRequest(server, http.MethodGet, "/api/search/views", "", nil); result.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous saved views: %d", result.Code)
	}
	if result := externalRequest(server, http.MethodPost, "/api/search/views", `{"name":"空条件","filter":{}}`, owner); result.Code != http.StatusBadRequest {
		t.Fatalf("invalid saved view: %d", result.Code)
	}
	created := externalRequest(server, http.MethodPost, "/api/search/views", `{"name":"待整理课程","filter":{"source":"file","organization_status":"pending"}}`, owner)
	if created.Code != http.StatusCreated {
		t.Fatalf("create saved view: %d %s", created.Code, created.Body.String())
	}
	var body struct {
		Data model.SavedSearch `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &body); err != nil || len(body.Data.ID) != 32 {
		t.Fatalf("saved view response: %+v, %v", body, err)
	}
	if result := externalRequest(server, http.MethodGet, "/api/search/views", "", other); result.Code != http.StatusOK || strings.Contains(result.Body.String(), body.Data.ID) {
		t.Fatalf("other user's views: %d %s", result.Code, result.Body.String())
	}
	path := "/api/search/views/" + body.Data.ID
	if result := externalRequest(server, http.MethodDelete, path, "", other); result.Code != http.StatusNotFound {
		t.Fatalf("cross-user view delete: %d", result.Code)
	}
	if result := externalRequest(server, http.MethodDelete, path, "", owner); result.Code != http.StatusNoContent {
		t.Fatalf("owner view delete: %d", result.Code)
	}
}
