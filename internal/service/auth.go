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
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"mizuki-archive/internal/repository"
)

const SessionLifetime = 12 * time.Hour

var ErrInvalidCredentials = errors.New("invalid credentials")

// Auth 管理兼容管理员密码和多用户可撤销会话，Controller 不接触密码哈希细节。
type Auth struct {
	store        repository.Store
	username     string
	passwordHash []byte
	userID       string
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

// SetUserID 将启动时迁移得到的管理员用户 ID 注入认证服务，之后密码会话也具备资料归属。
func (auth *Auth) SetUserID(userID string) { auth.userID = userID }

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
	return auth.createSession(ctx, auth.userID)
}

// createSession 为已完成身份核验的路径创建服务端会话；用户归属作为参数传入，避免并发请求修改共享认证状态。
func (auth *Auth) createSession(ctx context.Context, userID string) (string, error) {
	// Cookie 中是不可预测的原始令牌；Repository 只收到摘要，令牌可随时撤销。
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", fmt.Errorf("generate session: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	expiresAt := time.Now().UTC().Add(SessionLifetime)
	if identityStore, ok := auth.store.(repository.IdentityStore); ok {
		if userID == "" {
			return "", errors.New("missing authenticated user ID")
		}
		if err := identityStore.SaveUserSession(ctx, userID, hashToken(token), expiresAt); err != nil {
			return "", fmt.Errorf("save user session: %w", err)
		}
	} else if err := auth.store.SaveSession(ctx, hashToken(token), expiresAt); err != nil {
		return "", fmt.Errorf("save session: %w", err)
	}
	return token, nil
}

// CreateSessionForUser 在邮箱验证码完成身份核验后签发指定用户会话，不接受前端传入资料归属。
func (auth *Auth) CreateSessionForUser(ctx context.Context, userID string) (string, error) {
	if userID == "" {
		return "", errors.New("missing user ID")
	}
	return auth.createSession(ctx, userID)
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

// ValidateUser 校验会话并返回服务端保存的用户 ID，旧测试仓储则回退到兼容管理员身份。
func (auth *Auth) ValidateUser(ctx context.Context, token string) (string, bool, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return "", false, nil
	}
	if identityStore, ok := auth.store.(repository.IdentityStore); ok {
		userID, valid, storeErr := identityStore.GetSessionUser(ctx, hashToken(token))
		return userID, valid, storeErr
	}
	valid, storeErr := auth.store.HasSession(ctx, hashToken(token))
	if !valid || storeErr != nil {
		return "", valid, storeErr
	}
	if auth.userID != "" {
		return auth.userID, true, nil
	}
	return auth.username, true, nil
}

// LabelForContext 返回当前会话的邮箱或兼容账号名，只用于界面展示，不参与权限判断。
func (auth *Auth) LabelForContext(ctx context.Context) string {
	userID, ok := repository.UserIDFromContext(ctx)
	if ok {
		if identityStore, identityOK := auth.store.(repository.IdentityStore); identityOK {
			if user, err := identityStore.GetUserByID(ctx, userID); err == nil {
				if user.Email != "" && !strings.HasSuffix(user.Email, "@local.invalid") {
					return user.Email
				}
				return user.Username
			}
		}
	}
	return auth.username
}

// Logout 从持久层撤销令牌摘要；仅清除浏览器 Cookie 不足以阻止令牌重放。
func (auth *Auth) Logout(ctx context.Context, token string) error {
	// 删除数据库会话而非仅清除浏览器 Cookie，使已泄露的旧 Cookie 也立即失效。
	if err := auth.store.DeleteSession(ctx, hashToken(token)); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}
