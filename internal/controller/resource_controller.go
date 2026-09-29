package controller

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
	"mizuki-archive/internal/service"
)

// upload 解析并限制 multipart 请求，将文件校验交给 Service，再映射为明确的 HTTP 状态。
func (handler *Controller) upload(ctx *gin.Context) {
	// HTTP 层限制请求总体大小，Service 再限制真实文件内容大小与类型。
	// 这里只接受第一个名为 file 的 multipart 部件；其余部件不会被当成资料存储。
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, service.MaxFileBytes+(1<<20))
	reader, err := ctx.Request.MultipartReader()
	if err != nil {
		failure(ctx, http.StatusBadRequest, "invalid_upload", "需要上传文件")
		return
	}
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "file" || part.FileName() == "" {
		failure(ctx, http.StatusBadRequest, "invalid_upload", "需要上传一个文件")
		return
	}
	defer part.Close()
	resource, err := handler.config.Resources.Upload(ctx.Request.Context(), part.FileName(), part)
	switch {
	case errors.Is(err, service.ErrInvalidFilename):
		failure(ctx, http.StatusBadRequest, "invalid_filename", "文件名无效")
	case errors.Is(err, service.ErrUnsupportedFile):
		failure(ctx, http.StatusUnsupportedMediaType, "unsupported_file", "不支持此文件格式")
	case errors.Is(err, service.ErrFileTooLarge):
		failure(ctx, http.StatusRequestEntityTooLarge, "upload_too_large", "文件超过 50 MB")
	case err != nil:
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			failure(ctx, http.StatusRequestEntityTooLarge, "upload_too_large", "文件超过 50 MB")
		} else {
			failure(ctx, http.StatusInternalServerError, "internal", "无法保存资料")
		}
	default:
		ctx.JSON(http.StatusCreated, gin.H{"data": resource})
	}
}

// list 校验页码和筛选条件，多取一条记录判断下一页并返回列表元数据。
func (handler *Controller) list(ctx *gin.Context) {
	// 页码和筛选条件只在 HTTP 边界解析，Service/Repository 接收已校验的查询对象。
	page := 1
	if ctx.Query("page") != "" {
		parsed, err := strconv.Atoi(ctx.Query("page"))
		if err != nil || parsed < 1 || parsed > 10000 {
			failure(ctx, http.StatusBadRequest, "invalid_page", "页码无效")
			return
		}
		page = parsed
	}
	search := strings.TrimSpace(ctx.Query("q"))
	kind := ctx.Query("kind")
	tag := strings.TrimSpace(ctx.Query("tag"))
	normalizedTag, tagErr := service.NormalizeTag(tag)
	if tag != "" && tagErr != nil {
		failure(ctx, http.StatusBadRequest, "invalid_filter", "标签筛选条件无效")
		return
	}
	if utf8.RuneCountInString(search) > 100 || (kind != "" && kind != "pdf" && kind != "image" && kind != "markdown" && kind != "text") {
		failure(ctx, http.StatusBadRequest, "invalid_filter", "筛选条件无效")
		return
	}
	// 多取一条记录判断是否有下一页，不单独跑一次 COUNT 查询。
	query := model.ListQuery{Search: search, Kind: kind, Tag: normalizedTag, Limit: 31, Offset: (page - 1) * 30}
	resources, err := handler.config.Resources.List(ctx.Request.Context(), query)
	if err != nil {
		failure(ctx, http.StatusInternalServerError, "internal", "无法读取资料")
		return
	}
	hasMore := len(resources) > 30
	if hasMore {
		resources = resources[:30]
	}
	ctx.JSON(http.StatusOK, gin.H{"data": resources, "meta": gin.H{"page": page, "has_more": hasMore}})
}

// recent 返回 Redis 记录的最近访问资料；缓存缺失时返回空数组，不影响主资料库操作。
func (handler *Controller) recent(ctx *gin.Context) {
	items, err := handler.config.Resources.Recent(ctx.Request.Context(), 10)
	if err != nil {
		failure(ctx, http.StatusInternalServerError, "internal", "无法读取最近访问资料")
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": items})
}

// listTags 返回已存在的标签建议；搜索值只用于缩小建议范围，不创建新标签。
func (handler *Controller) listTags(ctx *gin.Context) {
	search := strings.TrimSpace(ctx.Query("q"))
	if search != "" && utf8.RuneCountInString(search) > 24 {
		failure(ctx, http.StatusBadRequest, "invalid_tag", "标签搜索条件无效")
		return
	}
	tags, err := handler.config.Resources.ListTags(ctx.Request.Context(), search)
	if errors.Is(err, service.ErrInvalidTag) {
		failure(ctx, http.StatusBadRequest, "invalid_tag", "标签搜索条件无效")
		return
	}
	if err != nil {
		failure(ctx, http.StatusInternalServerError, "internal", "无法读取标签")
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": tags})
}

// resource 是详情与下载共用的资料查找入口，统一处理不存在与内部错误。
func (handler *Controller) resource(ctx *gin.Context) (model.Resource, bool) {
	// 详情与下载共享“未找到/内部错误”映射，避免对外暴露存储层错误细节。
	resource, err := handler.config.Resources.Get(ctx.Request.Context(), ctx.Param("id"))
	if errors.Is(err, repository.ErrNotFound) {
		failure(ctx, http.StatusNotFound, "not_found", "资料不存在")
		return model.Resource{}, false
	}
	if err != nil {
		failure(ctx, http.StatusInternalServerError, "internal", "无法读取资料")
		return model.Resource{}, false
	}
	return resource, true
}

// get 返回已认证用户可见的资料元数据，文件原件仍由下载接口单独提供。
func (handler *Controller) get(ctx *gin.Context) {
	resource, ok := handler.resource(ctx)
	if ok {
		ctx.JSON(http.StatusOK, gin.H{"data": resource})
	}
}

// setName 严格解析展示名并交给 Service 校验，原始文件名和磁盘存储键不会被请求修改。
func (handler *Controller) setName(ctx *gin.Context) {
	if ctx.ContentType() != "application/json" {
		failure(ctx, http.StatusBadRequest, "invalid_name", "名称请求格式无效")
		return
	}
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, 1024)
	decoder := json.NewDecoder(ctx.Request.Body)
	decoder.DisallowUnknownFields()
	var input struct {
		Name *string `json:"name"`
	}
	if err := decoder.Decode(&input); err != nil || input.Name == nil {
		failure(ctx, http.StatusBadRequest, "invalid_name", "名称请求格式无效")
		return
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		failure(ctx, http.StatusBadRequest, "invalid_name", "名称请求格式无效")
		return
	}
	resource, err := handler.config.Resources.SetName(ctx.Request.Context(), ctx.Param("id"), *input.Name)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		failure(ctx, http.StatusNotFound, "not_found", "资料不存在")
	case errors.Is(err, service.ErrInvalidName):
		failure(ctx, http.StatusBadRequest, "invalid_name", "资料名称无效")
	case err != nil:
		failure(ctx, http.StatusInternalServerError, "internal", "无法保存资料名称")
	default:
		ctx.JSON(http.StatusOK, gin.H{"data": resource})
	}
}

// setTags 严格解析完整标签数组，并把规范化、事务替换和错误映射交给 Service。
func (handler *Controller) setTags(ctx *gin.Context) {
	if ctx.ContentType() != "application/json" {
		failure(ctx, http.StatusBadRequest, "invalid_tags", "标签请求格式无效")
		return
	}
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, 1024)
	decoder := json.NewDecoder(ctx.Request.Body)
	decoder.DisallowUnknownFields()
	var input struct {
		Tags *[]string `json:"tags"`
	}
	if err := decoder.Decode(&input); err != nil || input.Tags == nil {
		failure(ctx, http.StatusBadRequest, "invalid_tags", "标签请求格式无效")
		return
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		failure(ctx, http.StatusBadRequest, "invalid_tags", "标签请求格式无效")
		return
	}
	resource, err := handler.config.Resources.SetTags(ctx.Request.Context(), ctx.Param("id"), *input.Tags)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		failure(ctx, http.StatusNotFound, "not_found", "资料不存在")
	case errors.Is(err, service.ErrInvalidTag), errors.Is(err, service.ErrTooManyTags):
		failure(ctx, http.StatusBadRequest, "invalid_tags", "标签内容无效")
	case err != nil:
		failure(ctx, http.StatusInternalServerError, "internal", "无法保存标签")
	default:
		ctx.JSON(http.StatusOK, gin.H{"data": resource})
	}
}

// setFavorite 仅接受有同源会话的明确布尔值；拒绝缺失、未知字段和多段 JSON，错误不回显内部细节。
func (handler *Controller) setFavorite(ctx *gin.Context) {
	if ctx.ContentType() != "application/json" {
		failure(ctx, http.StatusBadRequest, "invalid_favorite", "收藏状态无效")
		return
	}
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, 1024)
	decoder := json.NewDecoder(ctx.Request.Body)
	decoder.DisallowUnknownFields()
	var input struct {
		Favorite *bool `json:"favorite"`
	}
	if err := decoder.Decode(&input); err != nil || input.Favorite == nil {
		failure(ctx, http.StatusBadRequest, "invalid_favorite", "收藏状态无效")
		return
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		failure(ctx, http.StatusBadRequest, "invalid_favorite", "收藏状态无效")
		return
	}
	resource, err := handler.config.Resources.SetFavorite(ctx.Request.Context(), ctx.Param("id"), *input.Favorite)
	if errors.Is(err, repository.ErrNotFound) {
		failure(ctx, http.StatusNotFound, "not_found", "资料不存在")
		return
	}
	if err != nil {
		failure(ctx, http.StatusInternalServerError, "internal", "无法更新收藏")
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": resource})
}

// delete 将资料标记为删除并清理受控原件；删除后的资料不再出现在任何正常查询中。
func (handler *Controller) delete(ctx *gin.Context) {
	err := handler.config.Resources.Delete(ctx.Request.Context(), ctx.Param("id"))
	switch {
	case errors.Is(err, repository.ErrNotFound):
		failure(ctx, http.StatusNotFound, "not_found", "资料不存在")
	case err != nil:
		failure(ctx, http.StatusInternalServerError, "internal", "资料已隐藏，但原件清理未完成")
	default:
		ctx.Status(http.StatusNoContent)
	}
}

// preview 只内联渲染 PNG/JPEG/WebP 与纯文本内容；Markdown 按纯文本返回，避免用户内容变成本站 HTML。
func (handler *Controller) preview(ctx *gin.Context) {
	resource, ok := handler.resource(ctx)
	if !ok {
		return
	}
	file, err := handler.config.Resources.Open(resource)
	if err != nil {
		failure(ctx, http.StatusInternalServerError, "internal", "文件不可用")
		return
	}
	defer file.Close()
	ctx.Header("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": resource.Name}))
	switch resource.Kind {
	case "image":
		ctx.Header("Content-Type", resource.MIME)
		http.ServeContent(ctx.Writer, ctx.Request, resource.Name, resource.CreatedAt, file)
	case "text", "markdown":
		const previewLimit = 1 << 20
		content, err := io.ReadAll(io.LimitReader(file, previewLimit+1))
		if err != nil {
			failure(ctx, http.StatusInternalServerError, "internal", "无法读取预览")
			return
		}
		if int64(len(content)) > previewLimit {
			failure(ctx, http.StatusRequestEntityTooLarge, "preview_too_large", "文件预览超过 1 MB，请下载原文件")
			return
		}
		ctx.Header("Content-Type", "text/plain; charset=utf-8")
		_, _ = ctx.Writer.Write(content)
	default:
		failure(ctx, http.StatusUnsupportedMediaType, "preview_unavailable", "此格式暂不支持在线预览")
	}
}

// download 从受控目录取回原件并强制附件下载，避免活动内容在本站源内联执行。
func (handler *Controller) download(ctx *gin.Context) {
	resource, ok := handler.resource(ctx)
	if !ok {
		return
	}
	file, err := handler.config.Resources.Open(resource)
	if err != nil {
		failure(ctx, http.StatusInternalServerError, "internal", "文件不可用")
		return
	}
	defer file.Close()
	// 未经过净化的原件只作为附件下载，避免 HTML/脚本在本站源内联执行。
	// 使用记录创建时间作为下载时间戳，不读取用户原始文件名对应的磁盘路径。
	ctx.Header("Content-Type", resource.MIME)
	ctx.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": resource.OriginalName}))
	http.ServeContent(ctx.Writer, ctx.Request, resource.OriginalName, resource.CreatedAt, file)
}
