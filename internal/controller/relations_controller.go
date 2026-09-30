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

// relationFailure 将越权与不存在统一映射为 404，避免通过关联接口枚举他人资料。
func relationFailure(ctx *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidRelation):
		failure(ctx, http.StatusBadRequest, "invalid_relation", "请选择不同的有效资料")
	case errors.Is(err, repository.ErrNotFound):
		failure(ctx, http.StatusNotFound, "not_found", "资料或关联不存在")
	case errors.Is(err, repository.ErrRelationExists):
		failure(ctx, http.StatusConflict, "relation_exists", "两项资料已经关联")
	case errors.Is(err, repository.ErrRelationLimit):
		failure(ctx, http.StatusConflict, "relation_limit", "每项资料最多关联 100 条")
	default:
		failure(ctx, http.StatusInternalServerError, "internal", "关联操作失败")
	}
}

// listRelations 只读取当前用户资料的关联列表，仓储按用户和两端权限再次校验。
func (handler *Controller) listRelations(ctx *gin.Context) {
	entries, err := handler.config.Relations.List(ctx.Request.Context(), model.InboxSelection{Source: ctx.Param("source"), ID: ctx.Param("id")})
	if err != nil {
		relationFailure(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": entries})
}

// createRelation 限制输入与写入频率，拒绝未知字段和多段 JSON，用户身份仅取会话。
func (handler *Controller) createRelation(ctx *gin.Context) {
	owner, _ := repository.UserIDFromContext(ctx.Request.Context())
	key := "relations:" + owner
	if handler.config.RateLimiter != nil {
		allowed, err := handler.config.RateLimiter.Allow(ctx.Request.Context(), key, 20, time.Minute)
		if err == nil && !allowed {
			failure(ctx, http.StatusTooManyRequests, "rate_limited", "关联操作过于频繁")
			return
		}
	}
	if !handler.batchLimiter.allowedRequests(key, 20, time.Minute) {
		failure(ctx, http.StatusTooManyRequests, "rate_limited", "关联操作过于频繁")
		return
	}
	mediaType, _, err := mime.ParseMediaType(ctx.GetHeader("Content-Type"))
	if err != nil || mediaType != "application/json" {
		failure(ctx, http.StatusBadRequest, "invalid_relation", "关联请求格式无效")
		return
	}
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, 2048)
	var input struct {
		Source model.InboxSelection `json:"source"`
		Target model.InboxSelection `json:"target"`
	}
	decoder := json.NewDecoder(ctx.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		failure(ctx, http.StatusBadRequest, "invalid_relation", "关联请求格式无效")
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		failure(ctx, http.StatusBadRequest, "invalid_relation", "关联请求格式无效")
		return
	}
	entry, err := handler.config.Relations.Create(ctx.Request.Context(), input.Source, input.Target)
	if err != nil {
		relationFailure(ctx, err)
		return
	}
	ctx.JSON(http.StatusCreated, gin.H{"data": entry})
}

// deleteRelation 只删除当前账号拥有的关系，不接触两端文件或站外位置。
func (handler *Controller) deleteRelation(ctx *gin.Context) {
	if err := handler.config.Relations.Delete(ctx.Request.Context(), ctx.Param("id")); err != nil {
		relationFailure(ctx, err)
		return
	}
	ctx.Status(http.StatusNoContent)
}
