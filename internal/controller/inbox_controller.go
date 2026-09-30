package controller

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"mizuki-archive/internal/repository"
	"mizuki-archive/internal/service"
)

// inboxFailure 将非法输入和跨账号资料统一映射为安全的 HTTP 响应。
func inboxFailure(ctx *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidInboxInput):
		failure(ctx, http.StatusBadRequest, "invalid_inbox", "整理状态或页码无效")
	case errors.Is(err, repository.ErrNotFound):
		failure(ctx, http.StatusNotFound, "not_found", "资料不存在")
	default:
		failure(ctx, http.StatusInternalServerError, "internal", "收件箱操作失败")
	}
}

// listInbox 通过服务端会话分别获取两类待整理资料，不读取其他用户的卡片。
func (handler *Controller) listInbox(ctx *gin.Context) {
	page, err := strconv.Atoi(ctx.DefaultQuery("page", "1"))
	if err != nil {
		failure(ctx, http.StatusBadRequest, "invalid_inbox", "页码无效")
		return
	}
	result, err := handler.config.Inbox.List(ctx.Request.Context(), page)
	if err != nil {
		inboxFailure(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": result})
}

// setInboxStatus 以显式目标状态更新单个条目，便于请求重试且不会修改外部链接状态。
func (handler *Controller) setInboxStatus(ctx *gin.Context) {
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, 1<<10)
	var input struct {
		Status string `json:"status"`
	}
	if err := ctx.ShouldBindJSON(&input); err != nil {
		failure(ctx, http.StatusBadRequest, "invalid_inbox", "整理状态格式无效")
		return
	}
	if err := handler.config.Inbox.SetStatus(ctx.Request.Context(), ctx.Param("source"), ctx.Param("id"), input.Status); err != nil {
		inboxFailure(ctx, err)
		return
	}
	ctx.Status(http.StatusNoContent)
}
