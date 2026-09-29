package controller

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
	"mizuki-archive/internal/service"
)

// memoryStore 只模拟 HTTP 层关心的持久化行为，允许人为制造保存失败。
type memoryStore struct {
	resources     map[string]model.Resource
	sessions      map[string]time.Time
	saveError     error
	favoriteError error
	tags          []string
}

// SaveResource 保存 HTTP 测试资料；失败注入用于验证接口错误与文件清理。
func (store *memoryStore) SaveResource(_ context.Context, resource model.Resource) error {
	if store.saveError != nil {
		return store.saveError
	}
	store.resources[resource.ID] = resource
	return nil
}

// GetResource 模拟根据 ID 读取资料，缺失时返回统一未找到错误。
func (store *memoryStore) GetResource(_ context.Context, id string) (model.Resource, error) {
	resource, exists := store.resources[id]
	if !exists {
		return model.Resource{}, repository.ErrNotFound
	}
	return resource, nil
}

// SetFavorite 模拟资料收藏的原子更新；持久层失败时不能提前改变内存资料。
func (store *memoryStore) SetFavorite(_ context.Context, id string, favorite bool) (model.Resource, error) {
	if store.favoriteError != nil {
		return model.Resource{}, store.favoriteError
	}
	resource, exists := store.resources[id]
	if !exists {
		return model.Resource{}, repository.ErrNotFound
	}
	resource.Favorite = favorite
	store.resources[id] = resource
	return resource, nil
}

// ReplaceResourceTags 模拟事务完成后的整组标签替换，供 HTTP 错误映射测试复用。
func (store *memoryStore) ReplaceResourceTags(_ context.Context, id string, tags []string) (model.Resource, error) {
	resource, exists := store.resources[id]
	if !exists {
		return model.Resource{}, repository.ErrNotFound
	}
	resource.Tags = append([]string(nil), tags...)
	store.resources[id] = resource
	store.tags = append([]string(nil), tags...)
	return resource, nil
}

// ListTags 返回内存测试仓储的已有标签建议。
func (store *memoryStore) ListTags(_ context.Context, _ string) ([]string, error) {
	return append([]string(nil), store.tags...), nil
}

// UpdateResourceName 模拟展示名更新，测试接口不会改动原始文件名。
func (store *memoryStore) UpdateResourceName(_ context.Context, id, name string) (model.Resource, error) {
	resource, exists := store.resources[id]
	if !exists {
		return model.Resource{}, repository.ErrNotFound
	}
	resource.Name = name
	store.resources[id] = resource
	return resource, nil
}

// DeleteResource 模拟数据库软删除并返回资料，文件删除由 Service 负责。
func (store *memoryStore) DeleteResource(_ context.Context, id string) (model.Resource, error) {
	resource, exists := store.resources[id]
	if !exists {
		return model.Resource{}, repository.ErrNotFound
	}
	delete(store.resources, id)
	return resource, nil
}

// ListResources 模拟名称与类型筛选，隔离 HTTP 行为和真实 MySQL 查询。
func (store *memoryStore) ListResources(_ context.Context, query model.ListQuery) ([]model.Resource, error) {
	resources := make([]model.Resource, 0)
	for _, resource := range store.resources {
		matchesTag := query.Tag == ""
		for _, tag := range resource.Tags {
			if tag == query.Tag {
				matchesTag = true
				break
			}
		}
		if strings.Contains(strings.ToLower(resource.Name), strings.ToLower(query.Search)) && (query.Kind == "" || resource.Kind == query.Kind) && matchesTag {
			resources = append(resources, resource)
		}
	}
	return resources, nil
}

// SaveSession 在内存中登记登录令牌摘要与过期时间。
func (store *memoryStore) SaveSession(_ context.Context, hash string, expires time.Time) error {
	store.sessions[hash] = expires
	return nil
}

// HasSession 模拟数据库会话校验，过期后不允许访问私有路由。
func (store *memoryStore) HasSession(_ context.Context, hash string) (bool, error) {
	expires, exists := store.sessions[hash]
	return exists && expires.After(time.Now()), nil
}

// DeleteSession 删除内存会话，供退出接口验证撤销效果。
func (store *memoryStore) DeleteSession(_ context.Context, hash string) error {
	delete(store.sessions, hash)
	return nil
}

// testServer 构造独立的 Gin、内存仓储和临时目录，防止用例读写真实资料。
func testServer(t *testing.T) (http.Handler, *memoryStore, string) {
	t.Helper()
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("correct horse battery staple"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{resources: map[string]model.Resource{}, sessions: map[string]time.Time{}}
	dataDir := t.TempDir()
	resourceService, err := service.NewResources(store, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	authService, err := service.NewAuth(store, "owner", passwordHash)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{
		Resources: resourceService, Auth: authService, Origin: "http://localhost:5173",
	})
	if err != nil {
		t.Fatal(err)
	}
	return server, store, dataDir
}

// login 用正确测试凭据获取 HttpOnly Cookie，供后续私有接口复用。
func login(t *testing.T, server http.Handler) *http.Cookie {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"username":"owner","password":"correct horse battery staple"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://localhost:5173")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("login returned %d: %s", response.Code, response.Body.String())
	}
	if len(response.Result().Cookies()) != 1 || !response.Result().Cookies()[0].HttpOnly {
		t.Fatal("expected an HttpOnly session cookie")
	}
	return response.Result().Cookies()[0]
}

// uploadRequest 构造带 Origin 的 multipart 请求，与浏览器实际上传结构一致。
func uploadRequest(t *testing.T, name string, content []byte) *http.Request {
	t.Helper()
	buffer := &bytes.Buffer{}
	writer := multipart.NewWriter(buffer)
	part, err := writer.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/resources", buffer)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Origin", "http://localhost:5173")
	return request
}

// TestSessionAndResourceFlow 串起未授权访问、恶意文件拒绝、上传与附件下载。
func TestSessionAndResourceFlow(t *testing.T) {
	server, store, dataDir := testServer(t)
	unauthorized := httptest.NewRecorder()
	server.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/resources", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("private list returned %d", unauthorized.Code)
	}

	missingOrigin := httptest.NewRecorder()
	server.ServeHTTP(missingOrigin, httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{}`)))
	if missingOrigin.Code != http.StatusForbidden {
		t.Fatalf("missing origin returned %d", missingOrigin.Code)
	}

	cookie := login(t, server)
	badFile := uploadRequest(t, "attack.html", []byte("<script>alert(1)</script>"))
	badFile.AddCookie(cookie)
	badResponse := httptest.NewRecorder()
	server.ServeHTTP(badResponse, badFile)
	if badResponse.Code != http.StatusUnsupportedMediaType || len(store.resources) != 0 {
		t.Fatalf("unsupported file returned %d", badResponse.Code)
	}

	content := []byte("%PDF-1.4\n1 0 obj\n<<>>\nendobj\n%%EOF")
	request := uploadRequest(t, "notes.pdf", content)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || len(store.resources) != 1 {
		t.Fatalf("upload returned %d: %s", response.Code, response.Body.String())
	}
	var resource model.Resource
	for _, item := range store.resources {
		resource = item
	}
	if resource.Name != "notes.pdf" || resource.Size != int64(len(content)) {
		t.Fatalf("unexpected metadata: %#v", resource)
	}
	if _, err := os.Stat(filepath.Join(dataDir, resource.ID)); err != nil {
		t.Fatal(err)
	}

	download := httptest.NewRequest(http.MethodGet, "/api/resources/"+resource.ID+"/download", nil)
	download.AddCookie(cookie)
	downloadResponse := httptest.NewRecorder()
	server.ServeHTTP(downloadResponse, download)
	if downloadResponse.Code != http.StatusOK || !bytes.Equal(downloadResponse.Body.Bytes(), content) {
		t.Fatalf("download returned %d: %s", downloadResponse.Code, downloadResponse.Body.String())
	}
	if !strings.Contains(downloadResponse.Header().Get("Content-Disposition"), "attachment") {
		t.Fatal("download must not be rendered inline")
	}
}

// TestFailedSaveRemovesUploadedFile 验证持久层失败后接口报错且目录不遗留文件。
func TestFailedSaveRemovesUploadedFile(t *testing.T) {
	server, store, dataDir := testServer(t)
	store.saveError = errors.New("database unavailable")
	cookie := login(t, server)
	request := uploadRequest(t, "note.txt", []byte("sample"))
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("save failure returned %d", response.Code)
	}
	entries, err := os.ReadDir(dataDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("orphan files remain: %v, %v", entries, err)
	}
}

// TestLogoutRevokesSession 验证登出会撤销服务端会话，不只是清除客户端 Cookie。
func TestLogoutRevokesSession(t *testing.T) {
	server, _, _ := testServer(t)
	cookie := login(t, server)
	request := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	request.Header.Set("Origin", "http://localhost:5173")
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("logout returned %d", response.Code)
	}
	private := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	private.AddCookie(cookie)
	check := httptest.NewRecorder()
	server.ServeHTTP(check, private)
	if check.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session returned %d", check.Code)
	}
}

// TestLoginRateLimit 确认连续错误密码达到阈值时返回 429。
func TestLoginRateLimit(t *testing.T) {
	server, _, _ := testServer(t)
	for attempt := 0; attempt < 6; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"username":"owner","password":"wrong password"}`))
		request.Header.Set("Origin", "http://localhost:5173")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if attempt < 5 && response.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d returned %d", attempt, response.Code)
		}
		if attempt == 5 && response.Code != http.StatusTooManyRequests {
			t.Fatalf("rate limit returned %d", response.Code)
		}
	}
}

// TestLoginRejectsOversizedBody 确认过大的登录请求会在 HTTP 边界被拒绝。
func TestLoginRejectsOversizedBody(t *testing.T) {
	server, _, _ := testServer(t)
	request := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"username":"owner","password":"`+strings.Repeat("a", 9000)+`"}`))
	request.Header.Set("Origin", "http://localhost:5173")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized login returned %d", response.Code)
	}
}

// favoriteResponse 向测试服务提交收藏状态，统一携带已登录 Cookie 和同源来源。
func favoriteResponse(server http.Handler, cookie *http.Cookie, id, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPatch, "/api/resources/"+id+"/favorite", strings.NewReader(body))
	request.Header.Set("Origin", "http://localhost:5173")
	request.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

// tagsResponse 向测试服务提交完整标签数组，统一携带登录 Cookie 和同源来源。
func tagsResponse(server http.Handler, cookie *http.Cookie, id, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPut, "/api/resources/"+id+"/tags", strings.NewReader(body))
	request.Header.Set("Origin", "http://localhost:5173")
	request.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

// nameResponse 向测试服务提交展示名，统一携带登录 Cookie 和同源来源。
func nameResponse(server http.Handler, cookie *http.Cookie, id, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPatch, "/api/resources/"+id+"/name", strings.NewReader(body))
	request.Header.Set("Origin", "http://localhost:5173")
	request.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

// TestFavoriteFlow 验证收藏只能由已登录同源请求设置，失败不改变数据，重复设置仍成功。
func TestFavoriteFlow(t *testing.T) {
	server, store, _ := testServer(t)
	id := strings.Repeat("a", 32)
	store.resources[id] = model.Resource{ID: id, Name: "notes.txt"}
	if response := favoriteResponse(server, nil, id, `{"favorite":true}`); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous update returned %d", response.Code)
	}
	cookie := login(t, server)
	request := httptest.NewRequest(http.MethodPatch, "/api/resources/"+id+"/favorite", strings.NewReader(`{"favorite":true}`))
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin update returned %d", response.Code)
	}
	for _, body := range []string{`{}`, `{"favorite":null}`, `{"favorite":"yes"}`, `{"favorite":true,"extra":1}`, `{"favorite":true}{"favorite":false}`, strings.Repeat(" ", 1025)} {
		if response := favoriteResponse(server, cookie, id, body); response.Code != http.StatusBadRequest {
			t.Fatalf("invalid body returned %d: %q", response.Code, body)
		}
	}
	if response := favoriteResponse(server, cookie, "invalid", `{"favorite":true}`); response.Code != http.StatusNotFound {
		t.Fatalf("invalid ID returned %d", response.Code)
	}
	if response := favoriteResponse(server, cookie, strings.Repeat("b", 32), `{"favorite":true}`); response.Code != http.StatusNotFound {
		t.Fatalf("missing resource returned %d", response.Code)
	}
	for _, favorite := range []bool{true, true, false} {
		body := `{"favorite":false}`
		if favorite {
			body = `{"favorite":true}`
		}
		response := favoriteResponse(server, cookie, id, body)
		if response.Code != http.StatusOK || store.resources[id].Favorite != favorite || !strings.Contains(response.Body.String(), `"favorite":`+strconv.FormatBool(favorite)) {
			t.Fatalf("favorite=%t returned %d: %s", favorite, response.Code, response.Body.String())
		}
	}
	store.favoriteError = errors.New("database unavailable")
	if response := favoriteResponse(server, cookie, id, `{"favorite":true}`); response.Code != http.StatusInternalServerError || store.resources[id].Favorite {
		t.Fatalf("failed update returned %d and persisted favorite=%t", response.Code, store.resources[id].Favorite)
	}
}

// TestTagsFlow 验证标签替换的认证、规范化、清空、筛选和严格输入边界。
func TestTagsFlow(t *testing.T) {
	server, store, _ := testServer(t)
	id := strings.Repeat("a", 32)
	store.resources[id] = model.Resource{ID: id, Name: "notes.txt", Kind: "text"}
	if response := tagsResponse(server, nil, id, `{"tags":["go"]}`); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous update returned %d", response.Code)
	}
	cookie := login(t, server)
	request := httptest.NewRequest(http.MethodPut, "/api/resources/"+id+"/tags", strings.NewReader(`{"tags":["go"]}`))
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin update returned %d", response.Code)
	}
	for _, body := range []string{`{}`, `{"tags":null}`, `{"tags":["bad\nname"]}`, `{"tags":["one","two","three","four","five","six","seven","eight","nine","ten","eleven"]}`, `{"tags":["go"],"extra":1}`, `{"tags":["go"]}{"tags":[]}`, strings.Repeat(" ", 1025)} {
		if response := tagsResponse(server, cookie, id, body); response.Code != http.StatusBadRequest {
			t.Fatalf("invalid body returned %d: %q", response.Code, body)
		}
	}
	if response := tagsResponse(server, cookie, "invalid", `{"tags":["go"]}`); response.Code != http.StatusNotFound {
		t.Fatalf("invalid ID returned %d", response.Code)
	}
	response = tagsResponse(server, cookie, id, `{"tags":[" Redis ","面试","redis","go-1"]}`)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"tags":["go-1","redis","面试"]`) {
		t.Fatalf("normalized tags returned %d: %s", response.Code, response.Body.String())
	}
	list := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/api/resources?tag=redis", nil)
	request.AddCookie(cookie)
	server.ServeHTTP(list, request)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), id) {
		t.Fatalf("tag filter returned %d: %s", list.Code, list.Body.String())
	}
	suggestions := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/api/tags?q=red", nil)
	request.AddCookie(cookie)
	server.ServeHTTP(suggestions, request)
	if suggestions.Code != http.StatusOK || !strings.Contains(suggestions.Body.String(), "redis") {
		t.Fatalf("tag suggestions returned %d: %s", suggestions.Code, suggestions.Body.String())
	}
	if response := tagsResponse(server, cookie, id, `{"tags":[]}`); response.Code != http.StatusOK || len(store.resources[id].Tags) != 0 {
		t.Fatalf("clear tags returned %d: %s", response.Code, response.Body.String())
	}
}

// TestResourceNamePreviewAndDelete 验证展示名编辑、纯文本预览和删除后的资源不可见性。
func TestResourceNamePreviewAndDelete(t *testing.T) {
	server, store, dataDir := testServer(t)
	id := strings.Repeat("c", 32)
	store.resources[id] = model.Resource{ID: id, Name: "old.md", OriginalName: "upload.md", Kind: "markdown", MIME: "text/plain; charset=utf-8", StorageKey: id, SHA256: strings.Repeat("0", 64)}
	path := filepath.Join(dataDir, id)
	content := []byte("<script>alert(1)</script>\n# safe text")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	cookie := login(t, server)
	if response := nameResponse(server, cookie, id, `{"name":"整理后的资料.md"}`); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "整理后的资料.md") {
		t.Fatalf("name update returned %d: %s", response.Code, response.Body.String())
	}
	for _, body := range []string{`{}`, `{"name":"bad/name"}`, `{"name":"bad\nname"}`, `{"name":"ok","extra":1}`, `{"name":"ok"}{"name":"again"}`} {
		if response := nameResponse(server, cookie, id, body); response.Code != http.StatusBadRequest {
			t.Fatalf("invalid name returned %d: %q", response.Code, body)
		}
	}
	preview := httptest.NewRequest(http.MethodGet, "/api/resources/"+id+"/preview", nil)
	preview.AddCookie(cookie)
	previewResponse := httptest.NewRecorder()
	server.ServeHTTP(previewResponse, preview)
	if previewResponse.Code != http.StatusOK || previewResponse.Header().Get("Content-Type") != "text/plain; charset=utf-8" || previewResponse.Body.String() != string(content) {
		t.Fatalf("preview returned %d, %q, %q", previewResponse.Code, previewResponse.Header().Get("Content-Type"), previewResponse.Body.String())
	}
	deleteRequest := httptest.NewRequest(http.MethodDelete, "/api/resources/"+id, nil)
	deleteRequest.Header.Set("Origin", "http://localhost:5173")
	deleteRequest.AddCookie(cookie)
	deleted := httptest.NewRecorder()
	server.ServeHTTP(deleted, deleteRequest)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete returned %d: %s", deleted.Code, deleted.Body.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("deleted file exists: %v", err)
	}
	missing := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/resources/"+id, nil)
	request.AddCookie(cookie)
	server.ServeHTTP(missing, request)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("deleted resource returned %d", missing.Code)
	}
}
