import assert from 'node:assert/strict'
import test from 'node:test'
import { parseShareText, ShareParseError } from '../src/share-parser.ts'

// TestShareTextBaidu 验证中文网盘信息只预填表单，不丢失分享码。
test('网盘分享信息预填标题、位置、类型和提取码', () => {
  const result = parseShareText('文件名：Go 后端课\n链接：https://pan.baidu.com/s/1abc?pwd=a1b2\n提取码：a1b2')
  assert.deepEqual(result, { title: 'Go 后端课', location: 'https://pan.baidu.com/s/1abc?pwd=a1b2', resource_type: '网盘', note: '提取码：a1b2' })
})

// TestShareTextBareURL 验证单独链接仍可预填，尾部中文标点不会混进位置。
test('单独网址生成可编辑的默认标题', () => {
  const result = parseShareText('https://example.com/path?token=123。')
  assert.deepEqual(result, { title: 'example.com', location: 'https://example.com/path?token=123', resource_type: '网站', note: '' })
})

// TestShareTextInlineCode 验证中文逗号后的提取码不进入 URL。
test('同行提取码不拼进分享链接', () => {
  const result = parseShareText('https://pan.baidu.com/s/abc，提取码：abcd')
  assert.equal(result.location, 'https://pan.baidu.com/s/abc')
  assert.equal(result.note, '提取码：abcd')
})

// TestShareTextInlineTitle 验证常见单行分享格式的标题在下一个字段前截止。
test('单行分享信息的标题不包含链接和提取码', () => {
  const result = parseShareText('名称：课程资料；链接：https://pan.baidu.com/s/abc；提取码：abcd')
  assert.equal(result.title, '课程资料')
  assert.equal(result.location, 'https://pan.baidu.com/s/abc')
})

// TestShareTextOtherCloud 验证常见网盘域名只影响类型，不会向其发网络请求。
test('其他网盘域名识别为网盘类型', () => {
  assert.equal(parseShareText('名称：笔记\nhttps://www.aliyundrive.com/s/xyz').resource_type, '网盘')
})

// TestShareTextInvalid 验证无链接、危险协议和超长片段显式报错。
test('不接受无法预填的片段和超长链接', () => {
  for (const value of [' ', 'javascript:alert(1)', '名称：课程，但没有链接', `https://example.com/${'x'.repeat(2050)}`, `https://example.com/${'中'.repeat(700)}`, 'a'.repeat(8193)]) {
    assert.throws(() => parseShareText(value), ShareParseError)
  }
})

// TestShareTextCredentials 验证误贴的 URL 内嵌账号密码不会直接录入资源卡片。
test('拒绝网址中内嵌账号密码', () => {
  assert.throws(() => parseShareText('https://user:secret@example.com/file'), ShareParseError)
})

// TestShareTextBoundaries 验证无控制字符入库、标签类型和描述都在字段限制内。
test('清理标题中的控制字符和过长名称', () => {
  const result = parseShareText(`名称：${'资'.repeat(230)}\n链接：https://example.org/a`)
  assert.equal(result.title.length, 200)
  assert.equal(result.note, '')
})
