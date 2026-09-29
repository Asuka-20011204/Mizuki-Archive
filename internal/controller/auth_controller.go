package controller

import (
	"errors"
	"mime"
	"net/http"

	"github.com/gin-gonic/gin"
	"mizuki-archive/internal/service"
)

// login 限制登录体积和失败次数，成功后只把随机会话令牌放入 HttpOnly Cookie。
func (handler *Controller) login(ctx *gin.Context) {
	// 登录体积和尝试次数都有上限，避免账号入口成为内存耗尽或暴力猜测入口。
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, 8<<10)
	address := clientAddress(ctx.Request.RemoteAddr)
	if !handler.limiter.allowed(address) {
		failure(ctx, http.StatusTooManyRequests, "try_later", "尝试次数过多，请稍后再试")
		return
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
	ctx.SetSameSite(http.SameSiteStrictMode)
	ctx.SetCookie("archive_session", token, int(service.SessionLifetime.Seconds()), "/api", "", handler.config.SecureCookie, true)
	ctx.JSON(http.StatusOK, gin.H{"username": handler.config.Auth.Username()})
}

// requireSession 在每个私有路由前验证 Cookie 的数据库会话，失败时立即终止请求。
func (handler *Controller) requireSession(ctx *gin.Context) {
	token, err := ctx.Cookie("archive_session")
	if err != nil {
		failure(ctx, http.StatusUnauthorized, "unauthorized", "请先登录")
		ctx.Abort()
		return
	}
	valid, err := handler.config.Auth.Validate(ctx.Request.Context(), token)
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
