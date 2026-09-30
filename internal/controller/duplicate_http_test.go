package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
	"mizuki-archive/internal/service"
)

// duplicateHTTPStore 在 HTTP 测试中保留真实的账号边界，但不连接数据库。
type duplicateHTTPStore struct {
	*externalHTTPStore
}

// FindFileDuplicate 仅对当前账号同哈希文件返回提示，排除新文件自身。
func (store *duplicateHTTPStore) FindFileDuplicate(ctx context.Context, hash, excludeID string) (*model.DuplicateHint, error) {
	owner, _ := repository.UserIDFromContext(ctx)
	for id, item := range store.resources {
		if id != excludeID && item.OwnerID == owner && item.SHA256 == hash {
			return &model.DuplicateHint{ID: id, Name: item.Name}, nil
		}
	}
	return nil, nil
}

// FindExternalDuplicate 仅在当前账号内比较位置摘要，不访问用户提供的地址。
func (store *duplicateHTTPStore) FindExternalDuplicate(ctx context.Context, key, excludeID string) (*model.DuplicateHint, error) {
	owner, _ := repository.UserIDFromContext(ctx)
	for id, item := range store.cards {
		if id != excludeID && store.owners[id] == owner && model.ExternalLinkKey(item.Location) == key {
			return &model.DuplicateHint{ID: id, Name: item.Title}, nil
		}
	}
	return nil, nil
}

// TestDuplicateHTTPResponses 验证登录后的文件与卡片保存响应含提示、跨账号不泄漏且不拒绝重复保存。
func TestDuplicateHTTPResponses(t *testing.T) {
	base := &multiUserHTTPStore{resources: map[string]model.Resource{}, sessions: map[string]string{}, users: map[string]model.User{}}
	store := &duplicateHTTPStore{externalHTTPStore: &externalHTTPStore{multiUserHTTPStore: base, cards: map[string]model.ExternalResource{}, owners: map[string]string{}}}
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
	server, err := New(Config{Resources: files, ExternalResources: cards, Auth: auth, Origin: "http://localhost:5173"})
	if err != nil {
		t.Fatal(err)
	}
	owner := userCookie(t, auth, "user-a")
	other := userCookie(t, auth, "user-b")
	// uploadAs 将同一文件按指定登录身份上传，并解析用户可见的响应模型。
	uploadAs := func(cookie *http.Cookie) model.Resource {
		request := uploadRequest(t, "notes.txt", []byte("hello same owner\n"))
		request.AddCookie(cookie)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusCreated {
			t.Fatalf("upload: %d %s", response.Code, response.Body.String())
		}
		var payload struct {
			Data model.Resource `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload.Data
	}
	firstFile := uploadAs(owner)
	if firstFile.Duplicate != nil || uploadAs(other).Duplicate != nil {
		t.Fatal("a different account must not receive another user's file hint")
	}
	secondFile := uploadAs(owner)
	if secondFile.Duplicate == nil || secondFile.Duplicate.ID != firstFile.ID || secondFile.ID == firstFile.ID {
		t.Fatalf("same-account file hint: %+v", secondFile)
	}
	input := `{"title":"教程","location":"https://example.org/doc?a=1&b=2","resource_type":"文档"}`
	// createCardAs 检查保存成功码，并返回非公开内部字段的卡片 JSON。
	createCardAs := func(cookie *http.Cookie, body string) model.ExternalResource {
		response := externalRequest(server, http.MethodPost, "/api/external-resources", body, cookie)
		if response.Code != http.StatusCreated {
			t.Fatalf("create card: %d %s", response.Code, response.Body.String())
		}
		var payload struct {
			Data model.ExternalResource `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload.Data
	}
	firstCard := createCardAs(owner, input)
	if firstCard.Duplicate != nil || createCardAs(other, input).Duplicate != nil {
		t.Fatal("a different account must not receive another user's card hint")
	}
	secondCard := createCardAs(owner, `{"title":"另一张","location":"HTTPS://EXAMPLE.ORG:443/doc?b=2&a=1#part","resource_type":"文档"}`)
	if secondCard.Duplicate == nil || secondCard.Duplicate.ID != firstCard.ID || secondCard.ID == firstCard.ID {
		t.Fatalf("same-account card hint: %+v", secondCard)
	}
	if strings.Contains(secondCard.Duplicate.Name, "example.org") {
		t.Fatal("duplicate hint unexpectedly included address")
	}
}
