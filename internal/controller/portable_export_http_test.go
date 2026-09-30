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

// portableExportHTTPStore 只返回会话账号的数据，证明路由不接受用户自报归属。
type portableExportHTTPStore struct {
	*multiUserHTTPStore
	byOwner map[string]model.PortableArchive
}

// ExportSnapshot 用会话中的用户 ID 选择测试快照，并模拟明确的导出容量错误。
func (store *portableExportHTTPStore) ExportSnapshot(ctx context.Context) (model.PortableArchive, error) {
	owner, _ := repository.UserIDFromContext(ctx)
	if owner == "too-large" {
		return model.PortableArchive{}, repository.ErrPortableExportLimit
	}
	return store.byOwner[owner], nil
}

// TestPortableExportHTTPIsolation 覆盖匿名、账号隔离、附件响应、超限与频率上限。
func TestPortableExportHTTPIsolation(t *testing.T) {
	base := &multiUserHTTPStore{resources: map[string]model.Resource{}, sessions: map[string]string{}, users: map[string]model.User{}}
	store := &portableExportHTTPStore{multiUserHTTPStore: base, byOwner: map[string]model.PortableArchive{
		"first":  {Files: []model.Resource{{Name: "自己的资料"}}},
		"second": {Files: []model.Resource{{Name: "他人的资料"}}},
	}}
	hash, err := bcrypt.GenerateFromPassword([]byte("synthetic-test-password"), bcrypt.MinCost)
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
	exporter, err := service.NewPortableExport(store)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{Resources: files, Export: exporter, Auth: auth, Origin: "http://localhost:5173"})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/export"
	if response := externalRequest(server, http.MethodGet, path, "", nil); response.Code != http.StatusUnauthorized || strings.Contains(response.Body.String(), "自己的资料") {
		t.Fatalf("anonymous export: %d %s", response.Code, response.Body.String())
	}
	first := userCookie(t, auth, "first")
	second := userCookie(t, auth, "second")
	response := externalRequest(server, http.MethodGet, path, "", first)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "自己的资料") || strings.Contains(response.Body.String(), "他人的资料") {
		t.Fatalf("first export: %d %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Header().Get("Content-Disposition"), "attachment;") || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unsafe export headers: %+v", response.Header())
	}
	if response := externalRequest(server, http.MethodGet, path, "", second); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "他人的资料") || strings.Contains(response.Body.String(), "自己的资料") {
		t.Fatalf("second export: %d %s", response.Code, response.Body.String())
	}
	if response := externalRequest(server, http.MethodGet, path, "", first); response.Code != http.StatusOK {
		t.Fatalf("second request: %d", response.Code)
	}
	if response := externalRequest(server, http.MethodGet, path, "", first); response.Code != http.StatusTooManyRequests {
		t.Fatalf("rate limit: %d", response.Code)
	}
	if response := externalRequest(server, http.MethodGet, path, "", userCookie(t, auth, "too-large")); response.Code != http.StatusRequestEntityTooLarge || response.Header().Get("Content-Disposition") != "" {
		t.Fatalf("oversized export: %d %s", response.Code, response.Body.String())
	}
}
