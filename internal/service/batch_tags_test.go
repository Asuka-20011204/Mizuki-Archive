package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
)

type batchTagsStoreFake struct {
	items   []model.InboxSelection
	tag     string
	mode    string
	changed []model.InboxSelection
}

// BatchUpdateTags 记录已通过业务校验的请求，返回真正变化的条目供前端撤销。
func (store *batchTagsStoreFake) BatchUpdateTags(_ context.Context, items []model.InboxSelection, tag, mode string) ([]model.InboxSelection, error) {
	store.items, store.tag, store.mode = items, tag, mode
	return store.changed, nil
}

// TestBatchTagsValidation 验证服务端身份、来源/ID、标签与数量上限，拒绝重复项。
func TestBatchTagsValidation(t *testing.T) {
	store := &batchTagsStoreFake{changed: []model.InboxSelection{{Source: "file", ID: strings.Repeat("a", 32)}}}
	service, err := NewBatchTags(store)
	if err != nil {
		t.Fatal(err)
	}
	owner := repository.WithUserID(context.Background(), "owner")
	valid := []model.InboxSelection{{Source: "file", ID: strings.Repeat("a", 32)}}
	if _, err := service.Apply(context.Background(), valid, "学习", "add"); !errors.Is(err, ErrInboxIdentity) {
		t.Fatalf("anonymous batch: %v", err)
	}
	for _, items := range [][]model.InboxSelection{nil, append(valid, valid[0]), {{Source: "external", ID: "bad"}}, {{Source: "other", ID: strings.Repeat("a", 32)}}, make([]model.InboxSelection, 51)} {
		if _, err := service.Apply(owner, items, "学习", "add"); !errors.Is(err, ErrInvalidInboxInput) {
			t.Fatalf("invalid items %+v: %v", items, err)
		}
	}
	if _, err := service.Apply(owner, valid, "<script>", "add"); !errors.Is(err, ErrInvalidTag) {
		t.Fatalf("invalid tag: %v", err)
	}
	if _, err := service.Apply(owner, valid, "学习", "replace"); !errors.Is(err, ErrInvalidInboxInput) {
		t.Fatalf("invalid action: %v", err)
	}
	changed, err := service.Apply(owner, valid, " 学习 ", "add")
	if err != nil || store.tag != "学习" || store.mode != "add" || len(changed) != 1 {
		t.Fatalf("valid add: %+v, %+v, %v", store, changed, err)
	}
}
