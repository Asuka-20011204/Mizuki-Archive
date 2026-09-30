import assert from 'node:assert/strict'
import test from 'node:test'
import { normalizeSearchInput } from '../src/search-filter.ts'

const empty = { q: '', source: '', kind: '', tag: '', organization_status: '' }

// TestFilterOnlyStatus 验证不用关键词也可以按整理状态保存和检索。
test('整理状态可独立作为筛选条件，来源本身不能列出全部资料', () => {
  assert.deepEqual(normalizeSearchInput('', { ...empty, source: 'external', organization_status: 'pending' }),
    { ...empty, source: 'external', organization_status: 'pending' })
  assert.equal(normalizeSearchInput('', { ...empty, source: 'file' }), null)
})

// TestFilterTrimming 验证关键词、类型和标签会修剪两侧空格并保留组合。
test('组合条件修剪空格且来源保留', () => {
  assert.deepEqual(normalizeSearchInput(' 课程 ', { ...empty, source: 'file', kind: ' pdf ', tag: ' 学习 ' }),
    { ...empty, q: '课程', source: 'file', kind: 'pdf', tag: '学习' })
})

// TestFilterInvalidInput 验证控制字符和过长输入在发请求前被拒绝。
test('拒绝换行和超长的检索条件', () => {
  assert.equal(normalizeSearchInput('课程\n', empty), null)
  assert.equal(normalizeSearchInput('课程', { ...empty, tag: 'a'.repeat(41) }), null)
  assert.equal(normalizeSearchInput('a'.repeat(101), empty), null)
})
