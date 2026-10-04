package controller

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/gin-gonic/gin"
	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
	"mizuki-archive/internal/service"
)

// createProcessingJob 只接受白名单处理类型，任务正文不携带原始文件内容。
func (handler *Controller) createProcessingJob(ctx *gin.Context) {
	if ctx.ContentType() != "application/json" {
		failure(ctx, http.StatusBadRequest, "invalid_job", "任务请求格式无效")
		return
	}
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, 1024)
	decoder := json.NewDecoder(ctx.Request.Body)
	decoder.DisallowUnknownFields()
	var input struct {
		Type *string `json:"type"`
	}
	if err := decoder.Decode(&input); err != nil || input.Type == nil {
		failure(ctx, http.StatusBadRequest, "invalid_job", "任务类型无效")
		return
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		failure(ctx, http.StatusBadRequest, "invalid_job", "任务请求格式无效")
		return
	}
	var (
		job model.ProcessingJob
		err error
	)
	switch *input.Type {
	case model.ProcessingTypeExtractText:
		job, err = handler.config.Processing.CreateTextJob(ctx.Request.Context(), ctx.Param("id"))
	case model.ProcessingTypeGenerateThumbnail:
		job, err = handler.config.Processing.CreateThumbnailJob(ctx.Request.Context(), ctx.Param("id"))
	case model.ProcessingTypeOCR:
		job, err = handler.config.Processing.CreateOCRJob(ctx.Request.Context(), ctx.Param("id"))
	default:
		failure(ctx, http.StatusBadRequest, "unsupported_job", "当前处理类型未开放")
		return
	}
	switch {
	case errors.Is(err, repository.ErrNotFound):
		failure(ctx, http.StatusNotFound, "not_found", "资料不存在")
	case errors.Is(err, service.ErrProcessingResource):
		if *input.Type == model.ProcessingTypeGenerateThumbnail {
			failure(ctx, http.StatusUnprocessableEntity, "unsupported_resource", "当前资料格式不能生成缩略图")
		} else if *input.Type == model.ProcessingTypeOCR {
			failure(ctx, http.StatusUnprocessableEntity, "unsupported_resource", "当前资料格式不能执行 OCR")
		} else {
			failure(ctx, http.StatusUnprocessableEntity, "unsupported_resource", "当前资料格式不能执行文本提取")
		}
	case errors.Is(err, repository.ErrProcessingQueueFull):
		ctx.Header("Retry-After", "5")
		failure(ctx, http.StatusTooManyRequests, "queue_full", "当前处理任务较多，请稍后重试")
	case err != nil:
		failure(ctx, http.StatusInternalServerError, "internal", "无法创建处理任务")
	default:
		ctx.JSON(http.StatusAccepted, gin.H{"data": job})
	}
}

// listProcessingJobs 返回资料最近任务及已成功产物，供详情面板显示处理历史。
func (handler *Controller) listProcessingJobs(ctx *gin.Context) {
	jobs, err := handler.config.Processing.ListJobs(ctx.Request.Context(), ctx.Param("id"))
	if errors.Is(err, repository.ErrNotFound) {
		failure(ctx, http.StatusNotFound, "not_found", "资料不存在")
		return
	}
	if err != nil {
		failure(ctx, http.StatusInternalServerError, "internal", "无法读取处理记录")
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": jobs})
}

// getProcessingJob 返回单条任务的状态、失败摘要和成功后的派生产物元数据。
func (handler *Controller) getProcessingJob(ctx *gin.Context) {
	job, err := handler.config.Processing.GetJob(ctx.Request.Context(), ctx.Param("id"))
	if errors.Is(err, repository.ErrNotFound) {
		failure(ctx, http.StatusNotFound, "not_found", "处理任务不存在")
		return
	}
	if err != nil {
		failure(ctx, http.StatusInternalServerError, "internal", "无法读取处理任务")
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": job})
}

// downloadDerived 下载受权限保护的派生产物，不把数据库中的检索正文直接拼进响应。
func (handler *Controller) downloadDerived(ctx *gin.Context) {
	handler.serveDerived(ctx, "attachment")
}

// previewDerived 以内联方式展示缩略图；只有服务端生成的派生产物可以被打开。
func (handler *Controller) previewDerived(ctx *gin.Context) {
	asset, err := handler.config.Processing.GetAsset(ctx.Request.Context(), ctx.Param("id"))
	if errors.Is(err, repository.ErrNotFound) {
		failure(ctx, http.StatusNotFound, "not_found", "派生产物不存在")
		return
	}
	if err != nil {
		failure(ctx, http.StatusInternalServerError, "internal", "无法读取派生产物")
		return
	}
	if asset.Kind != model.DerivedAssetThumbnail {
		failure(ctx, http.StatusNotFound, "not_found", "该派生产物不支持在线预览")
		return
	}
	handler.serveDerivedFile(ctx, asset, "inline")
}

// serveDerived 负责读取派生产物元数据并选择下载或在线展示的响应方式。
func (handler *Controller) serveDerived(ctx *gin.Context, disposition string) {
	asset, err := handler.config.Processing.GetAsset(ctx.Request.Context(), ctx.Param("id"))
	if errors.Is(err, repository.ErrNotFound) {
		failure(ctx, http.StatusNotFound, "not_found", "派生产物不存在")
		return
	}
	if err != nil {
		failure(ctx, http.StatusInternalServerError, "internal", "无法读取派生产物")
		return
	}
	handler.serveDerivedFile(ctx, asset, disposition)
}

// serveDerivedFile 统一设置文件响应头，避免下载与预览路径出现权限或文件处理差异。
func (handler *Controller) serveDerivedFile(ctx *gin.Context, asset model.DerivedAsset, disposition string) {
	file, err := handler.config.Processing.OpenAsset(asset)
	if err != nil {
		failure(ctx, http.StatusInternalServerError, "internal", "派生产物文件不可用")
		return
	}
	defer file.Close()
	ctx.Header("Content-Type", asset.MIME)
	ctx.Header("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": asset.Name}))
	http.ServeContent(ctx.Writer, ctx.Request, asset.Name, asset.CreatedAt, file)
}
