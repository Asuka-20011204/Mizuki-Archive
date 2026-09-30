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

// topicHTTPStore 模拟会话范围持久化，检查跨账号操作不会触及他人专题。
type topicHTTPStore struct {
	*multiUserHTTPStore
	topics map[string]model.TopicDetail
	owners map[string]string
}

// SaveTopic 拒绝越权引用和越权更新，并为可见条目填充名称。
func (store *topicHTTPStore) SaveTopic(ctx context.Context, id string, input model.TopicInput, update bool) (model.TopicDetail, error) {
	owner, _ := repository.UserIDFromContext(ctx)
	if update && store.owners[id] != owner {
		return model.TopicDetail{}, repository.ErrNotFound
	}
	for _, section := range input.Sections {
		for _, item := range section.Items {
			if item.ID != strings.Repeat("a", 32) || item.Source != "file" || owner != "user-a" {
				return model.TopicDetail{}, repository.ErrNotFound
			}
		}
	}
	sections := make([]model.TopicSection, 0, len(input.Sections))
	for _, section := range input.Sections {
		items := make([]model.TopicItem, 0, len(section.Items))
		for _, item := range section.Items {
			items = append(items, model.TopicItem{Source: item.Source, ID: item.ID, Name: "游戏"})
		}
		sections = append(sections, model.TopicSection{Title: section.Title, Items: items})
	}
	detail := model.TopicDetail{TopicSummary: model.TopicSummary{ID: id, Title: input.Title, Intro: input.Intro, SectionCount: len(sections), CreatedAt: time.Now().UTC()}, Sections: sections}
	store.owners[id] = owner
	store.topics[id] = detail
	return detail, nil
}

// ListTopics 只返回当前账号的专题摘要。
func (store *topicHTTPStore) ListTopics(ctx context.Context) ([]model.TopicSummary, error) {
	owner, _ := repository.UserIDFromContext(ctx)
	list := []model.TopicSummary{}
	for id, entry := range store.topics {
		if store.owners[id] == owner {
			list = append(list, entry.TopicSummary)
		}
	}
	return list, nil
}

// GetTopic 对不存在与非本人专题统一返回未找到。
func (store *topicHTTPStore) GetTopic(ctx context.Context, id string) (model.TopicDetail, error) {
	owner, _ := repository.UserIDFromContext(ctx)
	if store.owners[id] != owner {
		return model.TopicDetail{}, repository.ErrNotFound
	}
	return store.topics[id], nil
}

// DeleteTopic 不允许删除其他账号的专题。
func (store *topicHTTPStore) DeleteTopic(ctx context.Context, id string) error {
	owner, _ := repository.UserIDFromContext(ctx)
	if store.owners[id] != owner {
		return repository.ErrNotFound
	}
	delete(store.topics, id)
	delete(store.owners, id)
	return nil
}

// TestTopicsHTTPBoundary 覆盖匿名、空列表、严格 JSON、跨用户引用和专题 CRUD。
func TestTopicsHTTPBoundary(t *testing.T) {
	base := &multiUserHTTPStore{resources: map[string]model.Resource{}, sessions: map[string]string{}, users: map[string]model.User{}}
	store := &topicHTTPStore{multiUserHTTPStore: base, topics: map[string]model.TopicDetail{}, owners: map[string]string{}}
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
	topics, err := service.NewTopics(store)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{Resources: resources, Auth: auth, Topics: topics, Origin: "http://localhost:5173"})
	if err != nil {
		t.Fatal(err)
	}
	ownerA, ownerB := userCookie(t, auth, "user-a"), userCookie(t, auth, "user-b")
	path := "/api/topics"
	valid := `{"title":"游戏","intro":"收藏","cover_file_id":null,"sections":[{"title":"攻略","items":[{"source":"file","id":"` + strings.Repeat("a", 32) + `"}]}]}`
	if result := externalRequest(server, http.MethodGet, path, "", nil); result.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", result.Code)
	}
	if result := externalRequest(server, http.MethodGet, path, "", ownerA); result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"data":[]`) {
		t.Fatalf("empty: %d %s", result.Code, result.Body.String())
	}
	for _, body := range []string{valid + `{}`, strings.Replace(valid, `"title":"游戏"`, `"title":"游戏","user_id":"user-a"`, 1), strings.Replace(valid, `"title":"游戏"`, `"title":" "`, 1)} {
		if result := externalRequest(server, http.MethodPost, path, body, ownerA); result.Code != http.StatusBadRequest {
			t.Fatalf("invalid: %d %s", result.Code, result.Body.String())
		}
	}
	withName := strings.Replace(valid, `"source":"file"`, `"name":"伪造","source":"file"`, 1)
	if result := externalRequest(server, http.MethodPost, path, withName, ownerA); result.Code != http.StatusBadRequest {
		t.Fatalf("output-only name accepted: %d", result.Code)
	}
	if result := externalRequest(server, http.MethodPost, path, valid, ownerB); result.Code != http.StatusNotFound {
		t.Fatalf("foreign reference: %d", result.Code)
	}
	created := externalRequest(server, http.MethodPost, path, valid, ownerA)
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"name":"游戏"`) || strings.Contains(created.Body.String(), "storage_key") {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var id string
	for key := range store.topics {
		id = key
	}
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		if result := externalRequest(server, method, path+"/"+id, valid, ownerB); result.Code != http.StatusNotFound {
			t.Fatalf("foreign %s: %d", method, result.Code)
		}
	}
	if result := externalRequest(server, http.MethodPut, path+"/"+id, valid, ownerA); result.Code != http.StatusOK {
		t.Fatalf("update: %d %s", result.Code, result.Body.String())
	}
	if result := externalRequest(server, http.MethodDelete, path+"/"+id, "", ownerA); result.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", result.Code)
	}
	if result := externalRequest(server, http.MethodGet, path+"/"+id, "", ownerA); result.Code != http.StatusNotFound {
		t.Fatalf("deleted: %d", result.Code)
	}
}
