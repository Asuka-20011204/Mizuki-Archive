package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
)

type inboxStoreFake struct {
	files     map[string]model.Resource
	externals map[string]model.ExternalResource
	owners    map[string]string
}

// ListPendingResources 只返回会话用户的待整理文件，模拟数据库范围过滤。
func (store *inboxStoreFake) ListPendingResources(ctx context.Context, _, _ int) ([]model.Resource, error) {
	owner, _ := repository.UserIDFromContext(ctx)
	result := []model.Resource{}
	for id, file := range store.files {
		if store.owners[id] == owner && file.OrganizationStatus == "pending" {
			result = append(result, file)
		}
	}
	return result, nil
}

// ListPendingExternalResources 只返回会话用户的待整理卡片，和链接状态独立。
func (store *inboxStoreFake) ListPendingExternalResources(ctx context.Context, _, _ int) ([]model.ExternalResource, error) {
	owner, _ := repository.UserIDFromContext(ctx)
	result := []model.ExternalResource{}
	for id, item := range store.externals {
		if store.owners[id] == owner && item.OrganizationStatus == "pending" {
			result = append(result, item)
		}
	}
	return result, nil
}

// SetResourceOrganizationStatus 拒绝跨账号修改文件整理状态。
func (store *inboxStoreFake) SetResourceOrganizationStatus(ctx context.Context, id, status string) error {
	owner, _ := repository.UserIDFromContext(ctx)
	file, ok := store.files[id]
	if !ok || store.owners[id] != owner {
		return repository.ErrNotFound
	}
	file.OrganizationStatus = status
	store.files[id] = file
	return nil
}

// SetExternalOrganizationStatus 拒绝跨账号修改外部卡片整理状态。
func (store *inboxStoreFake) SetExternalOrganizationStatus(ctx context.Context, id, status string) error {
	owner, _ := repository.UserIDFromContext(ctx)
	item, ok := store.externals[id]
	if !ok || store.owners[id] != owner {
		return repository.ErrNotFound
	}
	item.OrganizationStatus = status
	store.externals[id] = item
	return nil
}

// BatchSetOrganizationStatus 模拟全量归属校验后统一更新，防止部分条目先写入。
func (store *inboxStoreFake) BatchSetOrganizationStatus(ctx context.Context, items []model.InboxSelection, status string) error {
	owner, _ := repository.UserIDFromContext(ctx)
	for _, item := range items {
		switch item.Source {
		case "file":
			if _, exists := store.files[item.ID]; !exists || store.owners[item.ID] != owner {
				return repository.ErrNotFound
			}
		case "external":
			if _, exists := store.externals[item.ID]; !exists || store.owners[item.ID] != owner {
				return repository.ErrNotFound
			}
		}
	}
	for _, item := range items {
		if item.Source == "file" {
			file := store.files[item.ID]
			file.OrganizationStatus = status
			store.files[item.ID] = file
		} else {
			card := store.externals[item.ID]
			card.OrganizationStatus = status
			store.externals[item.ID] = card
		}
	}
	return nil
}

// TestInboxBatchAtomicAndValidated 验证混入他人资料拒绝整批、重复与超量输入均不写入。
func TestInboxBatchAtomicAndValidated(t *testing.T) {
	one, two, foreign := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32)
	store := &inboxStoreFake{
		files:     map[string]model.Resource{one: {ID: one, OrganizationStatus: "pending"}, foreign: {ID: foreign, OrganizationStatus: "pending"}},
		externals: map[string]model.ExternalResource{two: {ID: two, OrganizationStatus: "pending"}},
		owners:    map[string]string{one: "me", two: "me", foreign: "someone-else"},
	}
	inbox, err := NewInbox(store)
	if err != nil {
		t.Fatal(err)
	}
	ctx := repository.WithUserID(context.Background(), "me")
	valid := []model.InboxSelection{{Source: "file", ID: one}, {Source: "external", ID: two}}
	for _, selection := range [][]model.InboxSelection{
		nil,
		{{Source: "file", ID: one}, {Source: "file", ID: one}},
		{{Source: "file", ID: one}, {Source: "external", ID: "../bad"}},
		{{Source: "file", ID: one}, {Source: "unknown", ID: two}},
		append(make([]model.InboxSelection, 50), model.InboxSelection{Source: "file", ID: one}),
	} {
		if err := inbox.BatchSetStatus(ctx, selection, "organized"); !errors.Is(err, ErrInvalidInboxInput) {
			t.Fatalf("invalid selection: %v", err)
		}
	}
	if err := inbox.BatchSetStatus(context.Background(), valid, "organized"); !errors.Is(err, ErrInboxIdentity) {
		t.Fatalf("anonymous batch: %v", err)
	}
	if err := inbox.BatchSetStatus(ctx, append(valid, model.InboxSelection{Source: "file", ID: foreign}), "organized"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("foreign batch: %v", err)
	}
	if store.files[one].OrganizationStatus != "pending" || store.externals[two].OrganizationStatus != "pending" {
		t.Fatal("mixed-owner batch partially applied")
	}
	if err := inbox.BatchSetStatus(ctx, valid, "organized"); err != nil {
		t.Fatal(err)
	}
	if store.files[one].OrganizationStatus != "organized" || store.externals[two].OrganizationStatus != "organized" {
		t.Fatal("valid batch not applied")
	}
	if err := inbox.BatchSetStatus(ctx, valid, "pending"); err != nil {
		t.Fatal(err)
	}
}

// TestInboxUserIsolation 验证两类资料在一个收件箱显示、完成整理及跨用户拒绝。
func TestInboxUserIsolation(t *testing.T) {
	fileID := strings.Repeat("a", 32)
	externalID := strings.Repeat("b", 32)
	store := &inboxStoreFake{
		files:     map[string]model.Resource{fileID: {ID: fileID, Name: "upload.pdf", OrganizationStatus: "pending"}},
		externals: map[string]model.ExternalResource{externalID: {ID: externalID, Title: "remote", Status: "available", OrganizationStatus: "pending"}},
		owners:    map[string]string{fileID: "owner-b", externalID: "owner-b"},
	}
	inbox, err := NewInbox(store)
	if err != nil {
		t.Fatal(err)
	}
	ownerA := repository.WithUserID(context.Background(), "owner-a")
	ownerB := repository.WithUserID(context.Background(), "owner-b")
	if _, err := inbox.List(context.Background(), 1); !errors.Is(err, ErrInboxIdentity) {
		t.Fatalf("anonymous list: %v", err)
	}
	if result, err := inbox.List(ownerA, 1); err != nil || len(result.Files) != 0 || len(result.ExternalResources) != 0 {
		t.Fatalf("owner A list: %+v, %v", result, err)
	}
	if err := inbox.SetStatus(ownerA, "file", fileID, "organized"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("cross-user file update: %v", err)
	}
	if err := inbox.SetStatus(ownerA, "external", externalID, "organized"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("cross-user external update: %v", err)
	}
	if result, err := inbox.List(ownerB, 1); err != nil || len(result.Files) != 1 || len(result.ExternalResources) != 1 || result.ExternalResources[0].Status != "available" {
		t.Fatalf("owner B list: %+v, %v", result, err)
	}
	if err := inbox.SetStatus(ownerB, "file", fileID, "organized"); err != nil {
		t.Fatal(err)
	}
	if err := inbox.SetStatus(ownerB, "external", externalID, "organized"); err != nil {
		t.Fatal(err)
	}
	if result, err := inbox.List(ownerB, 1); err != nil || len(result.Files) != 0 || len(result.ExternalResources) != 0 {
		t.Fatalf("completed entries remain pending: %+v, %v", result, err)
	}
	if store.externals[externalID].Status != "available" {
		t.Fatal("organizing changed external link status")
	}
}

// TestInboxRejectsInvalidStatus 验证不接受未经会话确认的身份、非法来源和非法状态。
func TestInboxRejectsInvalidStatus(t *testing.T) {
	inbox, err := NewInbox(&inboxStoreFake{})
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 32)
	owner := repository.WithUserID(context.Background(), "owner")
	for _, item := range []struct{ source, id, status string }{
		{"file", id, "deleted"}, {"other", id, "pending"}, {"file", "../x", "pending"},
	} {
		if err := inbox.SetStatus(owner, item.source, item.id, item.status); err == nil {
			t.Fatalf("accepted invalid request: %+v", item)
		}
	}
	if err := inbox.SetStatus(context.Background(), "file", id, "organized"); !errors.Is(err, ErrInboxIdentity) {
		t.Fatalf("anonymous edit: %v", err)
	}
}
