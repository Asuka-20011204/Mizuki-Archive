package controller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
	"mizuki-archive/internal/service"
)

type multiUserHTTPStore struct {
	resources map[string]model.Resource
	sessions  map[string]string
	users     map[string]model.User
}

// SaveResource 保存带服务端用户归属的 HTTP 测试资料。
func (store *multiUserHTTPStore) SaveResource(ctx context.Context, resource model.Resource) error {
	userID, ok := repository.UserIDFromContext(ctx)
	if !ok {
		return errors.New("missing test user")
	}
	resource.OwnerID = userID
	store.resources[resource.ID] = resource
	return nil
}

// GetResource 只返回当前会话用户拥有且未删除的资料。
func (store *multiUserHTTPStore) GetResource(ctx context.Context, id string) (model.Resource, error) {
	resource, exists := store.resources[id]
	userID, scoped := repository.UserIDFromContext(ctx)
	if !exists || !scoped || resource.OwnerID != userID {
		return model.Resource{}, repository.ErrNotFound
	}
	return resource, nil
}

// SetFavorite 只允许资料所属用户修改收藏状态。
func (store *multiUserHTTPStore) SetFavorite(ctx context.Context, id string, favorite bool) (model.Resource, error) {
	resource, err := store.GetResource(ctx, id)
	if err != nil {
		return model.Resource{}, err
	}
	resource.Favorite = favorite
	store.resources[id] = resource
	return resource, nil
}

// ReplaceResourceTags 只替换当前用户资料的整组标签。
func (store *multiUserHTTPStore) ReplaceResourceTags(ctx context.Context, id string, tags []string) (model.Resource, error) {
	resource, err := store.GetResource(ctx, id)
	if err != nil {
		return model.Resource{}, err
	}
	resource.Tags = append([]string(nil), tags...)
	store.resources[id] = resource
	return resource, nil
}

// ListTags 汇总当前用户资料中的标签建议，不返回其他用户的标签。
func (store *multiUserHTTPStore) ListTags(ctx context.Context, search string) ([]string, error) {
	userID, ok := repository.UserIDFromContext(ctx)
	if !ok {
		return nil, errors.New("missing test user")
	}
	seen := map[string]bool{}
	result := make([]string, 0)
	for _, resource := range store.resources {
		if resource.OwnerID != userID {
			continue
		}
		for _, tag := range resource.Tags {
			if strings.Contains(tag, search) && !seen[tag] {
				seen[tag] = true
				result = append(result, tag)
			}
		}
	}
	return result, nil
}

// UpdateResourceName 只更新当前用户资料的展示名称。
func (store *multiUserHTTPStore) UpdateResourceName(ctx context.Context, id, name string) (model.Resource, error) {
	resource, err := store.GetResource(ctx, id)
	if err != nil {
		return model.Resource{}, err
	}
	resource.Name = name
	store.resources[id] = resource
	return resource, nil
}

// DeleteResource 只软删除当前用户拥有的资料。
func (store *multiUserHTTPStore) DeleteResource(ctx context.Context, id string) (model.Resource, error) {
	resource, err := store.GetResource(ctx, id)
	if err != nil {
		return model.Resource{}, err
	}
	delete(store.resources, id)
	return resource, nil
}

// ListResources 按当前用户范围执行最小的列表筛选，模拟真实 Repository 的权限边界。
func (store *multiUserHTTPStore) ListResources(ctx context.Context, query model.ListQuery) ([]model.Resource, error) {
	userID, ok := repository.UserIDFromContext(ctx)
	if !ok {
		return nil, errors.New("missing test user")
	}
	result := make([]model.Resource, 0)
	for _, resource := range store.resources {
		if resource.OwnerID != userID || (query.Kind != "" && resource.Kind != query.Kind) {
			continue
		}
		if query.Search != "" && !strings.Contains(strings.ToLower(resource.Name), strings.ToLower(query.Search)) {
			continue
		}
		if query.Tag != "" && !containsString(resource.Tags, query.Tag) {
			continue
		}
		result = append(result, resource)
	}
	return result, nil
}

// SaveSession 保留兼容密码登录所需的旧会话接口。
func (store *multiUserHTTPStore) SaveSession(context.Context, string, time.Time) error { return nil }

// HasSession 保留兼容密码登录所需的旧会话接口。
func (store *multiUserHTTPStore) HasSession(context.Context, string) (bool, error) { return false, nil }

// DeleteSession 保留兼容密码登录所需的旧会话接口。
func (store *multiUserHTTPStore) DeleteSession(context.Context, string) error { return nil }

// EnsureAdminUser 提供迁移期管理员用户的最小测试实现。
func (store *multiUserHTTPStore) EnsureAdminUser(_ context.Context, username string, hash []byte) (model.User, error) {
	user := model.User{ID: "admin", Username: username, PasswordHash: append([]byte(nil), hash...)}
	store.users[user.ID] = user
	return user, nil
}

// CreateUser 提供测试身份创建能力，并复用邮箱唯一约束语义。
func (store *multiUserHTTPStore) CreateUser(_ context.Context, email string) (model.User, error) {
	for _, user := range store.users {
		if user.Email == email {
			return model.User{}, repository.ErrUserExists
		}
	}
	user := model.User{ID: "user-" + email, Username: email, Email: email}
	store.users[user.ID] = user
	return user, nil
}

// GetUserByEmail 按邮箱读取测试用户。
func (store *multiUserHTTPStore) GetUserByEmail(_ context.Context, email string) (model.User, error) {
	for _, user := range store.users {
		if user.Email == email {
			return user, nil
		}
	}
	return model.User{}, repository.ErrNotFound
}

// GetUserByID 按会话中的服务端用户 ID 读取测试用户。
func (store *multiUserHTTPStore) GetUserByID(_ context.Context, id string) (model.User, error) {
	user, exists := store.users[id]
	if !exists {
		return model.User{}, repository.ErrNotFound
	}
	return user, nil
}

// SaveUserSession 记录随机会话令牌的用户归属。
func (store *multiUserHTTPStore) SaveUserSession(_ context.Context, userID, hash string, _ time.Time) error {
	store.sessions[hash] = userID
	return nil
}

// GetSessionUser 返回会话绑定的用户，模拟数据库对过期会话的过滤结果。
func (store *multiUserHTTPStore) GetSessionUser(_ context.Context, hash string) (string, bool, error) {
	userID, exists := store.sessions[hash]
	return userID, exists, nil
}

// FinalizeOwnership 提供启动迁移所需的测试接口。
func (store *multiUserHTTPStore) FinalizeOwnership(context.Context, string) error { return nil }

// containsString 判断测试资料是否包含目标标签。
func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// newMultiUserHTTPServer 创建不连接真实数据库的多用户 HTTP 隔离测试服务器。
func newMultiUserHTTPServer(t *testing.T) (http.Handler, *service.Auth, *multiUserHTTPStore, string) {
	t.Helper()
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("correct horse battery staple"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	store := &multiUserHTTPStore{resources: map[string]model.Resource{}, sessions: map[string]string{}, users: map[string]model.User{}}
	auth, err := service.NewAuth(store, "owner", passwordHash)
	if err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	resources, err := service.NewResources(store, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{Resources: resources, Auth: auth, Origin: "http://localhost:5173"})
	if err != nil {
		t.Fatal(err)
	}
	return server, auth, store, dataDir
}

// userCookie 为指定用户创建服务端归属的 HttpOnly 会话 Cookie。
func userCookie(t *testing.T, auth *service.Auth, userID string) *http.Cookie {
	t.Helper()
	token, err := auth.CreateSessionForUser(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: "archive_session", Value: token}
}

// TestMultiUserResourcesAreIsolated 验证用户 A 不能读取、下载或修改用户 B 的资料。
func TestMultiUserResourcesAreIsolated(t *testing.T) {
	server, auth, store, dataDir := newMultiUserHTTPServer(t)
	resourceID := strings.Repeat("a", 32)
	content := []byte("private user b document")
	store.resources[resourceID] = model.Resource{ID: resourceID, OwnerID: "user-b", Name: "private.txt", OriginalName: "private.txt", Kind: "text", MIME: "text/plain", Size: int64(len(content)), StorageKey: resourceID, SHA256: strings.Repeat("0", 64), CreatedAt: time.Now().UTC()}
	if err := os.WriteFile(filepath.Join(dataDir, resourceID), content, 0o600); err != nil {
		t.Fatal(err)
	}
	cookieA := userCookie(t, auth, "user-a")
	cookieB := userCookie(t, auth, "user-b")

	// requestAs 用指定会话发送同源请求，集中模拟浏览器携带 Cookie 的访问路径。
	requestAs := func(method, path string, cookie *http.Cookie, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.AddCookie(cookie)
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", "http://localhost:5173")
		}
		if method != http.MethodGet && method != http.MethodHead {
			request.Header.Set("Origin", "http://localhost:5173")
		}
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		return response
	}

	if response := requestAs(http.MethodGet, "/api/resources", cookieA, ""); response.Code != http.StatusOK || strings.Contains(response.Body.String(), resourceID) {
		t.Fatalf("user A list leaked user B resource: %d %s", response.Code, response.Body.String())
	}
	for _, test := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/resources/" + resourceID, ""},
		{http.MethodGet, "/api/resources/" + resourceID + "/download", ""},
		{http.MethodPatch, "/api/resources/" + resourceID + "/favorite", `{"favorite":true}`},
		{http.MethodDelete, "/api/resources/" + resourceID, ""},
	} {
		if response := requestAs(test.method, test.path, cookieA, test.body); response.Code != http.StatusNotFound {
			t.Fatalf("user A %s %s returned %d, want 404", test.method, test.path, response.Code)
		}
	}
	if response := requestAs(http.MethodGet, "/api/resources/"+resourceID+"/download", cookieB, ""); response.Code != http.StatusOK || string(response.Body.Bytes()) != string(content) {
		t.Fatalf("user B could not read own resource: %d %q", response.Code, response.Body.String())
	}
}
