package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
)

var ErrInvalidResourceNote = errors.New("invalid resource note")

// ResourceNotes 限定笔记只依附当前账号尚未删除的文件；不允许浏览器指定归属或 ID。
type ResourceNotes struct{ store repository.ResourceNoteStore }

// NewResourceNotes 要求独立的笔记持久化契约，避免 Controller 直接读写 GORM。
func NewResourceNotes(store repository.ResourceNoteStore) (*ResourceNotes, error) {
	if store == nil {
		return nil, errors.New("missing resource note store")
	}
	return &ResourceNotes{store: store}, nil
}

// normalizeResourceNote 限制文字长度与页码；正文可换行，但不允许隐藏控制字符。
func normalizeResourceNote(note model.ResourceNote, kind string) (model.ResourceNote, error) {
	note.Excerpt = strings.TrimSpace(note.Excerpt)
	note.Content = strings.TrimSpace(note.Content)
	note.Source = strings.TrimSpace(note.Source)
	if note.Excerpt == "" && note.Content == "" {
		return model.ResourceNote{}, ErrInvalidResourceNote
	}
	if note.PageNumber != nil && (kind != "pdf" || *note.PageNumber < 1 || *note.PageNumber > 100000) {
		return model.ResourceNote{}, ErrInvalidResourceNote
	}
	for _, field := range []struct {
		text      string
		max       int
		multiline bool
	}{
		{note.Excerpt, 2000, true}, {note.Content, 5000, true}, {note.Source, 500, false},
	} {
		if utf8.RuneCountInString(field.text) > field.max {
			return model.ResourceNote{}, ErrInvalidResourceNote
		}
		for _, character := range field.text {
			if unicode.IsControl(character) && !(field.multiline && (character == '\n' || character == '\r' || character == '\t')) {
				return model.ResourceNote{}, ErrInvalidResourceNote
			}
		}
	}
	return note, nil
}

// noteResource 校验会话与文件 ID，跨账号或软删除文件统一按不存在处理。
func (notes *ResourceNotes) noteResource(ctx context.Context, resourceID string) (model.Resource, error) {
	if _, ok := repository.UserIDFromContext(ctx); !ok {
		return model.Resource{}, ErrExternalResourceIdentity
	}
	if !validResourceID(resourceID) {
		return model.Resource{}, repository.ErrNotFound
	}
	return notes.store.GetResource(ctx, resourceID)
}

// Create 为文件创建一条私人注记；每份文件的上限由仓储事务保护。
func (notes *ResourceNotes) Create(ctx context.Context, resourceID string, input model.ResourceNote) (model.ResourceNote, error) {
	resource, err := notes.noteResource(ctx, resourceID)
	if err != nil {
		return model.ResourceNote{}, err
	}
	note, err := normalizeResourceNote(input, resource.Kind)
	if err != nil {
		return model.ResourceNote{}, err
	}
	identifier := make([]byte, 16)
	if _, err := rand.Read(identifier); err != nil {
		return model.ResourceNote{}, err
	}
	note.ID = hex.EncodeToString(identifier)
	note.ResourceID = resourceID
	note.CreatedAt = time.Now().UTC()
	note.UpdatedAt = note.CreatedAt
	if err := notes.store.CreateResourceNote(ctx, note); err != nil {
		return model.ResourceNote{}, err
	}
	return note, nil
}

// List 只读取当前账号这份文件的笔记，空列表与不存在文件严格区分。
func (notes *ResourceNotes) List(ctx context.Context, resourceID string) ([]model.ResourceNote, error) {
	if _, err := notes.noteResource(ctx, resourceID); err != nil {
		return nil, err
	}
	return notes.store.ListResourceNotes(ctx, resourceID)
}

// Update 只覆盖可编辑内容，不接受客户端提供的归属、创建时间或资源 ID。
func (notes *ResourceNotes) Update(ctx context.Context, resourceID, noteID string, input model.ResourceNote) error {
	resource, err := notes.noteResource(ctx, resourceID)
	if err != nil {
		return err
	}
	if !validResourceID(noteID) {
		return repository.ErrNotFound
	}
	note, err := normalizeResourceNote(input, resource.Kind)
	if err != nil {
		return err
	}
	note.ID = noteID
	note.ResourceID = resourceID
	note.UpdatedAt = time.Now().UTC()
	return notes.store.UpdateResourceNote(ctx, note)
}

// Delete 删除当前账号文件的一条笔记，既不删除原件也不访问外部链接。
func (notes *ResourceNotes) Delete(ctx context.Context, resourceID, noteID string) error {
	if _, err := notes.noteResource(ctx, resourceID); err != nil {
		return err
	}
	if !validResourceID(noteID) {
		return repository.ErrNotFound
	}
	return notes.store.DeleteResourceNote(ctx, resourceID, noteID)
}
