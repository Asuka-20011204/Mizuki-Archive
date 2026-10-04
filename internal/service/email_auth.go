package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
)

const (
	// EmailPurposeRegister 表示新用户注册验证码。
	EmailPurposeRegister = "register"
	// EmailPurposeLogin 表示已有用户登录验证码。
	EmailPurposeLogin        = "login"
	verificationCodeLifetime = 10 * time.Minute
	verificationResendDelay  = time.Minute
)

var (
	ErrEmailNotRegistered   = errors.New("email is not registered")
	ErrEmailAlreadyRegister = errors.New("email is already registered")
	ErrEmailCodeTooSoon     = errors.New("verification code requested too soon")
	ErrEmailCodeInvalid     = errors.New("invalid verification code")
	ErrEmailUnavailable     = errors.New("email verification is unavailable")
)

// EmailSender 是验证码发送端口，测试可用内存发送器替换真实 SMTP。
type EmailSender interface {
	SendCode(context.Context, string, string) error
}

// EmailAuth 管理邮箱验证码注册与登录；注册还需创建用户名和密码哈希，会话始终绑定用户 ID。
type EmailAuth struct {
	identity  repository.IdentityStore
	challenge repository.EmailChallengeStore
	auth      *Auth
	sender    EmailSender
	secret    []byte
}

// NewEmailAuth 检查验证码服务依赖；密钥不足时拒绝启动，避免可预测验证码摘要。
func NewEmailAuth(identity repository.IdentityStore, challenge repository.EmailChallengeStore, auth *Auth, sender EmailSender, secret []byte) (*EmailAuth, error) {
	if identity == nil || challenge == nil || auth == nil || sender == nil || len(secret) < 32 {
		return nil, errors.New("invalid email authentication configuration")
	}
	return &EmailAuth{identity: identity, challenge: challenge, auth: auth, sender: sender, secret: append([]byte(nil), secret...)}, nil
}

// NormalizeEmail 统一邮箱大小写并拒绝显示名、换行和超长输入，保证查询和限频键一致。
func NormalizeEmail(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) == 0 || len(value) > 254 || strings.ContainsAny(value, "\r\n") {
		return "", ErrEmailUnavailable
	}
	address, err := mail.ParseAddress(value)
	if err != nil || address.Name != "" || address.Address != value {
		return "", ErrEmailUnavailable
	}
	return value, nil
}

// RequestRegistrationCode 为未注册邮箱发送注册验证码；已注册邮箱仍由 Controller 统一响应以避免枚举。
func (auth *EmailAuth) RequestRegistrationCode(ctx context.Context, email string) error {
	normalized, err := NormalizeEmail(email)
	if err != nil {
		return ErrEmailUnavailable
	}
	if _, err := auth.identity.GetUserByEmail(ctx, normalized); err == nil {
		return ErrEmailAlreadyRegister
	} else if !errors.Is(err, repository.ErrNotFound) {
		return fmt.Errorf("check registered email: %w", err)
	}
	return auth.issueCode(ctx, normalized, EmailPurposeRegister)
}

// RegisterWithCode 先校验账号字段，再消费注册码并创建带密码哈希的独立用户。
func (auth *EmailAuth) RegisterWithCode(ctx context.Context, email, code, username, password string) (string, error) {
	normalized, err := NormalizeEmail(email)
	if err != nil || !validCode(code) {
		return "", ErrEmailCodeInvalid
	}
	username, passwordHash, err := HashAccountPassword(username, password)
	if err != nil {
		return "", err
	}
	digest := auth.digest(normalized, EmailPurposeRegister, code)
	accepted, err := auth.challenge.ConsumeEmailChallenge(ctx, normalized, EmailPurposeRegister, digest, time.Now().UTC())
	if err != nil {
		return "", fmt.Errorf("consume registration code: %w", err)
	}
	if !accepted {
		return "", ErrEmailCodeInvalid
	}
	user, err := auth.identity.CreateUser(ctx, normalized, username, passwordHash)
	if err != nil {
		if errors.Is(err, repository.ErrUserExists) {
			return "", ErrEmailAlreadyRegister
		}
		return "", fmt.Errorf("create user after verification: %w", err)
	}
	return auth.auth.CreateSessionForUser(ctx, user.ID)
}

// RequestLoginCode 为已注册邮箱发送登录验证码；未知邮箱由 Controller 统一返回受理响应。
func (auth *EmailAuth) RequestLoginCode(ctx context.Context, email string) error {
	normalized, err := NormalizeEmail(email)
	if err != nil {
		return ErrEmailUnavailable
	}
	if _, err := auth.identity.GetUserByEmail(ctx, normalized); errors.Is(err, repository.ErrNotFound) {
		return ErrEmailNotRegistered
	} else if err != nil {
		return fmt.Errorf("check login email: %w", err)
	}
	return auth.issueCode(ctx, normalized, EmailPurposeLogin)
}

// LoginWithCode 校验已有用户的一次性验证码，返回绑定该用户的会话和展示用户名。
func (auth *EmailAuth) LoginWithCode(ctx context.Context, email, code string) (string, string, error) {
	normalized, err := NormalizeEmail(email)
	if err != nil || !validCode(code) {
		return "", "", ErrEmailCodeInvalid
	}
	user, err := auth.identity.GetUserByEmail(ctx, normalized)
	if errors.Is(err, repository.ErrNotFound) {
		return "", "", ErrEmailCodeInvalid
	}
	if err != nil {
		return "", "", fmt.Errorf("get login user: %w", err)
	}
	accepted, err := auth.challenge.ConsumeEmailChallenge(ctx, normalized, EmailPurposeLogin, auth.digest(normalized, EmailPurposeLogin, code), time.Now().UTC())
	if err != nil {
		return "", "", fmt.Errorf("consume login code: %w", err)
	}
	if !accepted {
		return "", "", ErrEmailCodeInvalid
	}
	token, err := auth.auth.CreateSessionForUser(ctx, user.ID)
	return token, user.Username, err
}

// issueCode 先持久化摘要再发送；发送失败时只删除仍属于本次请求的摘要，避免误删新验证码。
func (auth *EmailAuth) issueCode(ctx context.Context, email, purpose string) error {
	code, err := generateCode()
	if err != nil {
		return fmt.Errorf("generate email code: %w", err)
	}
	now := time.Now().UTC()
	digest := auth.digest(email, purpose, code)
	reserved, err := auth.challenge.ReserveEmailChallenge(ctx, model.EmailChallenge{Email: email, Purpose: purpose, Digest: digest, ExpiresAt: now.Add(verificationCodeLifetime), NextRequestAt: now.Add(verificationResendDelay)}, now)
	if err != nil {
		return fmt.Errorf("reserve email code: %w", err)
	}
	if !reserved {
		return ErrEmailCodeTooSoon
	}
	if err := auth.sender.SendCode(ctx, email, code); err != nil {
		_ = auth.challenge.DeleteEmailChallenge(context.Background(), email, purpose, digest)
		return ErrEmailUnavailable
	}
	return nil
}

// generateCode 使用系统安全随机源生成固定六位数字，永不使用伪随机或时间作为验证码。
func generateCode() (string, error) {
	bytes := make([]byte, 6)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	for index := range bytes {
		bytes[index] = '0' + bytes[index]%10
	}
	return string(bytes), nil
}

// digest 使用服务端密钥绑定邮箱、用途和验证码；数据库泄露时不能直接获得明文验证码。
func (auth *EmailAuth) digest(email, purpose, code string) string {
	digest := hmac.New(sha256.New, auth.secret)
	_, _ = fmt.Fprintf(digest, "%s\x00%s\x00%s", email, purpose, code)
	return fmt.Sprintf("%x", digest.Sum(nil))
}

// validCode 限制验证码为六位 ASCII 数字，避免 Unicode 数字和控制字符绕过校验。
func validCode(code string) bool {
	if len(code) != 6 {
		return false
	}
	for _, digit := range code {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}
