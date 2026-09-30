package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
)

// batchFavoriteFake 记录通过服务层校验的收藏目标，避免单测访问真实资料。
type batchFavoriteFake struct {
	items    []model.InboxSelection
	favorite bool
}

// BatchUpdateFavorites 返回变化项，供服务测试确认没有反转布尔目标。
func (store *batchFavoriteFake) BatchUpdateFavorites(_ context.Context, items []model.InboxSelection, favorite bool) ([]model.InboxSelection, error) {
	store.items, store.favorite = items, favorite
	return items, nil
}

// TestBatchFavoritesValidation 覆盖匿名、空批、重复项、无效 ID、超量和明确的目标状态。
func TestBatchFavoritesValidation(t *testing.T) {
	store := &batchFavoriteFake{}
	batch, err := NewBatchFavorites(store)
	if err != nil {
		t.Fatal(err)
	}
	item := model.InboxSelection{Source: "external", ID: strings.Repeat("a", 32)}
	owner := repository.WithUserID(context.Background(), "owner")
	if _, err := batch.Apply(context.Background(), []model.InboxSelection{item}, true); !errors.Is(err, ErrInboxIdentity) {
		t.Fatalf("anonymous: %v", err)
	}
	for _, items := range [][]model.InboxSelection{nil, {item, item}, {{Source: "file", ID: "bad"}}, make([]model.InboxSelection, 51)} {
		if _, err := batch.Apply(owner, items, true); !errors.Is(err, ErrInvalidInboxInput) {
			t.Fatalf("invalid items: %v", err)
		}
	}
	if _, err := batch.Apply(owner, []model.InboxSelection{item}, false); err != nil || store.favorite || len(store.items) != 1 {
		t.Fatalf("explicit false: %+v %v", store, err)
	}
	if _, err := batch.Apply(owner, []model.InboxSelection{item}, true); err != nil || !store.favorite {
		t.Fatalf("explicit true: %+v %v", store, err)
	}
}
