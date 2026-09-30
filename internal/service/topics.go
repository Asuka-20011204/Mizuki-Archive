package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"mizuki-archive/internal/model"
	"mizuki-archive/internal/repository"
)

var ErrInvalidTopic = errors.New("invalid topic")
var ErrTopicIdentity = errors.New("missing topic identity")

const maxTopicSections = 10
const maxTopicItems = 100

// Topics 管理账号私有的专题编排，不负责公开分享或访问外部位置。
type Topics struct{ store repository.TopicStore }

// NewTopics 检查持久化依赖，允许以替身验证业务负例。
func NewTopics(store repository.TopicStore) (*Topics, error) {
	if store == nil {
		return nil, errors.New("missing topic store")
	}
	return &Topics{store: store}, nil
}

// topicIdentity 拒绝没有服务端会话身份的直接服务调用。
func topicIdentity(ctx context.Context) error {
	if _, ok := repository.UserIDFromContext(ctx); !ok {
		return ErrTopicIdentity
	}
	return nil
}

// topicName 统一校验文本长度、控制字符及前后空白。
func topicName(value string, limit int, required bool) (string, error) {
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", ErrInvalidTopic
	}
	value = strings.TrimSpace(value)
	if (required && value == "") || utf8.RuneCountInString(value) > limit {
		return "", ErrInvalidTopic
	}
	return value, nil
}

// normalizeTopic 校验上限、ID 格式和跨分区重复引用，并复制输入避免隐式修改调用方。
func normalizeTopic(input model.TopicInput) (model.TopicInput, error) {
	var err error
	input.Title, err = topicName(input.Title, 200, true)
	if err != nil {
		return model.TopicInput{}, err
	}
	input.Intro, err = topicName(input.Intro, 2000, false)
	if err != nil {
		return model.TopicInput{}, err
	}
	if input.CoverFileID != nil && !validResourceID(*input.CoverFileID) {
		return model.TopicInput{}, ErrInvalidTopic
	}
	if len(input.Sections) > maxTopicSections {
		return model.TopicInput{}, ErrInvalidTopic
	}
	result := model.TopicInput{Title: input.Title, Intro: input.Intro, CoverFileID: input.CoverFileID, Sections: make([]model.TopicSection, 0, len(input.Sections))}
	seen := make(map[model.InboxSelection]bool)
	count := 0
	for _, section := range input.Sections {
		name, err := topicName(section.Title, 200, true)
		if err != nil {
			return model.TopicInput{}, err
		}
		copySection := model.TopicSection{Title: name, Items: make([]model.TopicItem, 0, len(section.Items))}
		for _, item := range section.Items {
			selection := model.InboxSelection{Source: item.Source, ID: item.ID}
			if (item.Source != "file" && item.Source != "external") || !validResourceID(item.ID) || seen[selection] || item.Name != "" {
				return model.TopicInput{}, ErrInvalidTopic
			}
			seen[selection] = true
			count++
			if count > maxTopicItems {
				return model.TopicInput{}, ErrInvalidTopic
			}
			copySection.Items = append(copySection.Items, model.TopicItem{Source: item.Source, ID: item.ID})
		}
		result.Sections = append(result.Sections, copySection)
	}
	return result, nil
}

// Create 创建随机标识的专题，端点归属在仓储事务中重新检查。
func (topics *Topics) Create(ctx context.Context, input model.TopicInput) (model.TopicDetail, error) {
	if err := topicIdentity(ctx); err != nil {
		return model.TopicDetail{}, err
	}
	input, err := normalizeTopic(input)
	if err != nil {
		return model.TopicDetail{}, err
	}
	identifier := make([]byte, 16)
	if _, err := rand.Read(identifier); err != nil {
		return model.TopicDetail{}, err
	}
	return topics.store.SaveTopic(ctx, hex.EncodeToString(identifier), input, false)
}

// Update 整体替换本人专题，非法标识与不存在统一视作未找到。
func (topics *Topics) Update(ctx context.Context, id string, input model.TopicInput) (model.TopicDetail, error) {
	if err := topicIdentity(ctx); err != nil {
		return model.TopicDetail{}, err
	}
	if !validResourceID(id) {
		return model.TopicDetail{}, repository.ErrNotFound
	}
	input, err := normalizeTopic(input)
	if err != nil {
		return model.TopicDetail{}, err
	}
	return topics.store.SaveTopic(ctx, id, input, true)
}

// List 仅列出当前会话用户的私有专题。
func (topics *Topics) List(ctx context.Context) ([]model.TopicSummary, error) {
	if err := topicIdentity(ctx); err != nil {
		return nil, err
	}
	return topics.store.ListTopics(ctx)
}

// Get 仅返回本人专题与当前可见条目的名称。
func (topics *Topics) Get(ctx context.Context, id string) (model.TopicDetail, error) {
	if err := topicIdentity(ctx); err != nil {
		return model.TopicDetail{}, err
	}
	if !validResourceID(id) {
		return model.TopicDetail{}, repository.ErrNotFound
	}
	return topics.store.GetTopic(ctx, id)
}

// Delete 删除本人专题及有序内容，不影响所引用的资料。
func (topics *Topics) Delete(ctx context.Context, id string) error {
	if err := topicIdentity(ctx); err != nil {
		return err
	}
	if !validResourceID(id) {
		return repository.ErrNotFound
	}
	return topics.store.DeleteTopic(ctx, id)
}
