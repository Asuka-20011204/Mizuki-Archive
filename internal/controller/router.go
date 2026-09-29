// Package controller 负责 HTTP 边界：路由、请求校验、会话中间件和响应格式。
// 业务规则放在 service，数据库操作放在 repository。
package controller

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"mizuki-archive/internal/service"
)

// Config 由 cmd/server 组装；Controller 不自行创建数据库或文件服务。
type Config struct {
	Resources    *service.Resources
	Processing   *service.Processing
	Auth         *service.Auth
	Origin       string
	SecureCookie bool
}

type Controller struct {
	config  Config
	limiter *loginLimiter
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
	handler := &Controller{config: config, limiter: newLoginLimiter()}
	engine.Use(handler.headersAndOrigin)
	// 健康检查回调只报告进程存活，不查询数据库，也不返回私有资料或配置细节。
	engine.GET("/healthz", func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) })
	engine.POST("/api/login", handler.login)
	private := engine.Group("/api", handler.requireSession)
	// 身份回调只在会话中间件通过后返回服务端用户名，不信任浏览器提交的身份。
	private.GET("/me", func(ctx *gin.Context) { ctx.JSON(http.StatusOK, gin.H{"username": config.Auth.Username()}) })
	private.POST("/logout", handler.logout)
	private.POST("/resources", handler.upload)
	private.GET("/resources", handler.list)
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
	ctx.Next()
}

// failure 统一输出不含内部错误细节的 HTTP 错误结构，供前端稳定展示。
func failure(ctx *gin.Context, status int, code, message string) {
	ctx.JSON(status, gin.H{"error": gin.H{"code": code, "message": message}})
}
