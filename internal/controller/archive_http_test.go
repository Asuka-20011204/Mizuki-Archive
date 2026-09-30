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

// archiveHTTPStore 在内存中模拟全部归属先校验再更新，HTTP 测试不替代真实 MySQL 事务。
type archiveHTTPStore struct{ *inboxHTTPStore }

// ListArchivedResources 按当前会话归属读取归档文件。
func (store *archiveHTTPStore) ListArchivedResources(ctx context.Context, _, _ int) ([]model.Resource, error) {
	owner, _ := repository.UserIDFromContext(ctx)
	items := []model.Resource{}
	for _, file := range store.resources {
		if file.OwnerID == owner && file.Archived {
			items = append(items, file)
		}
	}
	return items, nil
}

// ListArchivedExternalResources 按当前会话归属读取归档卡片。
func (store *archiveHTTPStore) ListArchivedExternalResources(ctx context.Context, _, _ int) ([]model.ExternalResource, error) {
	owner, _ := repository.UserIDFromContext(ctx)
	items := []model.ExternalResource{}
	for id, card := range store.cards {
		if store.owners[id] == owner && card.Archived {
			items = append(items, card)
		}
	}
	return items, nil
}

// BatchSetArchived 先核对整个混合批次再更新，拒绝任何跨账号或缺失项。
func (store *archiveHTTPStore) BatchSetArchived(ctx context.Context, items []model.InboxSelection, archived bool) ([]model.InboxSelection, error) {
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
	changed := []model.InboxSelection{}
	for _, item := range items {
		if item.Source == "file" {
			file := store.resources[item.ID]
			if file.Archived == archived {
				continue
			}
			file.Archived = archived
			store.resources[item.ID] = file
		} else {
			card := store.cards[item.ID]
			if card.Archived == archived {
				continue
			}
			card.Archived = archived
			store.cards[item.ID] = card
		}
		changed = append(changed, item)
	}
	return changed, nil
}

// TestArchiveHTTPBoundary 验证匿名、跨账号混合批次、严格 JSON、归档列表和恢复。
func TestArchiveHTTPBoundary(t *testing.T) {
	base := &multiUserHTTPStore{resources: map[string]model.Resource{}, sessions: map[string]string{}, users: map[string]model.User{}}
	external := &externalHTTPStore{multiUserHTTPStore: base, cards: map[string]model.ExternalResource{}, owners: map[string]string{}}
	store := &archiveHTTPStore{inboxHTTPStore: &inboxHTTPStore{externalHTTPStore: external}}
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
	archive, err := service.NewArchive(store)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{Resources: resources, Auth: auth, Archive: archive, Origin: "http://localhost:5173"})
	if err != nil {
		t.Fatal(err)
	}
	ownerA, ownerB := userCookie(t, auth, "user-a"), userCookie(t, auth, "user-b")
	fileID, cardID := strings.Repeat("c", 32), strings.Repeat("d", 32)
	store.resources[fileID] = model.Resource{ID: fileID, OwnerID: "user-a", Name: "own file"}
	store.owners[cardID] = "user-b"
	store.cards[cardID] = model.ExternalResource{ID: cardID, Title: "private card"}
	path := "/api/batch/archive"
	valid := `{"archived":true,"items":[{"source":"file","id":"` + fileID + `"}]}`
	if result := externalRequest(server, http.MethodGet, "/api/archive", "", nil); result.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous list: %d", result.Code)
	}
	if result := externalRequest(server, http.MethodPatch, path, valid, nil); result.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous write: %d", result.Code)
	}
	mixed := `{"archived":true,"items":[{"source":"file","id":"` + fileID + `"},{"source":"external","id":"` + cardID + `"}]}`
	if result := externalRequest(server, http.MethodPatch, path, mixed, ownerA); result.Code != http.StatusNotFound || store.resources[fileID].Archived {
		t.Fatalf("cross-user atomicity: %d", result.Code)
	}
	for _, body := range []string{valid + ` {}`, valid + strings.Repeat(" ", 9<<10), `{"items":[{"source":"file","id":"` + fileID + `"}]}`, `{"archived":true,"items":[{"source":"file","id":"` + fileID + `"},{"source":"file","id":"` + fileID + `"}]}`} {
		if result := externalRequest(server, http.MethodPatch, path, body, ownerA); result.Code != http.StatusBadRequest {
			t.Fatalf("invalid body: %d", result.Code)
		}
	}
	if result := externalRequest(server, http.MethodPatch, path, valid, ownerA); result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"count":1`) {
		t.Fatalf("archive: %d %s", result.Code, result.Body.String())
	}
	cardBatch := `{"archived":true,"items":[{"source":"external","id":"` + cardID + `"}]}`
	if result := externalRequest(server, http.MethodPatch, path, cardBatch, ownerB); result.Code != http.StatusOK {
		t.Fatalf("card archive: %d %s", result.Code, result.Body.String())
	}
	if result := externalRequest(server, http.MethodGet, "/api/archive", "", ownerA); result.Code != http.StatusOK || !strings.Contains(result.Body.String(), "own file") || strings.Contains(result.Body.String(), "private card") {
		t.Fatalf("archive list: %d %s", result.Code, result.Body.String())
	}
	restore := strings.Replace(valid, `"archived":true`, `"archived":false`, 1)
	if result := externalRequest(server, http.MethodPatch, path, restore, ownerA); result.Code != http.StatusOK || store.resources[fileID].Archived {
		t.Fatalf("restore: %d", result.Code)
	}
	if result := externalRequest(server, http.MethodGet, "/api/archive?page=0", "", ownerA); result.Code != http.StatusBadRequest {
		t.Fatalf("invalid page: %d", result.Code)
	}
}
