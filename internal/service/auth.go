package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"
	"mizuki-archive/internal/repository"
)

const SessionLifetime = 12 * time.Hour

var ErrInvalidCredentials = errors.New("invalid credentials")

// Auth 管理单管理员验证与可撤销的数据库会话，Controller 不接触密码哈希细节。
type Auth struct {
	store        repository.Store
	username     string
	passwordHash []byte
}

func NewAuth(store repository.Store, username string, passwordHash []byte) (*Auth, error) {
	if store == nil || username == "" {
		return nil, errors.New("invalid authentication configuration")
	}
	if _, err := bcrypt.Cost(passwordHash); err != nil {
		return nil, fmt.Errorf("invalid password hash: %w", err)
	}
	return &Auth{store: store, username: username, passwordHash: passwordHash}, nil
}

func (auth *Auth) Username() string { return auth.username }

func hashToken(token string) string {
	// 数据库只保存令牌摘要，泄露会话表也不能直接拿到浏览器中的原始令牌。
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func (auth *Auth) Login(ctx context.Context, username, password string) (string, error) {
	// 无论账号是否匹配都执行 bcrypt 校验，减少由响应时间泄露账号的机会。
	usernameDigest := sha256.Sum256([]byte(username))
	adminDigest := sha256.Sum256([]byte(auth.username))
	validUsername := subtle.ConstantTimeCompare(usernameDigest[:], adminDigest[:]) == 1
	validPassword := bcrypt.CompareHashAndPassword(auth.passwordHash, []byte(password)) == nil
	if !validUsername || !validPassword {
		return "", ErrInvalidCredentials
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", fmt.Errorf("generate session: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	if err := auth.store.SaveSession(ctx, hashToken(token), time.Now().UTC().Add(SessionLifetime)); err != nil {
		return "", fmt.Errorf("save session: %w", err)
	}
	return token, nil
}

func (auth *Auth) Validate(ctx context.Context, token string) (bool, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return false, nil
	}
	return auth.store.HasSession(ctx, hashToken(token))
}

func (auth *Auth) Logout(ctx context.Context, token string) error {
	if err := auth.store.DeleteSession(ctx, hashToken(token)); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}
