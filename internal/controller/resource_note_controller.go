package controller

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
	"mizuki-archive/internal/service"
)

// resourceNoteInput 只接受笔记可编辑字段；资源归属、ID 和时间由服务端决定。
type resourceNoteInput struct {
	PageNumber *int   `json:"page_number"`
	Excerpt    string `json:"excerpt"`
	Content    string `json:"content"`
	Source     string `json:"source"`
}

// toModel 将用户文字交给 Service 校验，不信任正文中的身份或文件 ID。
func (input resourceNoteInput) toModel() model.ResourceNote {
	return model.ResourceNote{PageNumber: input.PageNumber, Excerpt: input.Excerpt, Content: input.Content, Source: input.Source}
}

// resourceNoteFailure 保持跨用户未找到、参数错误和容量上限的明确 HTTP 语义。
func resourceNoteFailure(ctx *gin.Context, err error) {
	switch {
	case errors.Is(err, repository.ErrNotFound):
		failure(ctx, http.StatusNotFound, "not_found", "笔记或资料不存在")
	case errors.Is(err, service.ErrInvalidResourceNote):
		failure(ctx, http.StatusBadRequest, "invalid_note", "笔记内容或页码无效")
	case errors.Is(err, repository.ErrResourceNoteLimit):
		failure(ctx, http.StatusConflict, "note_limit", "每份资料最多保存 100 条笔记")
	default:
		failure(ctx, http.StatusInternalServerError, "internal", "无法操作笔记")
	}
}

// listResourceNotes 列出当前会话用户在指定资料中的私人笔记。
func (handler *Controller) listResourceNotes(ctx *gin.Context) {
	notes, err := handler.config.Notes.List(ctx.Request.Context(), ctx.Param("id"))
	if err != nil {
		resourceNoteFailure(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": notes})
}

// createResourceNote 限制请求体大小并只创建属于当前账号文件的笔记。
func (handler *Controller) createResourceNote(ctx *gin.Context) {
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, 64<<10)
	var input resourceNoteInput
	if err := ctx.ShouldBindJSON(&input); err != nil {
		failure(ctx, http.StatusBadRequest, "invalid_note", "笔记格式无效")
		return
	}
	note, err := handler.config.Notes.Create(ctx.Request.Context(), ctx.Param("id"), input.toModel())
	if err != nil {
		resourceNoteFailure(ctx, err)
		return
	}
	ctx.JSON(http.StatusCreated, gin.H{"data": note})
}

// updateResourceNote 仅替换当前用户指定文件中的一条笔记，不修改原文件。
func (handler *Controller) updateResourceNote(ctx *gin.Context) {
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, 64<<10)
	var input resourceNoteInput
	if err := ctx.ShouldBindJSON(&input); err != nil {
		failure(ctx, http.StatusBadRequest, "invalid_note", "笔记格式无效")
		return
	}
	if err := handler.config.Notes.Update(ctx.Request.Context(), ctx.Param("id"), ctx.Param("note_id"), input.toModel()); err != nil {
		resourceNoteFailure(ctx, err)
		return
	}
	ctx.Status(http.StatusNoContent)
}

// deleteResourceNote 只移除笔记，不删除资料或其派生内容。
func (handler *Controller) deleteResourceNote(ctx *gin.Context) {
	if err := handler.config.Notes.Delete(ctx.Request.Context(), ctx.Param("id"), ctx.Param("note_id")); err != nil {
		resourceNoteFailure(ctx, err)
		return
	}
	ctx.Status(http.StatusNoContent)
}
