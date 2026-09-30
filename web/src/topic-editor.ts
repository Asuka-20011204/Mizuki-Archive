import type { TopicInput } from './api'

// newTopicDraft 显式同时重置编辑 ID 与内容，避免从已打开专题进入新建时误发更新请求。
export function newTopicDraft(): { id: null; input: TopicInput } {
  return { id: null, input: { title: '', intro: '', cover_file_id: null, sections: [] } }
}

// moveItem 为分区和条目提供同一套键盘按钮排序逻辑，边界外操作保持原顺序。
export function moveItem<Item>(items: readonly Item[], index: number, step: -1 | 1): Item[] {
  if (index < 0 || index >= items.length || index + step < 0 || index + step >= items.length) return [...items]
  const result = [...items]
  ;[result[index], result[index + step]] = [result[index + step], result[index]]
  return result
}

// topicPayload 将可编辑草稿压缩为私有 API 的完整写入契约，不发送服务端名称或本地标识。
export function topicPayload(draft: TopicInput): TopicInput {
  return {
    title: draft.title.trim(),
    intro: draft.intro.trim(),
    cover_file_id: draft.cover_file_id || null,
    sections: draft.sections.map((section) => ({
      title: section.title.trim(),
      items: section.items.map((item) => ({ source: item.source, id: item.id })),
    })),
  }
}
