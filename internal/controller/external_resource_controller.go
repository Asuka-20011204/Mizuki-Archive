package controller

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
	"mizuki-archive/internal/service"
)

// externalResourceInput 只包含可修改字段，忽略客户端提供的 ID、归属和时间戳。
type externalResourceInput struct {
	Title        string   `json:"title"`
	Location     string   `json:"location"`
	ResourceType string   `json:"resource_type"`
	Version      string   `json:"version"`
	Note         string   `json:"note"`
	Status       string   `json:"status"`
	Tags         []string `json:"tags"`
}

// toModel 把 HTTP 请求转换为不含归属信息的业务模型。
func (input externalResourceInput) toModel() model.ExternalResource {
	return model.ExternalResource{Title: input.Title, Location: input.Location, ResourceType: input.ResourceType, Version: input.Version, Note: input.Note, Status: input.Status, Tags: input.Tags}
}

// externalFailure 将校验失败和跨用户未找到映射为安全响应，不暴露数据库细节。
func externalFailure(ctx *gin.Context, err error) {
	switch {
	case errors.Is(err, repository.ErrNotFound):
		failure(ctx, http.StatusNotFound, "not_found", "资源不存在")
	case errors.Is(err, service.ErrInvalidExternalResource), errors.Is(err, service.ErrInvalidTag), errors.Is(err, service.ErrTooManyTags):
		failure(ctx, http.StatusBadRequest, "invalid_resource", "卡片内容或标签无效")
	default:
		failure(ctx, http.StatusInternalServerError, "internal", "操作失败")
	}
}

// createExternalResource 只接收卡片元数据，位置字段不会触发抓取或上传。
func (handler *Controller) createExternalResource(ctx *gin.Context) {
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, 16<<10)
	var input externalResourceInput
	if err := ctx.ShouldBindJSON(&input); err != nil {
		failure(ctx, http.StatusBadRequest, "invalid_resource", "卡片格式无效")
		return
	}
	resource, err := handler.config.ExternalResources.Create(ctx.Request.Context(), input.toModel())
	if err != nil {
		externalFailure(ctx, err)
		return
	}
	ctx.JSON(http.StatusCreated, gin.H{"data": resource})
}

// getExternalResource 返回当前用户拥有的卡片详情，跨用户访问按不存在处理。
func (handler *Controller) getExternalResource(ctx *gin.Context) {
	resource, err := handler.config.ExternalResources.Get(ctx.Request.Context(), ctx.Param("id"))
	if err != nil {
		externalFailure(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": resource})
}

// listExternalResources 读取当前用户有限数量的卡片，关键词搜索仅匹配元数据。
func (handler *Controller) listExternalResources(ctx *gin.Context) {
	resources, err := handler.config.ExternalResources.List(ctx.Request.Context(), ctx.Query("q"))
	if err != nil {
		externalFailure(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": resources})
}

// updateExternalResource 用完整的可编辑字段替换卡片，数据库负责原子更新标签。
func (handler *Controller) updateExternalResource(ctx *gin.Context) {
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, 16<<10)
	var input externalResourceInput
	if err := ctx.ShouldBindJSON(&input); err != nil {
		failure(ctx, http.StatusBadRequest, "invalid_resource", "卡片格式无效")
		return
	}
	resource, err := handler.config.ExternalResources.Update(ctx.Request.Context(), ctx.Param("id"), input.toModel())
	if err != nil {
		externalFailure(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": resource})
}

// deleteExternalResource 仅删除卡片元数据，绝不触碰其所指向的文件或第三方服务。
func (handler *Controller) deleteExternalResource(ctx *gin.Context) {
	if err := handler.config.ExternalResources.Delete(ctx.Request.Context(), ctx.Param("id")); err != nil {
		externalFailure(ctx, err)
		return
	}
	ctx.Status(http.StatusNoContent)
}
