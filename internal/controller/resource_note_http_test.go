package controller

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
	"mizuki-archive/internal/service"
)

// noteHTTPStore 在会话边界内模拟笔记持久化，跨用户操作必须先通过文件归属检查。
type noteHTTPStore struct {
	*multiUserHTTPStore
	notes map[string]model.ResourceNote
}

// CreateResourceNote 只接受拥有目标文件的会话身份。
func (store *noteHTTPStore) CreateResourceNote(ctx context.Context, note model.ResourceNote) error {
	if _, err := store.GetResource(ctx, note.ResourceID); err != nil {
		return err
	}
	store.notes[note.ID] = note
	return nil
}

// ListResourceNotes 仅返回当前用户拥有的文件笔记。
func (store *noteHTTPStore) ListResourceNotes(ctx context.Context, resourceID string) ([]model.ResourceNote, error) {
	if _, err := store.GetResource(ctx, resourceID); err != nil {
		return nil, err
	}
	items := []model.ResourceNote{}
	for _, note := range store.notes {
		if note.ResourceID == resourceID {
			items = append(items, note)
		}
	}
	return items, nil
}

// UpdateResourceNote 同时校验会话、资源 ID 与笔记 ID。
func (store *noteHTTPStore) UpdateResourceNote(ctx context.Context, note model.ResourceNote) error {
	if _, err := store.GetResource(ctx, note.ResourceID); err != nil {
		return err
	}
	previous, ok := store.notes[note.ID]
	if !ok || previous.ResourceID != note.ResourceID {
		return repository.ErrNotFound
	}
	store.notes[note.ID] = note
	return nil
}

// DeleteResourceNote 只删除指定文件中已存在的目标笔记。
func (store *noteHTTPStore) DeleteResourceNote(ctx context.Context, resourceID, noteID string) error {
	if _, err := store.GetResource(ctx, resourceID); err != nil {
		return err
	}
	if note, ok := store.notes[noteID]; !ok || note.ResourceID != resourceID {
		return repository.ErrNotFound
	}
	delete(store.notes, noteID)
	return nil
}

// TestResourceNoteHTTPIsolation 验证匿名拒绝、跨账号不可见、页码错误和完整编辑删除流程。
func TestResourceNoteHTTPIsolation(t *testing.T) {
	base := &multiUserHTTPStore{resources: map[string]model.Resource{}, sessions: map[string]string{}, users: map[string]model.User{}}
	store := &noteHTTPStore{multiUserHTTPStore: base, notes: map[string]model.ResourceNote{}}
	resourceID := strings.Repeat("a", 32)
	store.resources[resourceID] = model.Resource{ID: resourceID, OwnerID: "user-b", Kind: "pdf", Name: "private.pdf", CreatedAt: time.Now().UTC()}
	hash, err := bcrypt.GenerateFromPassword([]byte("test-only-not-a-real-secret"), bcrypt.MinCost)
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
	notes, err := service.NewResourceNotes(store)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{Resources: files, Notes: notes, Auth: auth, Origin: "http://localhost:5173"})
	if err != nil {
		t.Fatal(err)
	}
	owner := userCookie(t, auth, "user-b")
	other := userCookie(t, auth, "user-a")
	path := "/api/resources/" + resourceID + "/notes"
	if response := externalRequest(server, http.MethodGet, path, "", nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous list: %d", response.Code)
	}
	if response := externalRequest(server, http.MethodPost, path, `{"page_number":2,"excerpt":"私人摘录","content":"我的笔记","source":"第 2 页"}`, owner); response.Code != http.StatusCreated {
		t.Fatalf("owner create: %d %s", response.Code, response.Body.String())
	}
	var noteID string
	for id := range store.notes {
		noteID = id
	}
	if !strings.Contains(externalRequest(server, http.MethodGet, path, "", owner).Body.String(), "私人摘录") {
		t.Fatal("owner cannot read note")
	}
	if response := externalRequest(server, http.MethodGet, path, "", other); response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "私人摘录") {
		t.Fatalf("other user list: %d %s", response.Code, response.Body.String())
	}
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		if response := externalRequest(server, method, path+"/"+noteID, `{"content":"attack"}`, other); response.Code != http.StatusNotFound {
			t.Fatalf("other user %s: %d", method, response.Code)
		}
	}
	if response := externalRequest(server, http.MethodPost, path, `{"page_number":0,"content":"invalid"}`, owner); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid page: %d", response.Code)
	}
	if response := externalRequest(server, http.MethodPut, path+"/"+noteID, `{"content":"修订正文"}`, owner); response.Code != http.StatusNoContent {
		t.Fatalf("owner update: %d %s", response.Code, response.Body.String())
	}
	if response := externalRequest(server, http.MethodGet, path, "", owner); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "修订正文") {
		t.Fatalf("owner updated read: %d %s", response.Code, response.Body.String())
	}
	if response := externalRequest(server, http.MethodDelete, path+"/"+noteID, "", owner); response.Code != http.StatusNoContent {
		t.Fatalf("owner delete: %d", response.Code)
	}
	if _, exists := store.notes[noteID]; exists || !errors.Is(store.DeleteResourceNote(repository.WithUserID(context.Background(), "user-b"), resourceID, noteID), repository.ErrNotFound) {
		t.Fatal("deleted note remained readable")
	}
}
