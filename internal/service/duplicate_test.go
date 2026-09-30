package service

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
)

type fileDuplicateFake struct {
	*fakeStore
	fail bool
}

// FindFileDuplicate 模拟同账号同哈希查询，排除本次刚保存的文件。
func (store *fileDuplicateFake) FindFileDuplicate(_ context.Context, hash, excludeID string) (*model.DuplicateHint, error) {
	if store.fail {
		return nil, errors.New("duplicate lookup unavailable")
	}
	for _, item := range store.resources {
		if item.SHA256 == hash && item.ID != excludeID {
			return &model.DuplicateHint{ID: item.ID, Name: item.Name}, nil
		}
	}
	return nil, nil
}

// FindExternalDuplicate 满足查重契约，本测试只关注上传文件。
func (store *fileDuplicateFake) FindExternalDuplicate(context.Context, string, string) (*model.DuplicateHint, error) {
	return nil, nil
}

// TestUploadDuplicateHint 验证重复上传仍保存，查重故障也不误报上传失败。
func TestUploadDuplicateHint(t *testing.T) {
	store := &fileDuplicateFake{fakeStore: newFakeStore()}
	resources, err := NewResources(store, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("same document\n")
	first, err := resources.Upload(context.Background(), "one.txt", bytes.NewReader(content))
	if err != nil || first.Duplicate != nil {
		t.Fatalf("first upload: %+v, %v", first, err)
	}
	second, err := resources.Upload(context.Background(), "two.txt", bytes.NewReader(content))
	if err != nil || second.Duplicate == nil || second.Duplicate.ID != first.ID {
		t.Fatalf("second upload: %+v, %v", second, err)
	}
	store.fail = true
	third, err := resources.Upload(context.Background(), "three.txt", bytes.NewReader(content))
	if err != nil || third.Duplicate != nil || !third.DuplicateCheckUnavailable || len(store.resources) != 3 {
		t.Fatalf("lookup failure changed save outcome: %+v, %v", third, err)
	}
}

type externalDuplicateFake struct {
	*externalStoreFake
	fail bool
}

// FindFileDuplicate 满足查重契约，本测试只关注外部卡片。
func (store *externalDuplicateFake) FindFileDuplicate(context.Context, string, string) (*model.DuplicateHint, error) {
	return nil, nil
}

// FindExternalDuplicate 模拟纯文本位置摘要比较，排除正在保存的卡片自身。
func (store *externalDuplicateFake) FindExternalDuplicate(_ context.Context, key, excludeID string) (*model.DuplicateHint, error) {
	if store.fail {
		return nil, errors.New("duplicate lookup unavailable")
	}
	for _, item := range store.items {
		if item.ID != excludeID && model.ExternalLinkKey(item.Location) == key {
			return &model.DuplicateHint{ID: item.ID, Name: item.Title}, nil
		}
	}
	return nil, nil
}

// TestExternalDuplicateHint 验证规范化链接、编辑排除自身、非链接和查重故障行为。
func TestExternalDuplicateHint(t *testing.T) {
	store := &externalDuplicateFake{externalStoreFake: &externalStoreFake{items: map[string]model.ExternalResource{}}}
	resources, err := NewExternalResources(store)
	if err != nil {
		t.Fatal(err)
	}
	ctx := repository.WithUserID(context.Background(), "test-owner")
	first, err := resources.Create(ctx, model.ExternalResource{Title: "first", Location: "https://example.org/a?b=2&a=1", ResourceType: "doc"})
	if err != nil || first.Duplicate != nil {
		t.Fatalf("first card: %+v, %v", first, err)
	}
	second, err := resources.Create(ctx, model.ExternalResource{Title: "second", Location: "HTTPS://EXAMPLE.ORG:443/a?a=1&b=2#ref", ResourceType: "doc"})
	if err != nil || second.Duplicate == nil || second.Duplicate.ID != first.ID {
		t.Fatalf("second card: %+v, %v", second, err)
	}
	updated, err := resources.Update(ctx, first.ID, model.ExternalResource{Title: "first", Location: "local-folder", ResourceType: "doc"})
	if err != nil || updated.Duplicate != nil {
		t.Fatalf("non-HTTP location should not be hinted: %+v, %v", updated, err)
	}
	store.fail = true
	third, err := resources.Create(ctx, model.ExternalResource{Title: "third", Location: second.Location, ResourceType: "doc"})
	if err != nil || third.Duplicate != nil || !third.DuplicateCheckUnavailable || len(store.items) != 3 {
		t.Fatalf("lookup failure changed save outcome: %+v, %v", third, err)
	}
}
