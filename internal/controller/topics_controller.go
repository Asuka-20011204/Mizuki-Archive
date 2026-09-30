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

// topicFailure 把非法请求与越权资源分别映射为 400、404，不泄露持久化细节。
func topicFailure(ctx *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidTopic):
		failure(ctx, http.StatusBadRequest, "invalid_topic", "专题内容无效")
	case errors.Is(err, repository.ErrNotFound):
		failure(ctx, http.StatusNotFound, "not_found", "专题或资料不存在")
	case errors.Is(err, repository.ErrTopicLimit):
		failure(ctx, http.StatusConflict, "topic_limit", "每个账号最多创建 30 个专题")
	case errors.Is(err, service.ErrTopicIdentity):
		failure(ctx, http.StatusUnauthorized, "unauthorized", "请先登录")
	default:
		failure(ctx, http.StatusInternalServerError, "internal", "专题操作失败")
	}
}

// readTopicInput 限制请求体大小并拒绝未知字段、尾随 JSON 及非 JSON 媒体类型。
func readTopicInput(ctx *gin.Context) (model.TopicInput, error) {
	mediaType, _, err := mime.ParseMediaType(ctx.GetHeader("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return model.TopicInput{}, service.ErrInvalidTopic
	}
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, 32*1024)
	decoder := json.NewDecoder(ctx.Request.Body)
	decoder.DisallowUnknownFields()
	var body struct {
		Title       string  `json:"title"`
		Intro       string  `json:"intro"`
		CoverFileID *string `json:"cover_file_id"`
		Sections    []struct {
			Title string                 `json:"title"`
			Items []model.InboxSelection `json:"items"`
		} `json:"sections"`
	}
	if err := decoder.Decode(&body); err != nil {
		return model.TopicInput{}, service.ErrInvalidTopic
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return model.TopicInput{}, service.ErrInvalidTopic
	}
	input := model.TopicInput{Title: body.Title, Intro: body.Intro, CoverFileID: body.CoverFileID, Sections: make([]model.TopicSection, 0, len(body.Sections))}
	for _, section := range body.Sections {
		items := make([]model.TopicItem, 0, len(section.Items))
		for _, item := range section.Items {
			items = append(items, model.TopicItem{Source: item.Source, ID: item.ID})
		}
		input.Sections = append(input.Sections, model.TopicSection{Title: section.Title, Items: items})
	}
	return input, nil
}

// listTopics 只返回已验证会话账号下的专题摘要。
func (handler *Controller) listTopics(ctx *gin.Context) {
	list, err := handler.config.Topics.List(ctx.Request.Context())
	if err != nil {
		topicFailure(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": list})
}

// getTopic 返回有序内容及目前可见引用名称。
func (handler *Controller) getTopic(ctx *gin.Context) {
	entry, err := handler.config.Topics.Get(ctx.Request.Context(), ctx.Param("id"))
	if err != nil {
		topicFailure(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": entry})
}

// createTopic 使用会话身份创建私有专题，忽略任何客户端所有者主张。
func (handler *Controller) createTopic(ctx *gin.Context) {
	input, err := readTopicInput(ctx)
	if err != nil {
		topicFailure(ctx, err)
		return
	}
	entry, err := handler.config.Topics.Create(ctx.Request.Context(), input)
	if err != nil {
		topicFailure(ctx, err)
		return
	}
	ctx.JSON(http.StatusCreated, gin.H{"data": entry})
}

// updateTopic 在已验证身份下整体替换本人专题。
func (handler *Controller) updateTopic(ctx *gin.Context) {
	input, err := readTopicInput(ctx)
	if err != nil {
		topicFailure(ctx, err)
		return
	}
	entry, err := handler.config.Topics.Update(ctx.Request.Context(), ctx.Param("id"), input)
	if err != nil {
		topicFailure(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": entry})
}

// deleteTopic 删除本人专题而不删除原始文件或外部卡片。
func (handler *Controller) deleteTopic(ctx *gin.Context) {
	if err := handler.config.Topics.Delete(ctx.Request.Context(), ctx.Param("id")); err != nil {
		topicFailure(ctx, err)
		return
	}
	ctx.Status(http.StatusNoContent)
}
