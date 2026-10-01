import assert from 'node:assert/strict'
import test from 'node:test'
import { workspaceViewFromHash } from '../src/workspace-navigation.ts'

// 验证每个后台直达地址可以刷新恢复，旧专题地址仍然有效。
test('后台各分区可从地址片段恢复', () => {
  for (const view of ['library', 'search', 'inbox', 'organized', 'archive', 'external', 'topics']) {
    assert.equal(workspaceViewFromHash(`#${view}`), view)
  }
})

// 验证公开页面片段和未知输入不会打开错误的后台分区。
test('非后台片段默认进入文件资料', () => {
  for (const hash of ['', '#login', '#privacy', '#unexpected', '#topics/other']) {
    assert.equal(workspaceViewFromHash(hash), 'library')
  }
})
