package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
)

type concurrentSessionStore struct {
	*fakeStore
	mutex       sync.Mutex
	sessions    map[string]string
	saveEntered chan struct{}
	release     chan struct{}
}

// EnsureAdminUser 提供并发会话测试所需的身份迁移接口，但不参与本用例断言。
func (store *concurrentSessionStore) EnsureAdminUser(context.Context, string, []byte) (model.User, error) {
	return model.User{ID: "admin"}, nil
}

// CreateUser 提供并发会话测试所需的注册接口，但不参与本用例断言。
func (store *concurrentSessionStore) CreateUser(context.Context, string, string, []byte) (model.User, error) {
	return model.User{}, repository.ErrNotFound
}

// GetUserByUsername 为并发会话测试补齐只读身份接口，不参与本用例。
func (store *concurrentSessionStore) GetUserByUsername(context.Context, string) (model.User, error) {
	return model.User{}, repository.ErrNotFound
}

// GetUserByEmail 提供并发会话测试所需的邮箱查询接口，但不参与本用例断言。
func (store *concurrentSessionStore) GetUserByEmail(context.Context, string) (model.User, error) {
	return model.User{}, repository.ErrNotFound
}

// GetUserByID 提供并发会话测试所需的用户查询接口，但不参与本用例断言。
func (store *concurrentSessionStore) GetUserByID(context.Context, string) (model.User, error) {
	return model.User{}, repository.ErrNotFound
}

// SaveUserSession 阻塞两个并发请求同时进入持久化边界，再记录令牌与用户的实际绑定。
func (store *concurrentSessionStore) SaveUserSession(_ context.Context, userID, hash string, _ time.Time) error {
	store.saveEntered <- struct{}{}
	<-store.release
	store.mutex.Lock()
	defer store.mutex.Unlock()
	store.sessions[hash] = userID
	return nil
}

// GetSessionUser 返回并发测试中已记录的用户归属。
func (store *concurrentSessionStore) GetSessionUser(_ context.Context, hash string) (string, bool, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	userID, exists := store.sessions[hash]
	return userID, exists, nil
}

// FinalizeOwnership 提供身份迁移接口；并发会话测试不需要执行数据库回填。
func (store *concurrentSessionStore) FinalizeOwnership(context.Context, string) error { return nil }

// TestCreateSessionForUserIsConcurrentSafe 验证两个邮箱登录并发签发会话时不会串用用户归属。
func TestCreateSessionForUserIsConcurrentSafe(t *testing.T) {
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("correct horse battery staple"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	store := &concurrentSessionStore{
		fakeStore:   newFakeStore(),
		sessions:    map[string]string{},
		saveEntered: make(chan struct{}, 2),
		release:     make(chan struct{}),
	}
	auth, err := NewAuth(store, "owner", passwordHash)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	type result struct {
		token  string
		userID string
		err    error
	}
	results := make(chan result, 2)
	for _, userID := range []string{"user-a", "user-b"} {
		go func(userID string) {
			<-start
			token, sessionErr := auth.CreateSessionForUser(context.Background(), userID)
			results <- result{token: token, userID: userID, err: sessionErr}
		}(userID)
	}
	close(start)
	for range 2 {
		<-store.saveEntered
	}
	close(store.release)

	for range 2 {
		current := <-results
		if current.err != nil {
			t.Fatalf("create session for %s: %v", current.userID, current.err)
		}
		store.mutex.Lock()
		boundUserID, exists := store.sessions[hashToken(current.token)]
		store.mutex.Unlock()
		if !exists || boundUserID != current.userID {
			t.Fatalf("session %q bound to %q, want %q", current.token, boundUserID, current.userID)
		}
	}
}
