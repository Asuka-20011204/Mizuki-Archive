package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
)

// portableExportFake 记录调用次数，证明缺失会话时不会读取任何账号的资料。
type portableExportFake struct {
	called int
	data   model.PortableArchive
	err    error
}

// ExportSnapshot 返回测试数据，不隐含权限行为；Service 必须先检查会话。
func (store *portableExportFake) ExportSnapshot(context.Context) (model.PortableArchive, error) {
	store.called++
	return store.data, store.err
}

// TestPortableExportIdentityAndFormat 校验未登录拒绝、格式字段以及原件内容不被导出。
func TestPortableExportIdentityAndFormat(t *testing.T) {
	store := &portableExportFake{data: model.PortableArchive{Files: []model.Resource{{ID: strings.Repeat("a", 32), Name: "说明.txt", StorageKey: "private-path", OwnerID: "private-user"}}}}
	exporter, err := NewPortableExport(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exporter.Create(context.Background()); !errors.Is(err, ErrPortableExportIdentity) || store.called != 0 {
		t.Fatalf("missing identity: calls=%d err=%v", store.called, err)
	}
	content, err := exporter.Create(repository.WithUserID(context.Background(), "owner"))
	if err != nil {
		t.Fatal(err)
	}
	var archive model.PortableArchive
	if err := json.Unmarshal(content, &archive); err != nil || archive.Format != "mizuki-archive-portable" || archive.Version != 1 || len(archive.Files) != 1 {
		t.Fatalf("invalid export: %+v %v", archive, err)
	}
	if strings.Contains(string(content), "private-path") || strings.Contains(string(content), "private-user") {
		t.Fatal("export contains server storage or owner ID")
	}
}

// TestPortableExportLimit 确保超大结果明确失败，不返回截断后看似完整的 JSON。
func TestPortableExportLimit(t *testing.T) {
	store := &portableExportFake{data: model.PortableArchive{ExternalResources: []model.ExternalResource{{Note: strings.Repeat("x", portableExportMaxBytes)}}}}
	exporter, err := NewPortableExport(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exporter.Create(repository.WithUserID(context.Background(), "owner")); !errors.Is(err, ErrPortableExportLimit) {
		t.Fatalf("oversized export: %v", err)
	}
	store.err = repository.ErrPortableExportLimit
	if _, err := exporter.Create(repository.WithUserID(context.Background(), "owner")); !errors.Is(err, ErrPortableExportLimit) {
		t.Fatalf("row limit: %v", err)
	}
}
