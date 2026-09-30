package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
)

// topicFakeStore 记录有效请求是否抵达持久层。
type topicFakeStore struct{ saves int }

// SaveTopic 模拟成功保存供服务边界测试使用。
func (store *topicFakeStore) SaveTopic(_ context.Context, id string, input model.TopicInput, _ bool) (model.TopicDetail, error) {
	store.saves++
	return model.TopicDetail{TopicSummary: model.TopicSummary{ID: id, Title: input.Title}, Sections: input.Sections}, nil
}

// ListTopics 为服务边界返回空列表。
func (store *topicFakeStore) ListTopics(context.Context) ([]model.TopicSummary, error) {
	return []model.TopicSummary{}, nil
}

// GetTopic 模拟未知专题。
func (store *topicFakeStore) GetTopic(context.Context, string) (model.TopicDetail, error) {
	return model.TopicDetail{}, repository.ErrNotFound
}

// DeleteTopic 模拟未知专题。
func (store *topicFakeStore) DeleteTopic(context.Context, string) error {
	return repository.ErrNotFound
}

// TestTopicsValidation 确认缺身份、长度/数量限制及跨分区重复引用在持久化前被拒绝。
func TestTopicsValidation(t *testing.T) {
	store := &topicFakeStore{}
	topics, err := NewTopics(store)
	if err != nil {
		t.Fatal(err)
	}
	file := model.TopicItem{Source: "file", ID: strings.Repeat("a", 32)}
	valid := model.TopicInput{Title: "游戏收藏", Sections: []model.TopicSection{{Title: "攻略", Items: []model.TopicItem{file}}}}
	if _, err := topics.Create(context.Background(), valid); !errors.Is(err, ErrTopicIdentity) {
		t.Fatalf("anonymous: %v", err)
	}
	ctx := repository.WithUserID(context.Background(), "owner")
	invalid := []model.TopicInput{
		{Title: " "}, {Title: strings.Repeat("a", 201)}, {Title: "a\n"},
		{Title: "ok", Intro: strings.Repeat("a", 2001)},
		{Title: "ok", Sections: make([]model.TopicSection, 11)},
		{Title: "ok", Sections: []model.TopicSection{{Title: " "}}},
		{Title: "ok", Sections: []model.TopicSection{{Title: strings.Repeat("分", 201)}}},
		{Title: "ok", CoverFileID: new(string)},
		{Title: "ok", Sections: []model.TopicSection{{Title: "one", Items: []model.TopicItem{file}}, {Title: "two", Items: []model.TopicItem{file}}}},
		{Title: "ok", Sections: []model.TopicSection{{Title: "one", Items: []model.TopicItem{{Source: "file", ID: file.ID, Name: "伪造名称"}}}}},
		{Title: "ok", Sections: []model.TopicSection{{Title: "one", Items: []model.TopicItem{{Source: "external", ID: "bad"}}}}},
	}
	items := make([]model.TopicItem, 101)
	for index := range items {
		items[index] = model.TopicItem{Source: "file", ID: strings.Repeat("a", 30) + string("0123456789abcdef"[index/16]) + string("0123456789abcdef"[index%16])}
	}
	invalid = append(invalid, model.TopicInput{Title: "ok", Sections: []model.TopicSection{{Title: "one", Items: items}}})
	for index, input := range invalid {
		if _, err := topics.Create(ctx, input); !errors.Is(err, ErrInvalidTopic) {
			t.Fatalf("invalid %d: %v", index, err)
		}
	}
	if store.saves != 0 {
		t.Fatalf("invalid input reached store: %d", store.saves)
	}
	if _, err := topics.Create(ctx, valid); err != nil || store.saves != 1 {
		t.Fatalf("valid input: %v", err)
	}
	if _, err := topics.Get(ctx, "bad"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("invalid id: %v", err)
	}
}
