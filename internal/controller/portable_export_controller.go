package controller

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"mizuki-archive/internal/repository"
	"mizuki-archive/internal/service"
)

// downloadPortableExport 以当前会话导出元数据；先完整生成再写响应，失败不返回残缺文件。
func (handler *Controller) downloadPortableExport(ctx *gin.Context) {
	owner, _ := repository.UserIDFromContext(ctx.Request.Context())
	key := "export:" + owner
	if handler.config.RateLimiter != nil {
		allowed, err := handler.config.RateLimiter.Allow(ctx.Request.Context(), key, 2, time.Minute)
		if err == nil && !allowed {
			failure(ctx, http.StatusTooManyRequests, "rate_limited", "导出过于频繁，请稍后再试")
			return
		}
	}
	if !handler.exportLimiter.allowedRequests(key, 2, time.Minute) {
		failure(ctx, http.StatusTooManyRequests, "rate_limited", "导出过于频繁，请稍后再试")
		return
	}
	select {
	case handler.exportSlots <- struct{}{}:
		defer func() { <-handler.exportSlots }()
	default:
		failure(ctx, http.StatusServiceUnavailable, "export_busy", "正在处理其他导出，请稍后重试")
		return
	}
	exportContext, cancel := context.WithTimeout(ctx.Request.Context(), 30*time.Second)
	defer cancel()
	content, err := handler.config.Export.Create(exportContext)
	switch {
	case errors.Is(err, service.ErrPortableExportLimit):
		failure(ctx, http.StatusRequestEntityTooLarge, "export_limit", "资料清单超过本次导出上限，请使用备份方案")
		return
	case errors.Is(err, context.DeadlineExceeded):
		failure(ctx, http.StatusServiceUnavailable, "export_timeout", "导出超时，请稍后重试")
		return
	case err != nil:
		failure(ctx, http.StatusInternalServerError, "internal", "无法导出资料清单")
		return
	}
	ctx.Header("Content-Disposition", "attachment; filename=\"mizuki-archive-"+time.Now().UTC().Format("2006-01-02")+".json\"")
	ctx.Data(http.StatusOK, "application/json; charset=utf-8", content)
}
