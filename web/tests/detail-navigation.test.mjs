import assert from 'node:assert/strict'
import test from 'node:test'
import { adjacentResource } from '../src/detail-navigation.ts'

const page = [{ id: 'first' }, { id: 'second' }, { id: 'third' }]

// TestAdjacentResource 验证只在当前列表页切换，边界不跨页也不循环。
test('上一项和下一项只在打开详情的当前页内切换', () => {
  assert.deepEqual(adjacentResource(page, 'second', -1), page[0])
  assert.deepEqual(adjacentResource(page, 'second', 1), page[2])
  assert.equal(adjacentResource(page, 'first', -1), null)
  assert.equal(adjacentResource(page, 'third', 1), null)
})

// TestMissingResource 验证外部刷新或单项详情不在页内时不跳向无关资料。
test('当前资料不属于这页时不猜测相邻文件', () => {
  assert.equal(adjacentResource(page, 'missing', 1), null)
  assert.equal(adjacentResource([], 'first', -1), null)
})
