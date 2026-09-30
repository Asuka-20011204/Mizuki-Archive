package controller

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
	"mizuki-archive/internal/service"
)

type inboxHTTPStore struct {
	*externalHTTPStore
}

// ListPendingResources 在测试中按用户筛选文件，模拟数据库的用户归属边界。
func (store *inboxHTTPStore) ListPendingResources(ctx context.Context, _, _ int) ([]model.Resource, error) {
	owner, _ := repository.UserIDFromContext(ctx)
	items := []model.Resource{}
	for _, item := range store.resources {
		if item.OwnerID == owner && item.OrganizationStatus == "pending" {
			items = append(items, item)
		}
	}
	return items, nil
}

// ListPendingExternalResources 在测试中按用户筛选外部卡片。
func (store *inboxHTTPStore) ListPendingExternalResources(ctx context.Context, _, _ int) ([]model.ExternalResource, error) {
	owner, _ := repository.UserIDFromContext(ctx)
	items := []model.ExternalResource{}
	for id, item := range store.cards {
		if store.owners[id] == owner && item.OrganizationStatus == "pending" {
			items = append(items, item)
		}
	}
	return items, nil
}

// SetResourceOrganizationStatus 在测试中强制文件归属后才修改状态。
func (store *inboxHTTPStore) SetResourceOrganizationStatus(ctx context.Context, id, status string) error {
	owner, _ := repository.UserIDFromContext(ctx)
	item, ok := store.resources[id]
	if !ok || owner != item.OwnerID {
		return repository.ErrNotFound
	}
	item.OrganizationStatus = status
	store.resources[id] = item
	return nil
}

// SetExternalOrganizationStatus 在测试中只修改当前用户卡片的整理状态。
func (store *inboxHTTPStore) SetExternalOrganizationStatus(ctx context.Context, id, status string) error {
	item, err := store.GetExternalResource(ctx, id)
	if err != nil {
		return err
	}
	item.OrganizationStatus = status
	store.cards[id] = item
	return nil
}

// TestInboxHTTPIsolation 验证上传文件和新卡片都待整理，跨用户读取和写入均失败。
func TestInboxHTTPIsolation(t *testing.T) {
	base := &multiUserHTTPStore{resources: map[string]model.Resource{}, sessions: map[string]string{}, users: map[string]model.User{}}
	external := &externalHTTPStore{multiUserHTTPStore: base, cards: map[string]model.ExternalResource{}, owners: map[string]string{}}
	store := &inboxHTTPStore{externalHTTPStore: external}
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
	inbox, err := service.NewInbox(store)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{Resources: files, ExternalResources: cards, Inbox: inbox, Auth: auth, Origin: "http://localhost:5173"})
	if err != nil {
		t.Fatal(err)
	}
	ownerA := userCookie(t, auth, "user-a")
	ownerB := userCookie(t, auth, "user-b")
	fileID := strings.Repeat("c", 32)
	store.resources[fileID] = model.Resource{ID: fileID, OwnerID: "user-b", Name: "private.pdf", OrganizationStatus: "pending", CreatedAt: time.Now().UTC()}
	created := externalRequest(server, http.MethodPost, "/api/external-resources", `{"title":"private card","location":"local folder","resource_type":"note"}`, ownerB)
	if created.Code != http.StatusCreated {
		t.Fatalf("create card: %d %s", created.Code, created.Body.String())
	}
	var cardID string
	for id := range store.cards {
		cardID = id
	}
	if result := externalRequest(server, http.MethodGet, "/api/inbox", "", nil); result.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous inbox: %d", result.Code)
	}
	if result := externalRequest(server, http.MethodGet, "/api/inbox?page=10001", "", ownerB); result.Code != http.StatusBadRequest {
		t.Fatalf("invalid inbox page: %d", result.Code)
	}
	for _, test := range []struct {
		cookie      *http.Cookie
		wantPrivate bool
	}{
		{ownerA, false}, {ownerB, true},
	} {
		result := externalRequest(server, http.MethodGet, "/api/inbox", "", test.cookie)
		if result.Code != http.StatusOK || strings.Contains(result.Body.String(), "private.pdf") != test.wantPrivate || strings.Contains(result.Body.String(), "private card") != test.wantPrivate {
			t.Fatalf("inbox visibility: %d %s", result.Code, result.Body.String())
		}
	}
	for _, source := range []struct{ name, id string }{{"file", fileID}, {"external", cardID}} {
		path := "/api/inbox/" + source.name + "/" + source.id
		if response := externalRequest(server, http.MethodPatch, path, `{"status":"organized"}`, ownerA); response.Code != http.StatusNotFound {
			t.Fatalf("cross-user edit: %d", response.Code)
		}
		if response := externalRequest(server, http.MethodPatch, path, `{"status":"deleted"}`, ownerB); response.Code != http.StatusBadRequest {
			t.Fatalf("invalid status: %d", response.Code)
		}
		if response := externalRequest(server, http.MethodPatch, path, `{"status":"organized"}`, ownerB); response.Code != http.StatusNoContent {
			t.Fatalf("own completion: %d %s", response.Code, response.Body.String())
		}
		if response := externalRequest(server, http.MethodPatch, path, `{"status":"organized"}`, ownerB); response.Code != http.StatusNoContent {
			t.Fatalf("repeat completion: %d %s", response.Code, response.Body.String())
		}
	}
	if response := externalRequest(server, http.MethodGet, "/api/inbox", "", ownerB); response.Code != http.StatusOK || strings.Contains(response.Body.String(), "private card") || strings.Contains(response.Body.String(), "private.pdf") {
		t.Fatalf("completed items still pending: %d %s", response.Code, response.Body.String())
	}
	if store.cards[cardID].Status != "pending" {
		t.Fatal("completion changed external link status")
	}
}
