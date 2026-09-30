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

// relationHTTPStore 模拟关联的用户范围权限，与已有登录和资料测试仓储组合。
type relationHTTPStore struct {
	*multiUserHTTPStore
	links map[string]model.ResourceRelation
	owner string
}

// CreateRelation 核验两端归属，模拟实际数据库跨用户访问统一返回不存在。
func (store *relationHTTPStore) CreateRelation(ctx context.Context, source, target model.InboxSelection, id string) (model.ResourceRelation, error) {
	owner, _ := repository.UserIDFromContext(ctx)
	if source.ID != strings.Repeat("a", 32) || target.ID != strings.Repeat("b", 32) || owner != store.owner {
		return model.ResourceRelation{}, repository.ErrNotFound
	}
	link := model.ResourceRelation{ID: id, Target: target, Name: "攻略", CreatedAt: time.Now().UTC()}
	store.links[id] = link
	return link, nil
}

// ListRelations 对匿名或越权来源返回统一未找到，只返回已保存的关联。
func (store *relationHTTPStore) ListRelations(ctx context.Context, source model.InboxSelection) ([]model.ResourceRelation, error) {
	owner, _ := repository.UserIDFromContext(ctx)
	if source.ID != strings.Repeat("a", 32) || owner != store.owner {
		return nil, repository.ErrNotFound
	}
	items := []model.ResourceRelation{}
	for _, link := range store.links {
		items = append(items, link)
	}
	return items, nil
}

// DeleteRelation 只允许关系所属账号删除，并保持其他账号资料不变。
func (store *relationHTTPStore) DeleteRelation(ctx context.Context, id string) error {
	owner, _ := repository.UserIDFromContext(ctx)
	if owner != store.owner {
		return repository.ErrNotFound
	}
	if _, exists := store.links[id]; !exists {
		return repository.ErrNotFound
	}
	delete(store.links, id)
	return nil
}

// TestRelationsHTTPBoundary 验证匿名拒绝、格式校验、跨用户伪装、创建与删除响应。
func TestRelationsHTTPBoundary(t *testing.T) {
	base := &multiUserHTTPStore{resources: map[string]model.Resource{}, sessions: map[string]string{}, users: map[string]model.User{}}
	store := &relationHTTPStore{multiUserHTTPStore: base, links: map[string]model.ResourceRelation{}, owner: "user-a"}
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
	relations, err := service.NewRelations(store)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{Resources: resources, Auth: auth, Relations: relations, Origin: "http://localhost:5173"})
	if err != nil {
		t.Fatal(err)
	}
	ownerA, ownerB := userCookie(t, auth, "user-a"), userCookie(t, auth, "user-b")
	path := "/api/relations"
	valid := `{"source":{"source":"file","id":"` + strings.Repeat("a", 32) + `"},"target":{"source":"external","id":"` + strings.Repeat("b", 32) + `"}}`
	if response := externalRequest(server, http.MethodPost, path, valid, nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", response.Code)
	}
	for _, body := range []string{valid + `{}`, `{"source":{"source":"invalid","id":"` + strings.Repeat("a", 32) + `"}}`, valid[:len(valid)-1] + `,"user_id":"user-a"}`} {
		if response := externalRequest(server, http.MethodPost, path, body, ownerA); response.Code != http.StatusBadRequest {
			t.Fatalf("invalid input: %d %s", response.Code, response.Body.String())
		}
	}
	if response := externalRequest(server, http.MethodPost, path, valid, ownerB); response.Code != http.StatusNotFound {
		t.Fatalf("foreign endpoint: %d", response.Code)
	}
	created := externalRequest(server, http.MethodPost, path, valid, ownerA)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"name":"攻略"`) {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	for _, item := range []struct {
		cookie *http.Cookie
		status int
	}{{ownerA, http.StatusOK}, {ownerB, http.StatusNotFound}} {
		response := externalRequest(server, http.MethodGet, path+"/file/"+strings.Repeat("a", 32), "", item.cookie)
		if response.Code != item.status {
			t.Fatalf("list: %d %s", response.Code, response.Body.String())
		}
	}
	for id := range store.links {
		if response := externalRequest(server, http.MethodDelete, path+"/"+id, "", ownerB); response.Code != http.StatusNotFound {
			t.Fatalf("foreign delete: %d", response.Code)
		}
		if response := externalRequest(server, http.MethodDelete, path+"/"+id, "", ownerA); response.Code != http.StatusNoContent {
			t.Fatalf("delete: %d", response.Code)
		}
	}
}
