package controller

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
	"mizuki-archive/internal/service"
)

// listArchive 只展示已登录用户的归档项，页码和两类来源分别限量。
func (handler *Controller) listArchive(ctx *gin.Context) {
	page, err := strconv.Atoi(ctx.DefaultQuery("page", "1"))
	if err != nil {
		failure(ctx, http.StatusBadRequest, "invalid_archive", "页码无效")
		return
	}
	result, err := handler.config.Archive.List(ctx.Request.Context(), page)
	if errors.Is(err, service.ErrInvalidInboxInput) {
		failure(ctx, http.StatusBadRequest, "invalid_archive", "页码无效")
	} else if err != nil {
		failure(ctx, http.StatusInternalServerError, "internal", "归档列表暂时不可用")
	} else {
		ctx.JSON(http.StatusOK, gin.H{"data": result})
	}
}

// batchSetArchived 校验会话、请求大小与频率；缺失和越权统一返回 404。
func (handler *Controller) batchSetArchived(ctx *gin.Context) {
	owner, _ := repository.UserIDFromContext(ctx.Request.Context())
	key := "batch-archive:" + owner
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
		failure(ctx, http.StatusBadRequest, "invalid_archive", "归档请求格式无效")
		return
	}
	var input struct {
		Items    []model.InboxSelection `json:"items"`
		Archived *bool                  `json:"archived"`
	}
	decoder := json.NewDecoder(ctx.Request.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || input.Archived == nil {
		failure(ctx, http.StatusBadRequest, "invalid_archive", "请选择资料及明确的归档状态")
		return
	}
	// 完整读取到 EOF，避免在合法 JSON 后附加超长空白或其他对象。
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		failure(ctx, http.StatusBadRequest, "invalid_archive", "归档请求格式无效")
		return
	}
	changed, err := handler.config.Archive.Set(ctx.Request.Context(), input.Items, *input.Archived)
	switch {
	case errors.Is(err, service.ErrInvalidInboxInput):
		failure(ctx, http.StatusBadRequest, "invalid_archive", "请选择至多 50 项不同的资料")
	case errors.Is(err, repository.ErrNotFound):
		failure(ctx, http.StatusNotFound, "not_found", "部分资料不存在或不可访问")
	case err != nil:
		failure(ctx, http.StatusInternalServerError, "internal", "归档操作失败")
	default:
		ctx.JSON(http.StatusOK, gin.H{"count": len(changed), "changed": changed})
	}
}
