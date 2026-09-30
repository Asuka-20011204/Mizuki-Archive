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

var ErrInvalidExternalResource = errors.New("invalid external resource")
var ErrExternalResourceIdentity = errors.New("missing verified user identity")

// ExternalResources 负责校验卡片内容和用户身份；不请求或探测用户填写的位置。
type ExternalResources struct {
	store repository.ExternalResourceStore
}

// NewExternalResources 注入独立仓储，拒绝无持久化依赖的服务。
func NewExternalResources(store repository.ExternalResourceStore) (*ExternalResources, error) {
	if store == nil {
		return nil, errors.New("missing external resource store")
	}
	return &ExternalResources{store: store}, nil
}

// normalizeExternalResource 对长度、控制字符、状态和标签做统一校验，位置始终只是文本。
func normalizeExternalResource(value model.ExternalResource) (model.ExternalResource, error) {
	value.Title = strings.TrimSpace(value.Title)
	value.Location = strings.TrimSpace(value.Location)
	value.ResourceType = strings.TrimSpace(value.ResourceType)
	value.Version = strings.TrimSpace(value.Version)
	value.Note = strings.TrimSpace(value.Note)
	if value.Status == "" {
		value.Status = "pending"
	}
	for _, field := range []struct {
		text     string
		max      int
		required bool
	}{
		{value.Title, 200, true}, {value.Location, 2048, true},
		{value.ResourceType, 40, true}, {value.Version, 80, false}, {value.Note, 2000, false},
	} {
		if (field.required && field.text == "") || utf8.RuneCountInString(field.text) > field.max {
			return model.ExternalResource{}, ErrInvalidExternalResource
		}
		for _, character := range field.text {
			if unicode.IsControl(character) {
				return model.ExternalResource{}, ErrInvalidExternalResource
			}
		}
	}
	switch value.Status {
	case "pending", "available", "uncertain", "broken", "downloaded":
	default:
		return model.ExternalResource{}, ErrInvalidExternalResource
	}
	tags, err := NormalizeTags(value.Tags)
	if err != nil {
		return model.ExternalResource{}, err
	}
	value.Tags = tags
	return value, nil
}

// verifiedExternalOwner 确保业务请求已通过会话中间件，不允许缺少身份的上下文越过归属隔离。
func verifiedExternalOwner(ctx context.Context) error {
	if _, ok := repository.UserIDFromContext(ctx); !ok {
		return ErrExternalResourceIdentity
	}
	return nil
}

// Create 生成不可预测卡片 ID；重复地址暂不自动合并或删除。
func (resources *ExternalResources) Create(ctx context.Context, value model.ExternalResource) (model.ExternalResource, error) {
	if err := verifiedExternalOwner(ctx); err != nil {
		return model.ExternalResource{}, err
	}
	value, err := normalizeExternalResource(value)
	if err != nil {
		return model.ExternalResource{}, err
	}
	identifier := make([]byte, 16)
	if _, err := rand.Read(identifier); err != nil {
		return model.ExternalResource{}, err
	}
	value.ID = hex.EncodeToString(identifier)
	value.OrganizationStatus = "pending"
	value.CreatedAt = time.Now().UTC()
	value.UpdatedAt = value.CreatedAt
	if err := resources.store.CreateExternalResource(ctx, value); err != nil {
		return model.ExternalResource{}, err
	}
	resources.attachDuplicate(ctx, &value)
	return value, nil
}

// Get 只读取当前会话用户可见的卡片，非法 ID 与跨用户 ID 均按不存在处理。
func (resources *ExternalResources) Get(ctx context.Context, id string) (model.ExternalResource, error) {
	if err := verifiedExternalOwner(ctx); err != nil {
		return model.ExternalResource{}, err
	}
	if !validResourceID(id) {
		return model.ExternalResource{}, repository.ErrNotFound
	}
	return resources.store.GetExternalResource(ctx, id)
}

// List 返回当前用户的最近 100 条卡片；查询关键词由持久层限定在标题和位置。
func (resources *ExternalResources) List(ctx context.Context, query string) ([]model.ExternalResource, error) {
	if err := verifiedExternalOwner(ctx); err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if utf8.RuneCountInString(query) > 100 {
		return nil, ErrInvalidExternalResource
	}
	return resources.store.ListExternalResources(ctx, query)
}

// Update 以完整卡片替换可编辑字段，持久层原子更新并保留创建时间和归属。
func (resources *ExternalResources) Update(ctx context.Context, id string, value model.ExternalResource) (model.ExternalResource, error) {
	if err := verifiedExternalOwner(ctx); err != nil {
		return model.ExternalResource{}, err
	}
	if !validResourceID(id) {
		return model.ExternalResource{}, repository.ErrNotFound
	}
	value, err := normalizeExternalResource(value)
	if err != nil {
		return model.ExternalResource{}, err
	}
	value.ID = id
	value.UpdatedAt = time.Now().UTC()
	if err := resources.store.UpdateExternalResource(ctx, value); err != nil {
		return model.ExternalResource{}, err
	}
	updated, err := resources.Get(ctx, id)
	if err != nil {
		return model.ExternalResource{}, err
	}
	resources.attachDuplicate(ctx, &updated)
	return updated, nil
}

// attachDuplicate 只比较 HTTP(S) 地址；查重故障不改变已保存卡片的成功结果。
func (resources *ExternalResources) attachDuplicate(ctx context.Context, value *model.ExternalResource) {
	key := model.ExternalLinkKey(value.Location)
	if key == "" {
		return
	}
	if duplicates, ok := resources.store.(repository.DuplicateStore); ok {
		if hint, err := duplicates.FindExternalDuplicate(ctx, key, value.ID); err == nil {
			value.Duplicate = hint
		} else {
			value.DuplicateCheckUnavailable = true
		}
	}
}

// Delete 删除当前用户的卡片及其标签，不触碰用户所指向的外部原件。
func (resources *ExternalResources) Delete(ctx context.Context, id string) error {
	if err := verifiedExternalOwner(ctx); err != nil {
		return err
	}
	if !validResourceID(id) {
		return repository.ErrNotFound
	}
	return resources.store.DeleteExternalResource(ctx, id)
}
