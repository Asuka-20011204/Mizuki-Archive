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

type phoneIdentityFake struct {
	*emailIdentityFake
	phones map[string]model.User
	codes  map[string]model.PhoneChallenge
}

// CreatePhoneUser 模拟手机号唯一约束，不复用邮箱身份。
func (store *phoneIdentityFake) CreatePhoneUser(_ context.Context, phone, username string, hash []byte) (model.User, error) {
	if _, exists := store.phones[phone]; exists {
		return model.User{}, repository.ErrUserExists
	}
	user := model.User{ID: "phone-user-" + phone, Username: username, Phone: phone, PasswordHash: hash}
	store.phones[phone] = user
	store.users[user.ID] = user
	return user, nil
}

// GetUserByPhone 查询测试账号，未知手机号返回未找到。
func (store *phoneIdentityFake) GetUserByPhone(_ context.Context, phone string) (model.User, error) {
	user, exists := store.phones[phone]
	if !exists {
		return model.User{}, repository.ErrNotFound
	}
	return user, nil
}

// ReservePhoneChallenge 在测试中按手机号和用途模拟冷却期。
func (store *phoneIdentityFake) ReservePhoneChallenge(_ context.Context, value model.PhoneChallenge, now time.Time) (bool, error) {
	key := value.Phone + "\x00" + value.Purpose
	if current, exists := store.codes[key]; exists && current.NextRequestAt.After(now) {
		return false, nil
	}
	store.codes[key] = value
	return true, nil
}

// ConsumePhoneChallenge 校验摘要和有效期，并在成功时删除。
func (store *phoneIdentityFake) ConsumePhoneChallenge(_ context.Context, phone, purpose, digest string, now time.Time) (bool, error) {
	key := phone + "\x00" + purpose
	value, exists := store.codes[key]
	if !exists || !value.ExpiresAt.After(now) || value.Digest != digest {
		return false, nil
	}
	delete(store.codes, key)
	return true, nil
}

// DeletePhoneChallenge 只清除本次发送失败对应的验证码。
func (store *phoneIdentityFake) DeletePhoneChallenge(_ context.Context, phone, purpose, digest string) error {
	key := phone + "\x00" + purpose
	if store.codes[key].Digest == digest {
		delete(store.codes, key)
	}
	return nil
}

type phoneSenderFake struct {
	code string
	err  error
}

// SendCode 仅在测试中保存验证码，不输出到日志。
func (sender *phoneSenderFake) SendCode(_ context.Context, _ string, code string) error {
	sender.code = code
	return sender.err
}

// TestNormalizeMainlandPhone 验证本地号码和国际前缀统一存储，并拒绝含糊输入。
func TestNormalizeMainlandPhone(t *testing.T) {
	for _, input := range []string{"13800138000", "+8613800138000"} {
		phone, err := NormalizeMainlandPhone(input)
		if err != nil || phone != "+8613800138000" {
			t.Fatalf("input %q: phone=%q err=%v", input, phone, err)
		}
	}
	for _, input := range []string{"", "12800138000", "1380013800", "138001380000", "1380013800a", "138 0013 8000", "008613800138000", "+8613800138000\n", "１3800138000"} {
		if _, err := NormalizeMainlandPhone(input); err == nil {
			t.Fatalf("input %q accepted", input)
		}
	}
}

// TestPhoneRegistrationAndLogin 验证一次性注册、登录、冷却与独立用户归属。
func TestPhoneRegistrationAndLogin(t *testing.T) {
	store := &phoneIdentityFake{emailIdentityFake: newEmailIdentityFake(), phones: map[string]model.User{}, codes: map[string]model.PhoneChallenge{}}
	sender := &phoneSenderFake{}
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct horse battery staple"), bcrypt.MinCost)
	auth, _ := NewAuth(store, "owner", hash)
	phoneAuth, err := NewPhoneAuth(store, store, auth, sender, []byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := phoneAuth.RequestRegistrationCode(ctx, "13800138000"); err != nil {
		t.Fatal(err)
	}
	if err := phoneAuth.RequestRegistrationCode(ctx, "+8613800138000"); !errors.Is(err, ErrPhoneCodeTooSoon) {
		t.Fatalf("cooldown: %v", err)
	}
	token, err := phoneAuth.RegisterWithCode(ctx, "+8613800138000", sender.code, "phoneuser", "correct horse battery staple")
	if err != nil || token == "" {
		t.Fatalf("register: token=%q err=%v", token, err)
	}
	if _, err := phoneAuth.RegisterWithCode(ctx, "13800138000", sender.code, "phoneuser", "correct horse battery staple"); !errors.Is(err, ErrPhoneCodeInvalid) {
		t.Fatalf("replayed code: %v", err)
	}
	if err := phoneAuth.RequestLoginCode(ctx, "13800138000"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := phoneAuth.LoginWithCode(ctx, "13800138000", sender.code); err != nil {
		t.Fatalf("login: %v", err)
	}
	if err := phoneAuth.RequestRegistrationCode(ctx, "13800138000"); !errors.Is(err, ErrPhoneAlreadyRegistered) {
		t.Fatalf("duplicate registration: %v", err)
	}
	if err := phoneAuth.RequestLoginCode(ctx, "13900139000"); !errors.Is(err, ErrPhoneNotRegistered) {
		t.Fatalf("unknown login: %v", err)
	}
}

// TestPhoneSendFailure 验证发送失败后不会留下可消费的挑战。
func TestPhoneSendFailure(t *testing.T) {
	store := &phoneIdentityFake{emailIdentityFake: newEmailIdentityFake(), phones: map[string]model.User{}, codes: map[string]model.PhoneChallenge{}}
	sender := &phoneSenderFake{err: errors.New("offline")}
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct horse battery staple"), bcrypt.MinCost)
	auth, _ := NewAuth(store, "owner", hash)
	phoneAuth, _ := NewPhoneAuth(store, store, auth, sender, []byte("01234567890123456789012345678901"))
	if err := phoneAuth.RequestRegistrationCode(context.Background(), "13800138000"); !errors.Is(err, ErrPhoneUnavailable) || len(store.codes) != 0 {
		t.Fatalf("send failure: err=%v, pending=%d", err, len(store.codes))
	}
}
