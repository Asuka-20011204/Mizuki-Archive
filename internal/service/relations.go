package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"

	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
)

var ErrInvalidRelation = errors.New("invalid relation")
var ErrRelationIdentity = errors.New("missing verified relation identity")

// Relations 管理两类资料的私有双向关联，不把外部卡片的位置当作可访问文件。
type Relations struct{ store repository.RelationStore }

// NewRelations 拒绝缺少持久化依赖的配置，保持服务与具体数据库实现解耦。
func NewRelations(store repository.RelationStore) (*Relations, error) {
	if store == nil {
		return nil, errors.New("missing relation store")
	}
	return &Relations{store: store}, nil
}

// validRelationSelection 仅接受两种资源来源及服务端生成的十六进制标识。
func validRelationSelection(item model.InboxSelection) bool {
	return (item.Source == "file" || item.Source == "external") && validResourceID(item.ID)
}

// relationIdentity 确保仓储调用前已有服务端验证的用户身份。
func relationIdentity(ctx context.Context) error {
	if _, ok := repository.UserIDFromContext(ctx); !ok {
		return ErrRelationIdentity
	}
	return nil
}

// Create 创建无方向关联；重复、自关联与越权目标由业务层和事务层共同拒绝。
func (relations *Relations) Create(ctx context.Context, source, target model.InboxSelection) (model.ResourceRelation, error) {
	if err := relationIdentity(ctx); err != nil {
		return model.ResourceRelation{}, err
	}
	if !validRelationSelection(source) || !validRelationSelection(target) || source == target {
		return model.ResourceRelation{}, ErrInvalidRelation
	}
	identifier := make([]byte, 16)
	if _, err := rand.Read(identifier); err != nil {
		return model.ResourceRelation{}, err
	}
	return relations.store.CreateRelation(ctx, source, target, hex.EncodeToString(identifier))
}

// List 只查询当前用户拥有的条目；仓储限制每项关联数量与结果窗口。
func (relations *Relations) List(ctx context.Context, source model.InboxSelection) ([]model.ResourceRelation, error) {
	if err := relationIdentity(ctx); err != nil {
		return nil, err
	}
	if !validRelationSelection(source) {
		return nil, ErrInvalidRelation
	}
	return relations.store.ListRelations(ctx, source)
}

// Delete 只使用随机关联 ID 删除当前账号的关系，不删除任何资料原件。
func (relations *Relations) Delete(ctx context.Context, id string) error {
	if err := relationIdentity(ctx); err != nil {
		return err
	}
	if !validResourceID(id) {
		return ErrInvalidRelation
	}
	return relations.store.DeleteRelation(ctx, id)
}
