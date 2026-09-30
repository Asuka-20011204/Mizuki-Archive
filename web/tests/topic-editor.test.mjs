import assert from 'node:assert/strict'
import test from 'node:test'
import { moveItem, newTopicDraft, topicPayload } from '../src/topic-editor.ts'

// TestNewTopicDraft 确认新建模式不会沿用先前专题的编辑标识或资料引用。
test('从已打开专题切到新建时使用无 ID 的空草稿', () => {
  const previousId = 'existing-topic'
  const fresh = newTopicDraft()
  assert.equal(fresh.id, null)
  assert.notEqual(fresh.id, previousId)
  assert.deepEqual(fresh.input, { title: '', intro: '', cover_file_id: null, sections: [] })
})

// TestMoveItem 验证分区和条目移动不修改原数组，也不会越过边界。
test('编排顺序移动保持输入不可变', () => {
  const items = [{ id: 'a' }, { id: 'b' }, { id: 'c' }]
  assert.deepEqual(moveItem(items, 1, -1).map((item) => item.id), ['b', 'a', 'c'])
  assert.deepEqual(moveItem(items, 2, 1), items)
  assert.deepEqual(items.map((item) => item.id), ['a', 'b', 'c'])
})

// TestTopicPayload 验证只发送约定字段，详情名称和本地编排信息不会泄漏到写入请求。
test('专题提交只含标题、简介、封面和有序条目引用', () => {
  const draft = {
    id: 'private', title: '  游戏收藏  ', intro: '  给自己看的清单 ', cover_file_id: '',
    sections: [{ title: ' 第一章 ', items: [{ source: 'file', id: 'file-1', name: '原名', localKey: 3 }] }],
  }
  assert.deepEqual(topicPayload(draft), {
    title: '游戏收藏', intro: '给自己看的清单', cover_file_id: null,
    sections: [{ title: '第一章', items: [{ source: 'file', id: 'file-1' }] }],
  })
})
