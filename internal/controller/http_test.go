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
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
	"mizuki-archive/internal/service"
)

type memoryStore struct {
	resources map[string]model.Resource
	sessions  map[string]time.Time
	saveError error
}

func (store *memoryStore) SaveResource(_ context.Context, resource model.Resource) error {
	if store.saveError != nil {
		return store.saveError
	}
	store.resources[resource.ID] = resource
	return nil
}

func (store *memoryStore) GetResource(_ context.Context, id string) (model.Resource, error) {
	resource, exists := store.resources[id]
	if !exists {
		return model.Resource{}, repository.ErrNotFound
	}
	return resource, nil
}

func (store *memoryStore) ListResources(_ context.Context, query model.ListQuery) ([]model.Resource, error) {
	resources := make([]model.Resource, 0)
	for _, resource := range store.resources {
		if strings.Contains(strings.ToLower(resource.Name), strings.ToLower(query.Search)) && (query.Kind == "" || resource.Kind == query.Kind) {
			resources = append(resources, resource)
		}
	}
	return resources, nil
}

func (store *memoryStore) SaveSession(_ context.Context, hash string, expires time.Time) error {
	store.sessions[hash] = expires
	return nil
}

func (store *memoryStore) HasSession(_ context.Context, hash string) (bool, error) {
	expires, exists := store.sessions[hash]
	return exists && expires.After(time.Now()), nil
}

func (store *memoryStore) DeleteSession(_ context.Context, hash string) error {
	delete(store.sessions, hash)
	return nil
}

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
