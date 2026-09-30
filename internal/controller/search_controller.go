package controller

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
	"mizuki-archive/internal/service"
)

// searchAll 对两类私有资料使用一个关键词入口，保留来源分组及各自的下一页标志。
func (handler *Controller) searchAll(ctx *gin.Context) {
	page := 1
	if raw := ctx.Query("page"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 1000 {
			failure(ctx, http.StatusBadRequest, "invalid_search", "搜索页码无效")
			return
		}
		page = parsed
	}
	// 搜索可能扫描已提取正文：优先使用共享限流器，Redis 故障时仍保留单实例限流。
	owner, _ := repository.UserIDFromContext(ctx.Request.Context())
	key := "search:" + owner
	if handler.config.RateLimiter != nil {
		allowed, err := handler.config.RateLimiter.Allow(ctx.Request.Context(), key, 30, time.Minute)
		if err == nil && !allowed {
			failure(ctx, http.StatusTooManyRequests, "rate_limited", "搜索过于频繁，请稍后再试")
			return
		}
	}
	if !handler.searchLimiter.allowedRequests(key, 30, time.Minute) {
		failure(ctx, http.StatusTooManyRequests, "rate_limited", "搜索过于频繁，请稍后再试")
		return
	}
	searchContext, cancel := context.WithTimeout(ctx.Request.Context(), 5*time.Second)
	defer cancel()
	filter := model.SearchFilter{Query: ctx.Query("q"), Source: ctx.Query("source"), Kind: ctx.Query("kind"), Tag: ctx.Query("tag"), OrganizationStatus: ctx.Query("organization_status")}
	results, err := handler.config.Search.QueryFiltered(searchContext, filter, page)
	if errors.Is(err, service.ErrInvalidSearch) {
		failure(ctx, http.StatusBadRequest, "invalid_search", "请输入关键词或有效筛选条件")
		return
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			failure(ctx, http.StatusServiceUnavailable, "search_timeout", "搜索耗时过长，请缩小范围后重试")
			return
		}
		failure(ctx, http.StatusInternalServerError, "internal", "无法搜索资料")
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": results})
}
