import type { SearchFilter } from './api'

// normalizeSearchInput 在浏览器侧阻止空条件、控制字符和超长输入；服务端仍是最终权限与校验边界。
export function normalizeSearchInput(query: string, filter: SearchFilter): SearchFilter | null {
  const value = { ...filter, q: query.trim(), kind: filter.kind.trim(), tag: filter.tag.trim() }
  if ((!value.q && !value.kind && !value.tag && !value.organization_status) ||
    [...value.q].length > 100 || [...value.kind].length > 40 || [...value.tag].length > 40 ||
    /\p{Cc}/u.test(`${query}${filter.kind}${filter.tag}`)) return null
  return value
}
