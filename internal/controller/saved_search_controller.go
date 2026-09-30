package controller

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
	"mizuki-archive/internal/service"
)

// savedSearchInput 只接受视图名称和筛选条件，归属、ID 和创建时间均由服务端决定。
type savedSearchInput struct {
	Name   string             `json:"name"`
	Filter model.SearchFilter `json:"filter"`
}

// createSavedSearch 在当前登录用户范围内保存筛选条件，拒绝重复名称和超量视图。
func (handler *Controller) createSavedSearch(ctx *gin.Context) {
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, 4<<10)
	var input savedSearchInput
	if err := ctx.ShouldBindJSON(&input); err != nil {
		failure(ctx, http.StatusBadRequest, "invalid_search_view", "检索视图内容无效")
		return
	}
	view, err := handler.config.SavedSearches.Create(ctx.Request.Context(), input.Name, input.Filter)
	if err != nil {
		savedSearchFailure(ctx, err)
		return
	}
	ctx.JSON(http.StatusCreated, gin.H{"data": view})
}

// listSavedSearches 只返回登录用户保存的筛选条件，不触发全文检索。
func (handler *Controller) listSavedSearches(ctx *gin.Context) {
	views, err := handler.config.SavedSearches.List(ctx.Request.Context())
	if err != nil {
		savedSearchFailure(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": views})
}

// deleteSavedSearch 删除当前用户的视图；跨账号或无效 ID 和不存在保持同样响应。
func (handler *Controller) deleteSavedSearch(ctx *gin.Context) {
	if err := handler.config.SavedSearches.Delete(ctx.Request.Context(), ctx.Param("id")); err != nil {
		savedSearchFailure(ctx, err)
		return
	}
	ctx.Status(http.StatusNoContent)
}

// savedSearchFailure 将内部错误转换为不泄露账号信息的 HTTP 状态与提示。
func savedSearchFailure(ctx *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidSearch):
		failure(ctx, http.StatusBadRequest, "invalid_search_view", "名称或筛选条件无效")
	case errors.Is(err, repository.ErrSavedSearchConflict):
		failure(ctx, http.StatusConflict, "duplicate_search_view", "这个检索视图名称已存在")
	case errors.Is(err, repository.ErrSavedSearchLimit):
		failure(ctx, http.StatusConflict, "search_view_limit", "最多保存 30 个检索视图")
	case errors.Is(err, repository.ErrNotFound):
		failure(ctx, http.StatusNotFound, "not_found", "检索视图不存在")
	default:
		failure(ctx, http.StatusInternalServerError, "internal", "暂时无法管理检索视图")
	}
}
