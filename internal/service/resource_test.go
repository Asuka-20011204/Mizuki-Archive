package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
)

// fakeStore 隔离 Service 单测与数据库；各测试独立创建，避免共享状态污染。
type fakeStore struct {
	resources     map[string]model.Resource
	sessions      map[string]time.Time
	saveError     error
	favoriteError error
}

// SaveResource 模拟写入资源；可注入失败以检查文件系统补偿。
func (store *fakeStore) SaveResource(_ context.Context, resource model.Resource) error {
	if store.saveError != nil {
		return store.saveError
	}
	store.resources[resource.ID] = resource
	return nil
}

// GetResource 模拟按 ID 查找，并与真实仓储一样返回 ErrNotFound。
func (store *fakeStore) GetResource(_ context.Context, id string) (model.Resource, error) {
	resource, exists := store.resources[id]
	if !exists {
		return model.Resource{}, repository.ErrNotFound
	}
	return resource, nil
}

// SetFavorite 模拟收藏持久化，错误注入时保持原资料不变。
func (store *fakeStore) SetFavorite(_ context.Context, id string, favorite bool) (model.Resource, error) {
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

// ListResources 返回内存资料；Service 测试只验证转发，不测试 SQL 筛选。
func (store *fakeStore) ListResources(_ context.Context, _ model.ListQuery) ([]model.Resource, error) {
	resources := make([]model.Resource, 0, len(store.resources))
	for _, resource := range store.resources {
		resources = append(resources, resource)
	}
	return resources, nil
}

// SaveSession 在内存中保存摘要和失效时间，供认证流程断言。
func (store *fakeStore) SaveSession(_ context.Context, hash string, expires time.Time) error {
	store.sessions[hash] = expires
	return nil
}

// HasSession 用当前时间模拟数据库的会话过期判断。
func (store *fakeStore) HasSession(_ context.Context, hash string) (bool, error) {
	expires, exists := store.sessions[hash]
	return exists && expires.After(time.Now()), nil
}

// DeleteSession 删除测试会话，验证退出后旧令牌不可再用。
func (store *fakeStore) DeleteSession(_ context.Context, hash string) error {
	delete(store.sessions, hash)
	return nil
}

// newFakeStore 为每个用例创建独立内存仓储，避免测试之间共享状态。
func newFakeStore() *fakeStore {
	return &fakeStore{resources: map[string]model.Resource{}, sessions: map[string]time.Time{}}
}

// TestFileTypeRejectsMismatchedAndActiveContent 验证扩展名与内容不一致或含活动内容时拒绝上传。
func TestFileTypeRejectsMismatchedAndActiveContent(t *testing.T) {
	cases := []struct {
		name    string
		content string
		allowed bool
	}{
		{"document.pdf", "%PDF-1.7\n", true},
		{"document.pdf", "<script>bad</script>", false},
		{"image.jpg", "<svg onload=alert(1)>", false},
		{"notes.md", "# Notes\n", true},
		{"note.html", "<h1>Test</h1>", false},
	}
	for _, testCase := range cases {
		// 每个扩展名与文件头组合单独报告，便于定位绕过格式校验的回归。
		t.Run(testCase.name+testCase.content, func(t *testing.T) {
			_, _, allowed := fileType(testCase.name, []byte(testCase.content))
			if allowed != testCase.allowed {
				t.Fatalf("fileType(%q) allowed=%t, want %t", testCase.name, allowed, testCase.allowed)
			}
		})
	}
}

// TestSafeFilename 验证路径只留下展示文件名，控制字符直接拒绝。
func TestSafeFilename(t *testing.T) {
	if name, valid := safeFilename(`..\private\notes.txt`); !valid || name != "notes.txt" {
		t.Fatalf("unsafe path normalized to %q, valid=%t", name, valid)
	}
	if _, valid := safeFilename("bad\nname.txt"); valid {
		t.Fatal("control characters must be rejected")
	}
}

// TestResourceServiceUploadAndRead 覆盖上传落盘、读取原件、无效 ID 与异常存储键。
func TestResourceServiceUploadAndRead(t *testing.T) {
	store := newFakeStore()
	dataDir := t.TempDir()
	resources, err := NewResources(store, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("%PDF-1.4\n%%EOF")
	resource, err := resources.Upload(context.Background(), "../draft.pdf", bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	if resource.Name != "draft.pdf" || resource.Size != int64(len(content)) {
		t.Fatalf("unexpected resource: %#v", resource)
	}
	stored, err := resources.Get(context.Background(), resource.ID)
	if err != nil || stored.ID != resource.ID {
		t.Fatalf("cannot read resource: %#v, %v", stored, err)
	}
	file, err := resources.Open(stored)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	actual := make([]byte, len(content))
	if _, err := file.Read(actual); err != nil || !bytes.Equal(actual, content) {
		t.Fatalf("file mismatch: %q, %v", actual, err)
	}
	if _, err := resources.Get(context.Background(), "../outside"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("invalid ID returned %v", err)
	}
	stored.StorageKey = "../outside"
	if _, err := resources.Open(stored); !errors.Is(err, ErrFileUnavailable) {
		t.Fatalf("unsafe storage key returned %v", err)
	}
}

// TestResourceServiceSetFavorite 验证非法 ID 不落到仓储，重复设置幂等且存储失败不修改状态。
func TestResourceServiceSetFavorite(t *testing.T) {
	store := newFakeStore()
	resources, err := NewResources(store, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 32)
	store.resources[id] = model.Resource{ID: id, Name: "notes.txt"}
	if _, err := resources.SetFavorite(context.Background(), "../invalid", true); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("invalid ID returned %v", err)
	}
	if _, err := resources.SetFavorite(context.Background(), strings.Repeat("b", 32), true); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("missing resource returned %v", err)
	}
	for _, favorite := range []bool{true, true, false} {
		updated, err := resources.SetFavorite(context.Background(), id, favorite)
		if err != nil || updated.Favorite != favorite || store.resources[id].Favorite != favorite {
			t.Fatalf("favorite=%t: got %#v, error %v", favorite, updated, err)
		}
	}
	store.favoriteError = errors.New("database unavailable")
	if _, err := resources.SetFavorite(context.Background(), id, true); err == nil || store.resources[id].Favorite {
		t.Fatalf("failed update changed persisted favorite: %v", err)
	}
}

// TestResourceServiceRemovesFilesOnFailure 验证数据库写入失败时无孤儿文件，也不采用用户文件名作路径。
func TestResourceServiceRemovesFilesOnFailure(t *testing.T) {
	store := newFakeStore()
	store.saveError = errors.New("database unavailable")
	dataDir := t.TempDir()
	resources, err := NewResources(store, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = resources.Upload(context.Background(), "notes.txt", strings.NewReader("hello"))
	if err == nil {
		t.Fatal("expected database error")
	}
	entries, err := os.ReadDir(dataDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("orphan files remain: %v, %v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "notes.txt")); !os.IsNotExist(err) {
		t.Fatalf("user filename became storage path: %v", err)
	}
}

// TestAuthServiceSessionLifecycle 验证错误凭据、令牌摘要、会话有效期和主动退出。
func TestAuthServiceSessionLifecycle(t *testing.T) {
	store := newFakeStore()
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("a secure testing password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := NewAuth(store, "owner", passwordHash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := auth.Login(context.Background(), "other", "a secure testing password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong user returned %v", err)
	}
	token, err := auth.Login(context.Background(), "owner", "a secure testing password")
	if err != nil || len(token) == 0 {
		t.Fatalf("login failed: %v", err)
	}
	if valid, err := auth.Validate(context.Background(), token); !valid || err != nil {
		t.Fatalf("session invalid: %t, %v", valid, err)
	}
	if valid, err := auth.Validate(context.Background(), "bad-token"); valid || err != nil {
		t.Fatalf("forged session valid: %t, %v", valid, err)
	}
	if err := auth.Logout(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if valid, err := auth.Validate(context.Background(), token); valid || err != nil {
		t.Fatalf("revoked session valid: %t, %v", valid, err)
	}
}
