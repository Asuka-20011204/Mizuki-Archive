package controller

import (
	"errors"
	"mime"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"mizuki-archive/internal/service"
)

// phoneCodeInput 仅允许提供手机号和一次性验证码，不接受客户端传入用户归属。
type phoneCodeInput struct {
	Phone string `json:"phone"`
	Code  string `json:"code"`
}

// decodePhoneCodeInput 沿用验证码接口的 JSON、Content-Type 和 8KB 体积限制。
func decodePhoneCodeInput(ctx *gin.Context) (phoneCodeInput, bool) {
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, 8<<10)
	contentType, _, err := mime.ParseMediaType(ctx.GetHeader("Content-Type"))
	if err != nil || contentType != "application/json" {
		failure(ctx, http.StatusBadRequest, "invalid_input", "请求格式不正确")
		return phoneCodeInput{}, false
	}
	var input phoneCodeInput
	if err := ctx.ShouldBindJSON(&input); err != nil {
		failure(ctx, http.StatusBadRequest, "invalid_input", "请求格式不正确")
		return phoneCodeInput{}, false
	}
	return input, true
}

// allowPhoneRequest 在发送前检查单 IP 和单号码预算，Redis 故障时拒绝费用型请求。
func (handler *Controller) allowPhoneRequest(ctx *gin.Context, input string) bool {
	address := clientAddress(ctx.Request, handler.config.TrustProxyHeaders)
	phone, err := service.NormalizeMainlandPhone(input)
	if err != nil {
		return true
	}
	phoneKey := handler.config.PhoneAuth.RateLimitKey(phone)
	for _, bucket := range []struct {
		key    string
		limit  int
		window time.Duration
	}{
		{"phone-request-ip:" + address, 5, 15 * time.Minute},
		{"phone-request-number:" + phoneKey, 3, time.Hour},
	} {
		if handler.config.RateLimiter != nil {
			allowed, err := handler.config.RateLimiter.Allow(ctx.Request.Context(), bucket.key, bucket.limit, bucket.window)
			if err != nil {
				failure(ctx, http.StatusServiceUnavailable, "sms_unavailable", "短信服务暂不可用")
				return false
			}
			if !allowed {
				failure(ctx, http.StatusTooManyRequests, "try_later", "验证码请求过于频繁，请稍后再试")
				return false
			}
		}
		if !handler.emailLimiter.allowedRequests(bucket.key, bucket.limit, bucket.window) {
			failure(ctx, http.StatusTooManyRequests, "try_later", "验证码请求过于频繁，请稍后再试")
			return false
		}
	}
	return true
}

// allowPhoneAttempt 在核验前限制单 IP 和单号码尝试次数，阻止分布式猜码。
func (handler *Controller) allowPhoneAttempt(ctx *gin.Context, phone string) bool {
	address := clientAddress(ctx.Request, handler.config.TrustProxyHeaders)
	normalized, err := service.NormalizeMainlandPhone(phone)
	if err != nil {
		normalized = "invalid"
	}
	phoneKey := handler.config.PhoneAuth.RateLimitKey(normalized)
	for _, bucket := range []struct {
		key   string
		limit int
	}{
		{"phone-attempt-ip:" + address, 20},
		{"phone-attempt-number:" + phoneKey, 10},
	} {
		if handler.config.RateLimiter != nil {
			allowed, err := handler.config.RateLimiter.Allow(ctx.Request.Context(), bucket.key, bucket.limit, 15*time.Minute)
			if err != nil {
				failure(ctx, http.StatusServiceUnavailable, "verification_unavailable", "验证服务暂不可用")
				return false
			}
			if !allowed {
				failure(ctx, http.StatusTooManyRequests, "try_later", "验证码尝试过于频繁，请稍后再试")
				return false
			}
		}
		if !handler.emailLimiter.allowedRequests(bucket.key, bucket.limit, 15*time.Minute) {
			failure(ctx, http.StatusTooManyRequests, "try_later", "验证码尝试过于频繁，请稍后再试")
			return false
		}
	}
	return true
}

// requestPhoneRegistrationCode 为新手机号请求验证码，隐藏注册状态和发送失败细节。
func (handler *Controller) requestPhoneRegistrationCode(ctx *gin.Context) {
	input, ok := decodePhoneCodeInput(ctx)
	if !ok || !handler.allowPhoneRequest(ctx, input.Phone) {
		return
	}
	err := handler.config.PhoneAuth.RequestRegistrationCode(ctx.Request.Context(), input.Phone)
	if err != nil && !errors.Is(err, service.ErrPhoneCodeTooSoon) && !errors.Is(err, service.ErrPhoneAlreadyRegistered) && !errors.Is(err, service.ErrPhoneUnavailable) {
		failure(ctx, http.StatusInternalServerError, "internal", "无法处理验证码请求")
		return
	}
	ctx.JSON(http.StatusAccepted, gin.H{"message": "如果手机号可注册且短信服务可用，验证码将发送到手机"})
}

// registerWithPhoneCode 仅使用一次性验证码创建用户和会话，不信任客户端身份字段。
func (handler *Controller) registerWithPhoneCode(ctx *gin.Context) {
	input, ok := decodePhoneCodeInput(ctx)
	if !ok {
		return
	}
	if input.Code == "" {
		failure(ctx, http.StatusBadRequest, "invalid_input", "验证码格式不正确")
		return
	}
	if !handler.allowPhoneAttempt(ctx, input.Phone) {
		return
	}
	token, err := handler.config.PhoneAuth.RegisterWithCode(ctx.Request.Context(), input.Phone, input.Code)
	if errors.Is(err, service.ErrPhoneCodeInvalid) {
		failure(ctx, http.StatusUnauthorized, "invalid_code", "验证码不正确或已过期")
		return
	}
	if errors.Is(err, service.ErrPhoneAlreadyRegistered) {
		failure(ctx, http.StatusConflict, "phone_already_registered", "手机号已经注册")
		return
	}
	if err != nil {
		failure(ctx, http.StatusInternalServerError, "internal", "无法创建账号")
		return
	}
	handler.setSessionCookie(ctx, token)
	phone, _ := service.NormalizeMainlandPhone(input.Phone)
	ctx.JSON(http.StatusOK, gin.H{"username": phone})
}

// requestPhoneLoginCode 为已注册号码请求验证码；未知号码返回相同的受理响应。
func (handler *Controller) requestPhoneLoginCode(ctx *gin.Context) {
	input, ok := decodePhoneCodeInput(ctx)
	if !ok || !handler.allowPhoneRequest(ctx, input.Phone) {
		return
	}
	err := handler.config.PhoneAuth.RequestLoginCode(ctx.Request.Context(), input.Phone)
	if err != nil && !errors.Is(err, service.ErrPhoneCodeTooSoon) && !errors.Is(err, service.ErrPhoneNotRegistered) && !errors.Is(err, service.ErrPhoneUnavailable) {
		failure(ctx, http.StatusInternalServerError, "internal", "无法处理验证码请求")
		return
	}
	ctx.JSON(http.StatusAccepted, gin.H{"message": "如果手机号已注册且短信服务可用，验证码将发送到手机"})
}

// loginWithPhoneCode 使用已注册用户的一次性验证码建立带归属的服务端会话。
func (handler *Controller) loginWithPhoneCode(ctx *gin.Context) {
	input, ok := decodePhoneCodeInput(ctx)
	if !ok {
		return
	}
	if input.Code == "" {
		failure(ctx, http.StatusBadRequest, "invalid_input", "验证码格式不正确")
		return
	}
	if !handler.allowPhoneAttempt(ctx, input.Phone) {
		return
	}
	token, err := handler.config.PhoneAuth.LoginWithCode(ctx.Request.Context(), input.Phone, input.Code)
	if errors.Is(err, service.ErrPhoneCodeInvalid) {
		failure(ctx, http.StatusUnauthorized, "invalid_code", "验证码不正确或已过期")
		return
	}
	if err != nil {
		failure(ctx, http.StatusInternalServerError, "internal", "无法创建会话")
		return
	}
	handler.setSessionCookie(ctx, token)
	phone, _ := service.NormalizeMainlandPhone(input.Phone)
	ctx.JSON(http.StatusOK, gin.H{"username": phone})
}
