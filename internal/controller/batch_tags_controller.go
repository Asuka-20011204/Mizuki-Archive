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

// batchUpdateTags 校验请求大小和当前用户限流，返回真正变化的条目以支持一次撤销。
func (handler *Controller) batchUpdateTags(ctx *gin.Context) {
	owner, _ := repository.UserIDFromContext(ctx.Request.Context())
	key := "batch-tags:" + owner
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
		failure(ctx, http.StatusBadRequest, "invalid_batch", "批量标签请求格式无效")
		return
	}
	var input struct {
		Items []model.InboxSelection `json:"items"`
		Tag   string                 `json:"tag"`
		Mode  string                 `json:"mode"`
	}
	decoder := json.NewDecoder(ctx.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		failure(ctx, http.StatusBadRequest, "invalid_batch", "批量标签请求格式无效")
		return
	}
	// 必须读到 EOF，避免首个合法 JSON 后附带第二个对象或超过上限的空白被忽略。
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		failure(ctx, http.StatusBadRequest, "invalid_batch", "批量标签请求格式无效")
		return
	}
	changed, err := handler.config.BatchTags.Apply(ctx.Request.Context(), input.Items, input.Tag, input.Mode)
	switch {
	case errors.Is(err, service.ErrInvalidInboxInput), errors.Is(err, service.ErrInvalidTag):
		failure(ctx, http.StatusBadRequest, "invalid_batch", "请选择至多 50 项并填写有效标签")
	case errors.Is(err, repository.ErrNotFound):
		failure(ctx, http.StatusNotFound, "not_found", "部分资料不存在或不可访问")
	case errors.Is(err, repository.ErrBatchTagLimit):
		failure(ctx, http.StatusConflict, "tag_limit", "某项资料已达到 10 个标签上限")
	case err != nil:
		failure(ctx, http.StatusInternalServerError, "internal", "批量标签操作失败")
	default:
		ctx.JSON(http.StatusOK, gin.H{"count": len(changed), "changed": changed})
	}
}
