package controller

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
	"mizuki-archive/internal/service"
)

// batchUpdateFavorites 限定已登录用户的批次、请求体和频率，并返回真实变化项。
func (handler *Controller) batchUpdateFavorites(ctx *gin.Context) {
	owner, _ := repository.UserIDFromContext(ctx.Request.Context())
	key := "batch-favorites:" + owner
	if handler.config.RateLimiter != nil {
		allowed, err := handler.config.RateLimiter.Allow(ctx.Request.Context(), key, 20, time.Minute)
		if err == nil && !allowed {
			failure(ctx, http.StatusTooManyRequests, "rate_limited", "批量操作过于频繁，请稍后再试")
			return
		}
	}
	if !handler.batchLimiter.allowedRequests(key, 20, time.Minute) {
		failure(ctx, http.StatusTooManyRequests, "rate_limited", "批量操作过于频繁，请稍后再试")
		return
	}
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, 8<<10)
	mediaType, _, err := mime.ParseMediaType(ctx.GetHeader("Content-Type"))
	if err != nil || mediaType != "application/json" {
		failure(ctx, http.StatusBadRequest, "invalid_batch", "批量收藏请求格式无效")
		return
	}
	var input struct {
		Items    []model.InboxSelection `json:"items"`
		Favorite *bool                  `json:"favorite"`
	}
	decoder := json.NewDecoder(ctx.Request.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || input.Favorite == nil {
		failure(ctx, http.StatusBadRequest, "invalid_batch", "请选择明确的收藏状态和资料")
		return
	}
	// 消费至 EOF，避免首个合法 JSON 后的超长空白或额外对象绕过请求体上限。
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		failure(ctx, http.StatusBadRequest, "invalid_batch", "批量收藏请求格式无效")
		return
	}
	changed, err := handler.config.BatchFavorites.Apply(ctx.Request.Context(), input.Items, *input.Favorite)
	switch {
	case errors.Is(err, service.ErrInvalidInboxInput):
		failure(ctx, http.StatusBadRequest, "invalid_batch", "请选择至多 50 项不同的资料")
	case errors.Is(err, repository.ErrNotFound):
		failure(ctx, http.StatusNotFound, "not_found", "部分资料不存在或不可访问")
	case err != nil:
		failure(ctx, http.StatusInternalServerError, "internal", "批量收藏操作失败")
	default:
		ctx.JSON(http.StatusOK, gin.H{"count": len(changed), "changed": changed})
	}
}
