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

type fakeStore struct {
	resources map[string]model.Resource
	sessions  map[string]time.Time
	saveError error
}

func (store *fakeStore) SaveResource(_ context.Context, resource model.Resource) error {
	if store.saveError != nil {
		return store.saveError
	}
	store.resources[resource.ID] = resource
	return nil
}

func (store *fakeStore) GetResource(_ context.Context, id string) (model.Resource, error) {
	resource, exists := store.resources[id]
	if !exists {
		return model.Resource{}, repository.ErrNotFound
	}
	return resource, nil
}

func (store *fakeStore) ListResources(_ context.Context, _ model.ListQuery) ([]model.Resource, error) {
	resources := make([]model.Resource, 0, len(store.resources))
	for _, resource := range store.resources {
		resources = append(resources, resource)
	}
	return resources, nil
}

func (store *fakeStore) SaveSession(_ context.Context, hash string, expires time.Time) error {
	store.sessions[hash] = expires
	return nil
}

func (store *fakeStore) HasSession(_ context.Context, hash string) (bool, error) {
	expires, exists := store.sessions[hash]
	return exists && expires.After(time.Now()), nil
}

func (store *fakeStore) DeleteSession(_ context.Context, hash string) error {
	delete(store.sessions, hash)
	return nil
}

func newFakeStore() *fakeStore {
	return &fakeStore{resources: map[string]model.Resource{}, sessions: map[string]time.Time{}}
}

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
		t.Run(testCase.name+testCase.content, func(t *testing.T) {
			_, _, allowed := fileType(testCase.name, []byte(testCase.content))
			if allowed != testCase.allowed {
				t.Fatalf("fileType(%q) allowed=%t, want %t", testCase.name, allowed, testCase.allowed)
			}
		})
	}
}

func TestSafeFilename(t *testing.T) {
	if name, valid := safeFilename(`..\private\notes.txt`); !valid || name != "notes.txt" {
		t.Fatalf("unsafe path normalized to %q, valid=%t", name, valid)
	}
	if _, valid := safeFilename("bad\nname.txt"); valid {
		t.Fatal("control characters must be rejected")
	}
}

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
