import assert from 'node:assert/strict'
import test from 'node:test'
import { loadTopicCandidates } from '../src/topic-picker.ts'

// TestEmptyPickerQuery 验证空关键词不调用拒绝仅来源条件的统一搜索。
test('空关键词按来源使用普通列表，外部卡片明确不分页', async () => {
  const calls = []
  const client = {
    list: async (...args) => { calls.push(['list', ...args]); return { data: [{ id: 'f' }], meta: { has_more: true } } },
    listExternalResources: async (...args) => { calls.push(['external', ...args]); return { data: [{ id: 'e' }] } },
    searchAll: async () => { throw new Error('空关键词不可使用统一搜索') },
  }
  const files = await loadTopicCandidates(client, 'item', 'file', '  ', 2)
  const cards = await loadTopicCandidates(client, 'item', 'external', '', 1)
  assert.deepEqual(calls, [['list', '', '', '', 2], ['external', '']])
  assert.equal(files.hasMore, true)
  assert.equal(cards.limitedExternal, true)
  assert.equal(cards.hasMore, false)
})

// TestPickerSearch 验证非空关键词走统一搜索，封面独立走仅图片的文件列表。
test('搜索按来源分页，封面只接受图片结果', async () => {
  const calls = []
  const client = {
    list: async (...args) => { calls.push(['list', ...args]); return { data: [{ kind: 'image' }, { kind: 'pdf' }], meta: { has_more: false } } },
    listExternalResources: async () => { throw new Error('非空词不走受限卡片列表') },
    searchAll: async (filter, page) => {
      calls.push(['search', filter, page])
      return { data: { files: [], external_resources: [{ id: 'e' }], has_more_files: false, has_more_external: true } }
    },
  }
  const cards = await loadTopicCandidates(client, 'item', 'external', '  手册 ', 3)
  const cover = await loadTopicCandidates(client, 'cover', 'file', '', 1)
  assert.equal(cards.hasMore, true)
  assert.equal(cover.files.length, 1)
  assert.deepEqual(calls, [
    ['search', { q: '手册', source: 'external', kind: '', tag: '', organization_status: '' }, 3],
    ['list', '', 'image', '', 1],
  ])
})
