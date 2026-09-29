// Package controller 负责 HTTP 边界：路由、请求校验、会话中间件和响应格式。
// 业务规则放在 service，数据库操作放在 repository。
package controller

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"mizuki-archive/internal/cache"
	"mizuki-archive/internal/service"
)

// Config 由 cmd/server 组装；Controller 不自行创建数据库或文件服务。
type Config struct {
	Resources    *service.Resources
	Processing   *service.Processing
	Auth         *service.Auth
	EmailAuth    *service.EmailAuth
	Origin       string
	SecureCookie bool
	// TrustProxyHeaders 仅应在 API 不直接暴露、且前置代理会覆盖 X-Real-IP 时开启。
	TrustProxyHeaders bool
	RateLimiter       cache.RateLimiter
	Ready             func(context.Context) error
}

type Controller struct {
	config       Config
	limiter      *loginLimiter
	emailLimiter *requestLimiter
}

// New 注册公开路由和需要会话的私有路由，是 HTTP 层的入口。
func New(config Config) (*gin.Engine, error) {
	if config.Resources == nil || config.Auth == nil || config.Origin == "" {
		return nil, errors.New("missing HTTP configuration")
	}
	engine := gin.New()
	if err := engine.SetTrustedProxies(nil); err != nil {
		return nil, err
	}
	engine.Use(gin.Recovery())
	handler := &Controller{config: config, limiter: newLoginLimiter(), emailLimiter: newRequestLimiter()}
	engine.Use(handler.headersAndOrigin)
	// 健康检查回调只报告进程存活，不查询数据库，也不返回私有资料或配置细节。
	engine.GET("/healthz", func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) })
	// 就绪探针仅检查关键数据库依赖，不返回数据库错误或私有配置。
	engine.GET("/readyz", func(ctx *gin.Context) {
		if config.Ready == nil {
			ctx.Status(http.StatusServiceUnavailable)
			return
		}
		checkCtx, cancel := context.WithTimeout(ctx.Request.Context(), 2*time.Second)
		defer cancel()
		if err := config.Ready(checkCtx); err != nil {
			ctx.Status(http.StatusServiceUnavailable)
			return
		}
		ctx.Status(http.StatusNoContent)
	})
	// 前端先读取能力而不是猜测环境配置；未配置 SMTP 时不会展示不可用的邮箱入口。
	engine.GET("/api/auth/capabilities", func(ctx *gin.Context) {
		ctx.JSON(http.StatusOK, gin.H{"email_verification": config.EmailAuth != nil})
	})
	engine.POST("/api/login", handler.login)
	if config.EmailAuth != nil {
		// 邮箱路由只有在 SMTP 和验证码密钥完整配置时出现，避免展示半成品注册入口。
		engine.POST("/api/email/register/request", handler.requestEmailRegistrationCode)
		engine.POST("/api/email/register", handler.registerWithEmailCode)
		engine.POST("/api/email/login/request", handler.requestEmailLoginCode)
		engine.POST("/api/email/login", handler.loginWithEmailCode)
	}
	private := engine.Group("/api", handler.requireSession)
	// 身份回调只在会话中间件通过后返回服务端用户名，不信任浏览器提交的身份。
	private.GET("/me", func(ctx *gin.Context) {
		ctx.JSON(http.StatusOK, gin.H{"username": config.Auth.LabelForContext(ctx.Request.Context())})
	})
	private.POST("/logout", handler.logout)
	private.POST("/resources", handler.upload)
	private.GET("/resources", handler.list)
	private.GET("/resources/recent", handler.recent)
	private.GET("/tags", handler.listTags)
	private.GET("/resources/:id", handler.get)
	private.PATCH("/resources/:id/name", handler.setName)
	private.PATCH("/resources/:id/favorite", handler.setFavorite)
	private.PUT("/resources/:id/tags", handler.setTags)
	private.DELETE("/resources/:id", handler.delete)
	private.GET("/resources/:id/preview", handler.preview)
	private.GET("/resources/:id/download", handler.download)
	if config.Processing != nil {
		private.POST("/resources/:id/jobs", handler.createProcessingJob)
		private.GET("/resources/:id/jobs", handler.listProcessingJobs)
		private.GET("/jobs/:id", handler.getProcessingJob)
		private.GET("/derived-assets/:id/download", handler.downloadDerived)
		private.GET("/derived-assets/:id/preview", handler.previewDerived)
	}
	return engine, nil
}

// headersAndOrigin 设置私有响应的安全头，并对写请求执行同源校验以降低 CSRF 风险。
func (handler *Controller) headersAndOrigin(ctx *gin.Context) {
	ctx.Header("X-Content-Type-Options", "nosniff")
	ctx.Header("Referrer-Policy", "no-referrer")
	ctx.Header("Cache-Control", "no-store")
	// Cookie 会随请求自动携带，写操作必须额外校验浏览器来源以防跨站请求。
	if ctx.Request.Method != http.MethodGet && ctx.Request.Method != http.MethodHead && ctx.GetHeader("Origin") != handler.config.Origin {
		failure(ctx, http.StatusForbidden, "origin_rejected", "请求来源未获授权")
		ctx.Abort()
		return
	}
	if handler.config.RateLimiter != nil && ctx.Request.Method == http.MethodPost && ctx.Request.URL.Path != "/api/login" {
		allowed, err := handler.config.RateLimiter.Allow(ctx.Request.Context(), clientAddress(ctx.Request, handler.config.TrustProxyHeaders)+":"+ctx.Request.URL.Path, 60, time.Minute)
		if err == nil && !allowed {
			failure(ctx, http.StatusTooManyRequests, "rate_limited", "请求过于频繁，请稍后再试")
			ctx.Abort()
			return
		}
	}
	ctx.Next()
}

// failure 统一输出不含内部错误细节的 HTTP 错误结构，供前端稳定展示。
func failure(ctx *gin.Context, status int, code, message string) {
	ctx.JSON(status, gin.H{"error": gin.H{"code": code, "message": message}})
}
