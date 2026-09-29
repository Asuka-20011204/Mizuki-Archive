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
	engine.GET("/healthz", func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) })
	engine.POST("/api/login", handler.login)
	private := engine.Group("/api", handler.requireSession)
	private.GET("/me", func(ctx *gin.Context) { ctx.JSON(http.StatusOK, gin.H{"username": config.Auth.Username()}) })
	private.POST("/logout", handler.logout)
	private.POST("/resources", handler.upload)
	private.GET("/resources", handler.list)
	private.GET("/resources/:id", handler.get)
	private.GET("/resources/:id/download", handler.download)
	return engine, nil
}

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

func failure(ctx *gin.Context, status int, code, message string) {
	ctx.JSON(status, gin.H{"error": gin.H{"code": code, "message": message}})
}
