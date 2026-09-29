package controller

import (
	"errors"
	"mime"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"mizuki-archive/internal/repository"
	"mizuki-archive/internal/service"
)

// emailCodeInput 是邮箱验证码接口共用的最小请求结构，不接受账号、角色或资料 ID。
type emailCodeInput struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

// login 限制登录体积和失败次数，成功后只把随机会话令牌放入 HttpOnly Cookie。
func (handler *Controller) login(ctx *gin.Context) {
	// 登录体积和尝试次数都有上限，避免账号入口成为内存耗尽或暴力猜测入口。
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, 8<<10)
	address := clientAddress(ctx.Request.RemoteAddr)
	if !handler.limiter.allowed(address) {
		failure(ctx, http.StatusTooManyRequests, "try_later", "尝试次数过多，请稍后再试")
		return
	}
	// 跨进程限制登录请求总量；Redis 故障时保留单进程失败次数限制，不阻断管理员登录。
	if handler.config.RateLimiter != nil {
		allowed, err := handler.config.RateLimiter.Allow(ctx.Request.Context(), "login:"+address, 30, 15*time.Minute)
		if err == nil && !allowed {
			failure(ctx, http.StatusTooManyRequests, "try_later", "尝试次数过多，请稍后再试")
			return
		}
	}
	contentType, _, err := mime.ParseMediaType(ctx.GetHeader("Content-Type"))
	if err != nil || contentType != "application/json" {
		failure(ctx, http.StatusBadRequest, "invalid_input", "登录信息格式不正确")
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := ctx.ShouldBindJSON(&input); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			failure(ctx, http.StatusRequestEntityTooLarge, "login_too_large", "登录请求过大")
			return
		}
		failure(ctx, http.StatusBadRequest, "invalid_input", "登录信息格式不正确")
		return
	}
	token, err := handler.config.Auth.Login(ctx.Request.Context(), input.Username, input.Password)
	if errors.Is(err, service.ErrInvalidCredentials) {
		handler.limiter.failed(address)
		failure(ctx, http.StatusUnauthorized, "invalid_credentials", "账号或密码不正确")
		return
	}
	if err != nil {
		failure(ctx, http.StatusInternalServerError, "internal", "无法创建会话")
		return
	}
	handler.limiter.succeeded(address)
	// 浏览器只保存随机会话令牌；密码哈希和会话验证留在服务端。
	handler.setSessionCookie(ctx, token)
	ctx.JSON(http.StatusOK, gin.H{"username": handler.config.Auth.Username()})
}

// requestEmailRegistrationCode 为新用户发送注册验证码；未知或已注册邮箱使用统一响应避免枚举。
func (handler *Controller) requestEmailRegistrationCode(ctx *gin.Context) {
	if !handler.allowEmailCodeRequest(ctx, "register") {
		return
	}
	input, ok := decodeEmailCodeInput(ctx)
	if !ok {
		return
	}
	err := handler.config.EmailAuth.RequestRegistrationCode(ctx.Request.Context(), input.Email)
	if errors.Is(err, service.ErrEmailCodeTooSoon) {
		failure(ctx, http.StatusTooManyRequests, "try_later", "验证码发送过于频繁，请稍后再试")
		return
	}
	if err != nil && !errors.Is(err, service.ErrEmailAlreadyRegister) && !errors.Is(err, service.ErrEmailUnavailable) {
		failure(ctx, http.StatusInternalServerError, "internal", "无法处理验证码请求")
		return
	}
	ctx.JSON(http.StatusAccepted, gin.H{"message": "如果邮箱可注册且发送服务可用，验证码将发送到邮箱"})
}

// registerWithEmailCode 消费注册验证码，创建独立用户并立即登录。
func (handler *Controller) registerWithEmailCode(ctx *gin.Context) {
	input, ok := decodeEmailCodeInput(ctx)
	if !ok || input.Code == "" {
		if ok {
			failure(ctx, http.StatusBadRequest, "invalid_input", "验证码格式不正确")
		}
		return
	}
	if !handler.allowEmailCodeAttempt(ctx, "register", input.Email) {
		return
	}
	token, err := handler.config.EmailAuth.RegisterWithCode(ctx.Request.Context(), input.Email, input.Code)
	if errors.Is(err, service.ErrEmailCodeInvalid) {
		failure(ctx, http.StatusUnauthorized, "invalid_code", "验证码不正确或已过期")
		return
	}
	if errors.Is(err, service.ErrEmailAlreadyRegister) {
		failure(ctx, http.StatusConflict, "email_already_registered", "邮箱已经注册")
		return
	}
	if err != nil {
		failure(ctx, http.StatusInternalServerError, "internal", "无法创建账号")
		return
	}
	handler.setSessionCookie(ctx, token)
	ctx.JSON(http.StatusOK, gin.H{"username": input.Email})
}

// requestEmailLoginCode 请求已绑定 owner 的邮箱验证码；未知邮箱统一返回已受理，减少账号枚举。
func (handler *Controller) requestEmailLoginCode(ctx *gin.Context) {
	if !handler.allowEmailCodeRequest(ctx, "login") {
		return
	}
	input, ok := decodeEmailCodeInput(ctx)
	if !ok {
		return
	}
	err := handler.config.EmailAuth.RequestLoginCode(ctx.Request.Context(), input.Email)
	if errors.Is(err, service.ErrEmailCodeTooSoon) {
		failure(ctx, http.StatusTooManyRequests, "try_later", "验证码发送过于频繁，请稍后再试")
		return
	}
	if err != nil && !errors.Is(err, service.ErrEmailNotRegistered) && !errors.Is(err, service.ErrEmailUnavailable) {
		failure(ctx, http.StatusInternalServerError, "internal", "无法处理验证码请求")
		return
	}
	// 对未绑定邮箱也返回同一状态和消息，避免通过响应判断 owner 邮箱是否存在。
	ctx.JSON(http.StatusAccepted, gin.H{"message": "如果邮箱已绑定且发送服务可用，验证码将发送到邮箱"})
}

// allowEmailCodeRequest 在解析请求体前限制单 IP 的验证码发送频率，防止 SMTP 成本被恶意消耗。
func (handler *Controller) allowEmailCodeRequest(ctx *gin.Context, purpose string) bool {
	address := clientAddress(ctx.Request.RemoteAddr)
	key := purpose + ":" + address
	if handler.config.RateLimiter != nil {
		allowed, err := handler.config.RateLimiter.Allow(ctx.Request.Context(), "email-code-request:"+key, 5, 15*time.Minute)
		if err == nil && !allowed {
			failure(ctx, http.StatusTooManyRequests, "try_later", "验证码请求过于频繁，请稍后再试")
			return false
		}
	}
	if handler.emailLimiter.allowedRequests(key, 5, 15*time.Minute) {
		return true
	}
	failure(ctx, http.StatusTooManyRequests, "try_later", "验证码请求过于频繁，请稍后再试")
	return false
}

// loginWithEmailCode 用已绑定 owner 的一次性验证码建立同一套数据库会话。
func (handler *Controller) loginWithEmailCode(ctx *gin.Context) {
	input, ok := decodeEmailCodeInput(ctx)
	if !ok || input.Code == "" {
		if ok {
			failure(ctx, http.StatusBadRequest, "invalid_input", "验证码格式不正确")
		}
		return
	}
	if !handler.allowEmailCodeAttempt(ctx, "login", input.Email) {
		return
	}
	token, err := handler.config.EmailAuth.LoginWithCode(ctx.Request.Context(), input.Email, input.Code)
	if errors.Is(err, service.ErrEmailCodeInvalid) {
		failure(ctx, http.StatusUnauthorized, "invalid_code", "验证码不正确或已过期")
		return
	}
	if err != nil {
		failure(ctx, http.StatusInternalServerError, "internal", "无法创建会话")
		return
	}
	handler.setSessionCookie(ctx, token)
	ctx.JSON(http.StatusOK, gin.H{"username": handler.config.Auth.Username()})
}

// allowEmailCodeAttempt 限制验证码核验次数，覆盖多实例 Redis 和单进程降级两条路径。
func (handler *Controller) allowEmailCodeAttempt(ctx *gin.Context, purpose, email string) bool {
	address := clientAddress(ctx.Request.RemoteAddr)
	if !handler.allowEmailCodeBucket(ctx, "attempt-ip:"+purpose+":"+address, 20) {
		failure(ctx, http.StatusTooManyRequests, "try_later", "验证码尝试过于频繁，请稍后再试")
		return false
	}
	if handler.allowEmailCodeBucket(ctx, "attempt:"+purpose+":"+address+":"+email, 10) {
		return true
	}
	failure(ctx, http.StatusTooManyRequests, "try_later", "验证码尝试过于频繁，请稍后再试")
	return false
}

// allowEmailCodeBucket 统一执行共享 Redis 与单进程本地两级验证码限流，Redis 故障时不中断合法用户。
func (handler *Controller) allowEmailCodeBucket(ctx *gin.Context, key string, limit int) bool {
	if handler.config.RateLimiter != nil {
		allowed, err := handler.config.RateLimiter.Allow(ctx.Request.Context(), "email-code:"+key, limit, 15*time.Minute)
		if err == nil && !allowed {
			return false
		}
	}
	return handler.emailLimiter.allowedRequests(key, limit, 15*time.Minute)
}

// decodeEmailCodeInput 限制邮箱接口请求体大小和 JSON 类型，避免重复实现边界校验。
func decodeEmailCodeInput(ctx *gin.Context) (emailCodeInput, bool) {
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, 8<<10)
	contentType, _, err := mime.ParseMediaType(ctx.GetHeader("Content-Type"))
	if err != nil || contentType != "application/json" {
		failure(ctx, http.StatusBadRequest, "invalid_input", "请求格式不正确")
		return emailCodeInput{}, false
	}
	var input emailCodeInput
	if err := ctx.ShouldBindJSON(&input); err != nil {
		failure(ctx, http.StatusBadRequest, "invalid_input", "请求格式不正确")
		return emailCodeInput{}, false
	}
	return input, true
}

// setSessionCookie 统一配置密码和邮箱登录的 HttpOnly、SameSite 会话 Cookie。
func (handler *Controller) setSessionCookie(ctx *gin.Context, token string) {
	ctx.SetSameSite(http.SameSiteStrictMode)
	ctx.SetCookie("archive_session", token, int(service.SessionLifetime.Seconds()), "/api", "", handler.config.SecureCookie, true)
}

// requireSession 在每个私有路由前验证 Cookie 的数据库会话，失败时立即终止请求。
func (handler *Controller) requireSession(ctx *gin.Context) {
	token, err := ctx.Cookie("archive_session")
	if err != nil {
		failure(ctx, http.StatusUnauthorized, "unauthorized", "请先登录")
		ctx.Abort()
		return
	}
	userID, valid, err := handler.config.Auth.ValidateUser(ctx.Request.Context(), token)
	if err != nil {
		failure(ctx, http.StatusInternalServerError, "internal", "无法验证会话")
		ctx.Abort()
		return
	}
	if !valid {
		failure(ctx, http.StatusUnauthorized, "unauthorized", "会话已失效")
		ctx.Abort()
		return
	}
	// 用户 ID 来自数据库会话而不是请求参数；后续 Repository 查询统一从该上下文做资料隔离。
	ctx.Request = ctx.Request.WithContext(repository.WithUserID(ctx.Request.Context(), userID))
	ctx.Next()
}

// logout 先撤销数据库中的会话再清理 Cookie，确保退出后旧令牌不可用。
func (handler *Controller) logout(ctx *gin.Context) {
	token, _ := ctx.Cookie("archive_session")
	if err := handler.config.Auth.Logout(ctx.Request.Context(), token); err != nil {
		failure(ctx, http.StatusInternalServerError, "internal", "退出失败")
		return
	}
	ctx.SetSameSite(http.SameSiteStrictMode)
	ctx.SetCookie("archive_session", "", -1, "/api", "", handler.config.SecureCookie, true)
	ctx.Status(http.StatusNoContent)
}
