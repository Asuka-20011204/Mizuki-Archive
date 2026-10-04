package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
)

type emailIdentityFake struct {
	*fakeStore
	users      map[string]model.User
	sessions   map[string]string
	challenges map[string]model.EmailChallenge
	consumed   map[string]bool
}

// EnsureAdminUser 模拟兼容旧密码管理员迁移到用户表。
func (store *emailIdentityFake) EnsureAdminUser(_ context.Context, username string, hash []byte) (model.User, error) {
	user := model.User{ID: "admin-user", Username: username, PasswordHash: hash}
	store.users[user.Email] = user
	return user, nil
}

// CreateUser 模拟邮箱唯一约束和新用户生成。
func (store *emailIdentityFake) CreateUser(_ context.Context, email, username string, hash []byte) (model.User, error) {
	if _, exists := store.users[email]; exists {
		return model.User{}, repository.ErrUserExists
	}
	user := model.User{ID: "user-" + email, Username: username, Email: email, PasswordHash: hash}
	store.users[email] = user
	return user, nil
}

// GetUserByUsername 模拟普通账号密码登录按用户名查找。
func (store *emailIdentityFake) GetUserByUsername(_ context.Context, username string) (model.User, error) {
	for _, user := range store.users {
		if user.Username == username {
			return user, nil
		}
	}
	return model.User{}, repository.ErrNotFound
}

// GetUserByEmail 模拟按规范化邮箱查找用户。
func (store *emailIdentityFake) GetUserByEmail(_ context.Context, email string) (model.User, error) {
	user, exists := store.users[email]
	if !exists {
		return model.User{}, repository.ErrNotFound
	}
	return user, nil
}

// GetUserByID 模拟会话后的用户展示读取。
func (store *emailIdentityFake) GetUserByID(_ context.Context, id string) (model.User, error) {
	for _, user := range store.users {
		if user.ID == id {
			return user, nil
		}
	}
	return model.User{}, repository.ErrNotFound
}

// SaveUserSession 记录带用户归属的测试会话。
func (store *emailIdentityFake) SaveUserSession(_ context.Context, userID, hash string, _ time.Time) error {
	store.sessions[hash] = userID
	return nil
}

// GetSessionUser 返回测试会话绑定的用户。
func (store *emailIdentityFake) GetSessionUser(_ context.Context, hash string) (string, bool, error) {
	userID, exists := store.sessions[hash]
	return userID, exists, nil
}

// FinalizeOwnership 在内存测试中不需要执行 SQL 回填。
func (store *emailIdentityFake) FinalizeOwnership(context.Context, string) error { return nil }

// ReserveEmailChallenge 模拟按邮箱和用途独立限频。
func (store *emailIdentityFake) ReserveEmailChallenge(_ context.Context, challenge model.EmailChallenge, now time.Time) (bool, error) {
	key := challenge.Email + "\x00" + challenge.Purpose
	if current, exists := store.challenges[key]; exists && current.NextRequestAt.After(now) {
		return false, nil
	}
	store.challenges[key] = challenge
	store.consumed[key] = false
	return true, nil
}

// ConsumeEmailChallenge 模拟一次性消费验证码。
func (store *emailIdentityFake) ConsumeEmailChallenge(_ context.Context, email, purpose, digest string, now time.Time) (bool, error) {
	key := email + "\x00" + purpose
	challenge, exists := store.challenges[key]
	if !exists || store.consumed[key] || !challenge.ExpiresAt.After(now) || challenge.Digest != digest {
		return false, nil
	}
	store.consumed[key] = true
	return true, nil
}

// DeleteEmailChallenge 只清理指定摘要的发送失败挑战。
func (store *emailIdentityFake) DeleteEmailChallenge(_ context.Context, email, purpose, digest string) error {
	key := email + "\x00" + purpose
	if challenge, exists := store.challenges[key]; exists && challenge.Digest == digest {
		delete(store.challenges, key)
		delete(store.consumed, key)
	}
	return nil
}

type emailSenderFake struct {
	code string
	err  error
}

// SendCode 记录测试验证码，不打印真实验证码。
func (sender *emailSenderFake) SendCode(_ context.Context, _ string, code string) error {
	sender.code = code
	return sender.err
}

// newEmailIdentityFake 创建隔离的多用户测试仓储。
func newEmailIdentityFake() *emailIdentityFake {
	return &emailIdentityFake{fakeStore: newFakeStore(), users: map[string]model.User{}, sessions: map[string]string{}, challenges: map[string]model.EmailChallenge{}, consumed: map[string]bool{}}
}

// TestEmailRegistrationAndLogin 验证新邮箱注册、会话归属、验证码一次性消费和再次登录。
func TestEmailRegistrationAndLogin(t *testing.T) {
	store := newEmailIdentityFake()
	sender := &emailSenderFake{}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("correct horse battery staple"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := NewAuth(store, "owner", passwordHash)
	if err != nil {
		t.Fatal(err)
	}
	emailAuth, err := NewEmailAuth(store, store, auth, sender, []byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	if err := emailAuth.RequestRegistrationCode(context.Background(), "User@Example.com"); err != nil {
		t.Fatal(err)
	}
	if err := emailAuth.RequestRegistrationCode(context.Background(), "user@example.com"); !errors.Is(err, ErrEmailCodeTooSoon) {
		t.Fatalf("expected resend cooldown, got %v", err)
	}
	token, err := emailAuth.RegisterWithCode(context.Background(), "user@example.com", sender.code, "emailuser", "correct horse battery staple")
	if err != nil || token == "" {
		t.Fatalf("registration failed: token=%q err=%v", token, err)
	}
	if _, err := emailAuth.RegisterWithCode(context.Background(), "user@example.com", sender.code, "emailuser", "correct horse battery staple"); !errors.Is(err, ErrEmailCodeInvalid) {
		t.Fatalf("expected one-time rejection, got %v", err)
	}
	if err := emailAuth.RequestLoginCode(context.Background(), "user@example.com"); err != nil {
		t.Fatal(err)
	}
	loginToken, _, err := emailAuth.LoginWithCode(context.Background(), "user@example.com", sender.code)
	if err != nil || loginToken == "" {
		t.Fatalf("email login failed: token=%q err=%v", loginToken, err)
	}
	if store.sessions == nil {
		t.Fatal("expected user session")
	}
}

// TestEmailRegistrationSenderFailure 验证邮件服务不可用时不会留下可消费验证码。
func TestEmailRegistrationSenderFailure(t *testing.T) {
	store := newEmailIdentityFake()
	sender := &emailSenderFake{err: errors.New("offline")}
	passwordHash, _ := bcrypt.GenerateFromPassword([]byte("correct horse battery staple"), bcrypt.MinCost)
	auth, _ := NewAuth(store, "owner", passwordHash)
	emailAuth, _ := NewEmailAuth(store, store, auth, sender, []byte("01234567890123456789012345678901"))
	if err := emailAuth.RequestRegistrationCode(context.Background(), "user@example.com"); !errors.Is(err, ErrEmailUnavailable) {
		t.Fatalf("got %v", err)
	}
	if len(store.challenges) != 0 {
		t.Fatal("failed delivery must clear challenge")
	}
}
