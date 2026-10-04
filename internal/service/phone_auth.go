package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
)

var (
	ErrPhoneNotRegistered     = errors.New("phone is not registered")
	ErrPhoneAlreadyRegistered = errors.New("phone is already registered")
	ErrPhoneCodeTooSoon       = errors.New("phone verification code requested too soon")
	ErrPhoneCodeInvalid       = errors.New("invalid phone verification code")
	ErrPhoneUnavailable       = errors.New("phone verification is unavailable")
)

// PhoneSender 由真实短信供应商适配器实现；不得用日志或前端响应代替发送。
type PhoneSender interface {
	SendCode(context.Context, string, string) error
}

// PhoneAuth 管理大陆手机号验证码和用户会话，不处理 HTTP 或供应商协议。
type PhoneAuth struct {
	identity  repository.PhoneIdentityStore
	challenge repository.PhoneChallengeStore
	auth      *Auth
	sender    PhoneSender
	secret    []byte
}

// NewPhoneAuth 只有依赖完整且摘要密钥足够长时才允许启用短信认证。
func NewPhoneAuth(identity repository.PhoneIdentityStore, challenge repository.PhoneChallengeStore, auth *Auth, sender PhoneSender, secret []byte) (*PhoneAuth, error) {
	if identity == nil || challenge == nil || auth == nil || sender == nil || len(secret) < 32 {
		return nil, errors.New("invalid phone authentication configuration")
	}
	return &PhoneAuth{identity: identity, challenge: challenge, auth: auth, sender: sender, secret: append([]byte(nil), secret...)}, nil
}

// NormalizeMainlandPhone 接受 11 位大陆手机号码或 +86 前缀，统一存为 +86 加 11 位数字。
func NormalizeMainlandPhone(input string) (string, error) {
	value := input
	if strings.HasPrefix(value, "+86") {
		value = strings.TrimPrefix(value, "+86")
	}
	if len(value) != 11 || value[0] != '1' || value[1] < '3' || value[1] > '9' {
		return "", ErrPhoneUnavailable
	}
	for index := range value {
		if value[index] < '0' || value[index] > '9' {
			return "", ErrPhoneUnavailable
		}
	}
	return "+86" + value, nil
}

// RequestRegistrationCode 对未注册手机号保留挑战并请求短信发送；外层统一响应避免账号枚举。
func (auth *PhoneAuth) RequestRegistrationCode(ctx context.Context, input string) error {
	phone, err := NormalizeMainlandPhone(input)
	if err != nil {
		return ErrPhoneUnavailable
	}
	if _, err := auth.identity.GetUserByPhone(ctx, phone); err == nil {
		return ErrPhoneAlreadyRegistered
	} else if !errors.Is(err, repository.ErrNotFound) {
		return fmt.Errorf("check registered phone: %w", err)
	}
	return auth.issueCode(ctx, phone, EmailPurposeRegister)
}

// RegisterWithCode 先校验账号字段，再消费注册码并创建带密码哈希的独立用户。
func (auth *PhoneAuth) RegisterWithCode(ctx context.Context, input, code, username, password string) (string, error) {
	phone, err := NormalizeMainlandPhone(input)
	if err != nil || !validCode(code) {
		return "", ErrPhoneCodeInvalid
	}
	username, passwordHash, err := HashAccountPassword(username, password)
	if err != nil {
		return "", err
	}
	accepted, err := auth.challenge.ConsumePhoneChallenge(ctx, phone, EmailPurposeRegister, auth.digest(phone, EmailPurposeRegister, code), time.Now().UTC())
	if err != nil {
		return "", fmt.Errorf("consume phone registration code: %w", err)
	}
	if !accepted {
		return "", ErrPhoneCodeInvalid
	}
	user, err := auth.identity.CreatePhoneUser(ctx, phone, username, passwordHash)
	if errors.Is(err, repository.ErrUserExists) {
		return "", ErrPhoneAlreadyRegistered
	}
	if err != nil {
		return "", fmt.Errorf("create phone user: %w", err)
	}
	return auth.auth.CreateSessionForUser(ctx, user.ID)
}

// RequestLoginCode 仅向已注册手机号发送登录验证码，未知号码由 HTTP 层返回相同受理消息。
func (auth *PhoneAuth) RequestLoginCode(ctx context.Context, input string) error {
	phone, err := NormalizeMainlandPhone(input)
	if err != nil {
		return ErrPhoneUnavailable
	}
	if _, err := auth.identity.GetUserByPhone(ctx, phone); errors.Is(err, repository.ErrNotFound) {
		return ErrPhoneNotRegistered
	} else if err != nil {
		return fmt.Errorf("check login phone: %w", err)
	}
	return auth.issueCode(ctx, phone, EmailPurposeLogin)
}

// LoginWithCode 核验一次性验证码，并返回归属会话和持久用户名。
func (auth *PhoneAuth) LoginWithCode(ctx context.Context, input, code string) (string, string, error) {
	phone, err := NormalizeMainlandPhone(input)
	if err != nil || !validCode(code) {
		return "", "", ErrPhoneCodeInvalid
	}
	user, err := auth.identity.GetUserByPhone(ctx, phone)
	if errors.Is(err, repository.ErrNotFound) {
		return "", "", ErrPhoneCodeInvalid
	}
	if err != nil {
		return "", "", fmt.Errorf("get phone user: %w", err)
	}
	accepted, err := auth.challenge.ConsumePhoneChallenge(ctx, phone, EmailPurposeLogin, auth.digest(phone, EmailPurposeLogin, code), time.Now().UTC())
	if err != nil {
		return "", "", fmt.Errorf("consume phone login code: %w", err)
	}
	if !accepted {
		return "", "", ErrPhoneCodeInvalid
	}
	token, err := auth.auth.CreateSessionForUser(ctx, user.ID)
	return token, user.Username, err
}

// issueCode 先原子保留摘要再发送；失败只清除当前挑战，避免并发覆盖新验证码。
func (auth *PhoneAuth) issueCode(ctx context.Context, phone, purpose string) error {
	code, err := generateCode()
	if err != nil {
		return fmt.Errorf("generate phone code: %w", err)
	}
	now := time.Now().UTC()
	digest := auth.digest(phone, purpose, code)
	reserved, err := auth.challenge.ReservePhoneChallenge(ctx, model.PhoneChallenge{Phone: phone, Purpose: purpose, Digest: digest, ExpiresAt: now.Add(verificationCodeLifetime), NextRequestAt: now.Add(verificationResendDelay)}, now)
	if err != nil {
		return fmt.Errorf("reserve phone code: %w", err)
	}
	if !reserved {
		return ErrPhoneCodeTooSoon
	}
	if err := auth.sender.SendCode(ctx, phone, code); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = auth.challenge.DeletePhoneChallenge(cleanup, phone, purpose, digest)
		return ErrPhoneUnavailable
	}
	return nil
}

// digest 将身份类型、手机号、用途和验证码绑定，防止与邮箱挑战互相复用。
func (auth *PhoneAuth) digest(phone, purpose, code string) string {
	digest := hmac.New(sha256.New, auth.secret)
	_, _ = fmt.Fprintf(digest, "phone\x00%s\x00%s\x00%s", phone, purpose, code)
	return fmt.Sprintf("%x", digest.Sum(nil))
}

// RateLimitKey 用服务端密钥派生稳定限流键，避免 Redis 键名中的低熵手机号可被离线枚举。
func (auth *PhoneAuth) RateLimitKey(phone string) string {
	return auth.digest(phone, "rate-limit", "")
}
