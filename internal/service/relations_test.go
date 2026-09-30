package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
)

// relationMemoryStore 验证业务层在任何数据库操作前完成来源、ID 和用户身份校验。
type relationMemoryStore struct {
	created int
	listed  int
}

// CreateRelation 记录已通过校验的创建调用，供测试断言非法输入不会下沉。
func (store *relationMemoryStore) CreateRelation(_ context.Context, _, _ model.InboxSelection, _ string) (model.ResourceRelation, error) {
	store.created++
	return model.ResourceRelation{}, nil
}

// ListRelations 记录列表调用，测试不具备身份的请求不可读取资料。
func (store *relationMemoryStore) ListRelations(_ context.Context, _ model.InboxSelection) ([]model.ResourceRelation, error) {
	store.listed++
	return []model.ResourceRelation{}, nil
}

// DeleteRelation 模拟删除路径；归属判断交由真实仓储的集成测试覆盖。
func (store *relationMemoryStore) DeleteRelation(_ context.Context, _ string) error { return nil }

// TestRelationsValidation 验证无身份、自关联、错误来源及非法标识都被业务层拒绝。
func TestRelationsValidation(t *testing.T) {
	store := &relationMemoryStore{}
	relations, err := NewRelations(store)
	if err != nil {
		t.Fatal(err)
	}
	file := model.InboxSelection{Source: "file", ID: strings.Repeat("a", 32)}
	card := model.InboxSelection{Source: "external", ID: strings.Repeat("b", 32)}
	if _, err := relations.Create(context.Background(), file, card); !errors.Is(err, ErrRelationIdentity) {
		t.Fatalf("missing identity: %v", err)
	}
	ctx := repository.WithUserID(context.Background(), strings.Repeat("c", 32))
	for _, target := range []model.InboxSelection{file, {Source: "unknown", ID: card.ID}, {Source: "external", ID: "invalid"}} {
		if _, err := relations.Create(ctx, file, target); !errors.Is(err, ErrInvalidRelation) {
			t.Fatalf("invalid target %v: %v", target, err)
		}
	}
	if store.created != 0 {
		t.Fatalf("invalid requests reached store: %d", store.created)
	}
	if _, err := relations.Create(ctx, file, card); err != nil {
		t.Fatal(err)
	}
	if store.created != 1 {
		t.Fatalf("valid request was not stored: %d", store.created)
	}
	if _, err := relations.List(context.Background(), file); !errors.Is(err, ErrRelationIdentity) {
		t.Fatalf("anonymous list: %v", err)
	}
	if store.listed != 0 {
		t.Fatal("anonymous list reached store")
	}
}
