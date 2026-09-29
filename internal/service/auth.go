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

// NewAuth 校验持久层、管理员名及 bcrypt 哈希，防止配置错误延迟到登录时才暴露。
func NewAuth(store repository.Store, username string, passwordHash []byte) (*Auth, error) {
	if store == nil || username == "" {
		return nil, errors.New("invalid authentication configuration")
	}
	// 启动时验证管理员哈希格式，避免把配置错误误报为每次登录的密码错误。
	if _, err := bcrypt.Cost(passwordHash); err != nil {
		return nil, fmt.Errorf("invalid password hash: %w", err)
	}
	return &Auth{store: store, username: username, passwordHash: passwordHash}, nil
}

// Username 返回服务端配置的管理员名，供已认证的 HTTP 接口显示身份。
func (auth *Auth) Username() string { return auth.username }

// hashToken 对浏览器会话令牌取摘要，使数据库无需保存可直接使用的 Cookie。
func hashToken(token string) string {
	// 数据库只保存令牌摘要，泄露会话表也不能直接拿到浏览器中的原始令牌。
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

// Login 核验单管理员凭据，生成随机会话令牌并只将摘要与过期时间持久化。
func (auth *Auth) Login(ctx context.Context, username, password string) (string, error) {
	// 无论账号是否匹配都执行 bcrypt 校验，减少由响应时间泄露账号的机会。
	usernameDigest := sha256.Sum256([]byte(username))
	adminDigest := sha256.Sum256([]byte(auth.username))
	validUsername := subtle.ConstantTimeCompare(usernameDigest[:], adminDigest[:]) == 1
	validPassword := bcrypt.CompareHashAndPassword(auth.passwordHash, []byte(password)) == nil
	if !validUsername || !validPassword {
		return "", ErrInvalidCredentials
	}
	// Cookie 中是不可预测的原始令牌；Repository 只收到摘要，令牌可随时撤销。
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

// Validate 拒绝无效令牌格式，再查询数据库中未过期且未撤销的会话。
func (auth *Auth) Validate(ctx context.Context, token string) (bool, error) {
	// 先拒绝格式不符的 Cookie，避免无效请求也访问数据库；过期时间由持久层判断。
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return false, nil
	}
	return auth.store.HasSession(ctx, hashToken(token))
}

// Logout 从持久层撤销令牌摘要；仅清除浏览器 Cookie 不足以阻止令牌重放。
func (auth *Auth) Logout(ctx context.Context, token string) error {
	// 删除数据库会话而非仅清除浏览器 Cookie，使已泄露的旧 Cookie 也立即失效。
	if err := auth.store.DeleteSession(ctx, hashToken(token)); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}
