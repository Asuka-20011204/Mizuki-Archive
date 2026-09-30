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

// batchDelete 对不可撤销的混合来源删除做显式确认、体积限制与逐账号限流。
func (handler *Controller) batchDelete(ctx *gin.Context) {
	owner, _ := repository.UserIDFromContext(ctx.Request.Context())
	key := "batch-delete:" + owner
	if handler.config.RateLimiter != nil {
		allowed, err := handler.config.RateLimiter.Allow(ctx.Request.Context(), key, 10, time.Minute)
		if err == nil && !allowed {
			failure(ctx, http.StatusTooManyRequests, "rate_limited", "批量删除过于频繁，请稍后再试")
			return
		}
	}
	if !handler.batchLimiter.allowedRequests(key, 10, time.Minute) {
		failure(ctx, http.StatusTooManyRequests, "rate_limited", "批量删除过于频繁，请稍后再试")
		return
	}
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, 8<<10)
	mediaType, _, err := mime.ParseMediaType(ctx.GetHeader("Content-Type"))
	if err != nil || mediaType != "application/json" {
		failure(ctx, http.StatusBadRequest, "invalid_batch_delete", "删除请求格式无效")
		return
	}
	var input struct {
		Items   []model.InboxSelection `json:"items"`
		Confirm string                 `json:"confirm"`
	}
	decoder := json.NewDecoder(ctx.Request.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || input.Confirm != "DELETE" {
		failure(ctx, http.StatusBadRequest, "invalid_batch_delete", "请选择资料并确认不可撤销的删除")
		return
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		failure(ctx, http.StatusBadRequest, "invalid_batch_delete", "删除请求格式无效")
		return
	}
	result, err := handler.config.BatchDelete.Apply(ctx.Request.Context(), input.Items)
	switch {
	case errors.Is(err, service.ErrInvalidInboxInput):
		failure(ctx, http.StatusBadRequest, "invalid_batch_delete", "请选择至多 50 项不同的资料")
	case errors.Is(err, repository.ErrNotFound):
		failure(ctx, http.StatusNotFound, "not_found", "部分资料不存在或不可访问")
	case err != nil:
		failure(ctx, http.StatusInternalServerError, "internal", "批量删除失败，未删除任何资料")
	default:
		ctx.JSON(http.StatusOK, result)
	}
}
