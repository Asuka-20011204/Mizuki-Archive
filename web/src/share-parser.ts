// SharePrefill 是可编辑的预填结果，不包含保存动作或服务器请求。
export interface SharePrefill {
  title: string
  location: string
  resource_type: string
  note: string
}

// ShareParseError 区分无法解析的粘贴文本和卡片保存接口返回的失败。
export class ShareParseError extends Error {}

// cleanShareURL 清除自然语言尾部标点，保留 URL 内部的查询参数和分享码。
function cleanShareURL(value: string): URL {
  const candidate = value.replace(/[，。；、！？,.!?;）)\]】]+$/u, '')
  let parsed: URL
  try {
    parsed = new URL(candidate)
  } catch {
    throw new ShareParseError('未识别到有效的分享链接')
  }
  if (!['https:', 'http:'].includes(parsed.protocol) || !parsed.hostname || parsed.username || parsed.password || parsed.href.length > 2048) {
    throw new ShareParseError('只能收录不含登录凭据的 HTTP 或 HTTPS 链接')
  }
  return parsed
}

// isCloudDomain 仅根据链接主机名预填类型，绝不连接或校验外部服务。
function isCloudDomain(hostname: string): boolean {
  const domains = ['pan.baidu.com', 'aliyundrive.com', 'alipan.com', 'pan.quark.cn', '123pan.com', 'drive.google.com']
  return domains.some((domain) => hostname === domain || hostname.endsWith(`.${domain}`))
}

// parseShareText 在浏览器内从粘贴文本提取首个链接、标题和提取码；用户确认后才允许保存。
export function parseShareText(text: string): SharePrefill {
  if (text.trim().length === 0 || text.length > 8192) {
    throw new ShareParseError('请粘贴不超过 8192 个字符的分享链接或网盘信息')
  }
  const link = text.match(/https?:\/\/[^\s<>"'\u0000-\u001f，。；）】]+/iu)
  if (!link) {
    throw new ShareParseError('未识别到 HTTP 或 HTTPS 分享链接，请核对后重试')
  }
  const parsed = cleanShareURL(link[0])
  const title = text.match(/(?:^|[\r\n])\s*(?:文件名|名称|标题)\s*[：:]\s*([^\r\n；;]+)/u)?.[1]?.trim()
  const code = text.match(/(?:提取码|访问码)\s*[：:]\s*([a-zA-Z0-9]{4,16})/u)?.[1]
  const cleanedTitle = (title || parsed.hostname).replace(/[\u0000-\u001f\u007f]/gu, ' ').trim()
  return {
    title: Array.from(cleanedTitle).slice(0, 200).join(''),
    location: parsed.href,
    resource_type: isCloudDomain(parsed.hostname) ? '网盘' : '网站',
    note: code ? `提取码：${code}` : '',
  }
}
