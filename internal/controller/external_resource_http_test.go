package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
	"mizuki-archive/internal/service"
)

type externalHTTPStore struct {
	*multiUserHTTPStore
	cards  map[string]model.ExternalResource
	owners map[string]string
}

// CreateExternalResource 模拟数据库以会话用户作为卡片归属，绝不信任请求字段。
func (store *externalHTTPStore) CreateExternalResource(ctx context.Context, item model.ExternalResource) error {
	owner, _ := repository.UserIDFromContext(ctx)
	store.cards[item.ID], store.owners[item.ID] = item, owner
	return nil
}

// GetExternalResource 仅返回当前用户拥有的测试卡片。
func (store *externalHTTPStore) GetExternalResource(ctx context.Context, id string) (model.ExternalResource, error) {
	owner, _ := repository.UserIDFromContext(ctx)
	if owner != store.owners[id] || owner == "" {
		return model.ExternalResource{}, repository.ErrNotFound
	}
	return store.cards[id], nil
}

// ListExternalResources 排除其他用户卡片，验证路由始终提供服务端身份。
func (store *externalHTTPStore) ListExternalResources(ctx context.Context, _ string) ([]model.ExternalResource, error) {
	owner, _ := repository.UserIDFromContext(ctx)
	items := []model.ExternalResource{}
	for id, item := range store.cards {
		if store.owners[id] == owner {
			items = append(items, item)
		}
	}
	return items, nil
}

// UpdateExternalResource 模拟按归属更新，确保越权写请求返回统一的未找到。
func (store *externalHTTPStore) UpdateExternalResource(ctx context.Context, item model.ExternalResource) (model.ExternalResource, error) {
	previous, err := store.GetExternalResource(ctx, item.ID)
	if err != nil {
		return model.ExternalResource{}, err
	}
	item.CreatedAt = previous.CreatedAt
	item.OrganizationStatus = previous.OrganizationStatus
	store.cards[item.ID] = item
	return item, nil
}

// DeleteExternalResource 模拟按归属删除卡片，不影响其他用户。
func (store *externalHTTPStore) DeleteExternalResource(ctx context.Context, id string) error {
	if _, err := store.GetExternalResource(ctx, id); err != nil {
		return err
	}
	delete(store.cards, id)
	delete(store.owners, id)
	return nil
}

// externalRequest 以真实 Cookie 和 Origin 访问路由，模拟跨用户 HTTP 流程。
func externalRequest(server http.Handler, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if cookie != nil {
		request.AddCookie(cookie)
	}
	if method != http.MethodGet {
		request.Header.Set("Origin", "http://localhost:5173")
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

// TestExternalResourceHTTPIsolation 覆盖未登录、创建、跨用户读写删除、校验和用户自行删除。
func TestExternalResourceHTTPIsolation(t *testing.T) {
	base := &multiUserHTTPStore{resources: map[string]model.Resource{}, sessions: map[string]string{}, users: map[string]model.User{}}
	store := &externalHTTPStore{multiUserHTTPStore: base, cards: map[string]model.ExternalResource{}, owners: map[string]string{}}
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
	cards, err := service.NewExternalResources(store)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{Resources: files, ExternalResources: cards, Auth: auth, Origin: "http://localhost:5173"})
	if err != nil {
		t.Fatal(err)
	}
	userA := userCookie(t, auth, "user-a")
	userB := userCookie(t, auth, "user-b")
	if response := externalRequest(server, http.MethodGet, "/api/external-resources", "", nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous list: %d", response.Code)
	}
	input := `{"title":"教程","location":"https://example.org/a","resource_type":"文档","tags":["go"]}`
	created := externalRequest(server, http.MethodPost, "/api/external-resources", input, userB)
	if created.Code != http.StatusCreated || len(store.cards) != 1 {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var id string
	for key := range store.cards {
		id = key
	}
	for _, request := range []struct {
		method string
		path   string
		body   string
		want   int
	}{
		{http.MethodGet, "/api/external-resources", "", http.StatusOK},
		{http.MethodGet, "/api/external-resources/" + id, "", http.StatusNotFound},
		{http.MethodPut, "/api/external-resources/" + id, input, http.StatusNotFound},
		{http.MethodDelete, "/api/external-resources/" + id, "", http.StatusNotFound},
	} {
		response := externalRequest(server, request.method, request.path, request.body, userA)
		if response.Code != request.want || strings.Contains(response.Body.String(), "example.org") {
			t.Fatalf("user A %s %s: %d %s", request.method, request.path, response.Code, response.Body.String())
		}
	}
	if response := externalRequest(server, http.MethodGet, "/api/external-resources/"+id, "", userB); response.Code != http.StatusOK {
		t.Fatalf("owner read: %d %s", response.Code, response.Body.String())
	}
	// 只有归属用户能标记链接失效，且该状态可从详情和列表重新读取。
	statusUpdate := `{"title":"教程","location":"https://example.org/a","resource_type":"文档","status":"broken"}`
	if response := externalRequest(server, http.MethodPut, "/api/external-resources/"+id, statusUpdate, userB); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"broken"`) {
		t.Fatalf("owner status update: %d %s", response.Code, response.Body.String())
	}
	if response := externalRequest(server, http.MethodGet, "/api/external-resources/"+id, "", userB); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"broken"`) {
		t.Fatalf("owner status read: %d %s", response.Code, response.Body.String())
	}
	if response := externalRequest(server, http.MethodGet, "/api/external-resources", "", userB); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"broken"`) {
		t.Fatalf("owner status list: %d %s", response.Code, response.Body.String())
	}
	if response := externalRequest(server, http.MethodGet, "/api/external-resources", "", userA); response.Code != http.StatusOK || strings.Contains(response.Body.String(), `"status":"broken"`) {
		t.Fatalf("other user status list: %d %s", response.Code, response.Body.String())
	}
	invalid := externalRequest(server, http.MethodPut, "/api/external-resources/"+id, `{"title":"","location":"x","resource_type":"doc"}`, userB)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid edit: %d", invalid.Code)
	}
	if response := externalRequest(server, http.MethodDelete, "/api/external-resources/"+id, "", userB); response.Code != http.StatusNoContent {
		t.Fatalf("owner delete: %d", response.Code)
	}
}
