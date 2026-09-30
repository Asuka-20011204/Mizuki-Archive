import type { api, ExternalResource, Resource } from './api'

// TopicCandidates 把分页文件、有限卡片列表和统一搜索结果归一成选择器显示数据。
export interface TopicCandidates {
  files: Resource[]
  cards: ExternalResource[]
  hasMore: boolean
  limitedExternal: boolean
}

// loadTopicCandidates 避免空关键词触发统一搜索的仅来源过滤 400；封面始终只取图片。
export async function loadTopicCandidates(
  client: Pick<typeof api, 'list' | 'listExternalResources' | 'searchAll'>,
  mode: 'cover' | 'item',
  source: 'file' | 'external',
  query: string,
  page: number,
): Promise<TopicCandidates> {
  const keyword = query.trim()
  if (mode === 'cover') {
    const result = await client.list(keyword, 'image', '', page)
    return { files: result.data.filter((file) => file.kind === 'image'), cards: [], hasMore: result.meta.has_more, limitedExternal: false }
  }
  if (!keyword && source === 'file') {
    const result = await client.list('', '', '', page)
    return { files: result.data, cards: [], hasMore: result.meta.has_more, limitedExternal: false }
  }
  if (!keyword) {
    const result = await client.listExternalResources('')
    return { files: [], cards: result.data, hasMore: false, limitedExternal: true }
  }
  const result = await client.searchAll({ q: keyword, source, kind: '', tag: '', organization_status: '' }, page)
  return {
    files: result.data.files,
    cards: result.data.external_resources,
    hasMore: source === 'file' ? result.data.has_more_files : result.data.has_more_external,
    limitedExternal: false,
  }
}
