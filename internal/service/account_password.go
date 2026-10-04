package service

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
	"mizuki-archive/internal/repository"
)

var ErrAccountInput = errors.New("invalid account registration fields")

// HashAccountPassword 在验证码消费前校验并哈希密码，拒绝 bcrypt 无法完整处理的超长输入。
func HashAccountPassword(username, password string) (string, []byte, error) {
	if username != strings.TrimSpace(username) || len(username) < 3 || len(username) > 32 {
		return "", nil, ErrAccountInput
	}
	if (username[0] < 'a' || username[0] > 'z') && (username[0] < 'A' || username[0] > 'Z') {
		return "", nil, ErrAccountInput
	}
	for _, character := range username {
		if character != '_' && character != '-' && !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') && !(character >= '0' && character <= '9') {
			return "", nil, ErrAccountInput
		}
	}
	if utf8.RuneCountInString(password) < 15 || len(password) > 72 || !utf8.ValidString(password) {
		return "", nil, ErrAccountInput
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", nil, err
	}
	return username, hash, nil
}

// LoginWithPassword 在服务端查询持久用户；兼容管理员环境凭据和旧无密码用户的验证码登录。
func (auth *Auth) LoginWithPassword(ctx context.Context, identity, password string) (string, string, error) {
	identity = strings.TrimSpace(identity)
	if identity == auth.username {
		token, err := auth.Login(ctx, identity, password)
		return token, auth.username, err
	}
	store, ok := auth.store.(repository.IdentityStore)
	if !ok {
		return "", "", ErrInvalidCredentials
	}
	user, err := store.GetUserByUsername(ctx, identity)
	if errors.Is(err, repository.ErrNotFound) {
		if email, normalizeErr := NormalizeEmail(identity); normalizeErr == nil {
			user, err = store.GetUserByEmail(ctx, email)
		} else if phone, normalizeErr := NormalizeMainlandPhone(identity); normalizeErr == nil {
			if phoneStore, enabled := auth.store.(repository.PhoneIdentityStore); enabled {
				user, err = phoneStore.GetUserByPhone(ctx, phone)
			}
		}
	}
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return "", "", err
	}
	// 未知账号与无密码旧账号均做一次 bcrypt 运算，不暴露账号存在状态。
	hash := user.PasswordHash
	if err != nil || len(hash) == 0 {
		hash = auth.passwordHash
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(password)) != nil || err != nil || len(user.PasswordHash) == 0 {
		return "", "", ErrInvalidCredentials
	}
	token, err := auth.CreateSessionForUser(ctx, user.ID)
	return token, user.Username, err
}
