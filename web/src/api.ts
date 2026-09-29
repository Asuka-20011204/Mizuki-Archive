// Resource 是 API 返回的资料元数据；原件由下载接口提供，存储路径不会暴露给浏览器。
export interface Resource {
  id: string
  name: string
  original_name: string
  kind: 'pdf' | 'image' | 'markdown' | 'text'
  mime: string
  size: number
  sha256: string
  favorite: boolean
  tags: string[]
  created_at: string
}

// DerivedAsset 描述成功任务生成的可下载派生文件，不把服务端存储键交给浏览器。
export interface DerivedAsset {
  id: string
  job_id: string
  resource_id: string
  kind: string
  name: string
  mime: string
  size: number
  sha256: string
  created_at: string
}

// ProcessingJob 描述详情面板需要展示的任务状态和失败摘要。
export interface ProcessingJob {
  id: string
  resource_id: string
  type: string
  source_sha256: string
  status: 'pending' | 'processing' | 'succeeded' | 'failed'
  attempts: number
  max_attempts: number
  available_at: string
  lease_until?: string
  last_error?: string
  started_at?: string
  finished_at?: string
  created_at: string
  asset?: DerivedAsset
}
// ApiError 只描述前端需要的安全错误消息，不依赖服务端内部异常细节。
interface ApiError {
  error?: { code: string; message: string }
}

// request 统一处理同源 Cookie、错误消息和无正文响应，页面不重复解析 HTTP 协议细节。
async function request<T>(path: string, options?: RequestInit): Promise<T> {
  // 与 Vite 代理或同源部署配合，浏览器自动携带 HttpOnly 会话 Cookie。
  const response = await fetch(`/api${path}`, { credentials: 'same-origin', ...options })
  if (!response.ok) {
    // JSON 解析失败回调返回空错误体；非 JSON 错误仍能用 HTTP 状态提示。
    const body = (await response.json().catch(() => ({}))) as ApiError
    throw new Error(body.error?.message || `请求失败（${response.status}）`)
  }
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}

// requestText 读取服务端纯文本预览，错误结构沿用 JSON API 的安全提示。
async function requestText(path: string, options?: RequestInit): Promise<string> {
  const response = await fetch(`/api${path}`, { credentials: 'same-origin', ...options })
  if (!response.ok) {
    const body = (await response.json().catch(() => ({}))) as ApiError
    throw new Error(body.error?.message || `请求失败（${response.status}）`)
  }
  return response.text()
}

export const api = {
  // me 由服务端会话确认当前身份，不读取浏览器可伪造的本地用户名。
  me: () => request<{ username: string }>('/me'),
  // login 使用 JSON 提交凭据；会话 Cookie 由浏览器按同源策略保存。
  login: (username: string, password: string) =>
    request<{ username: string }>('/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username, password }),
    }),
  // logout 请求服务端撤销会话，单纯隐藏页面不足以完成退出。
  logout: () => request<void>('/logout', { method: 'POST' }),
  // list 显式编码筛选和页码，避免文件名关键词破坏查询字符串。
  list: (search: string, kind: string, tag: string, page: number) =>
    request<{ data: Resource[]; meta: { page: number; has_more: boolean } }>(
      `/resources?${new URLSearchParams({ q: search, kind, tag, page: String(page) })}`,
    ),
  // listTags 只读取已有标签，用于建议和筛选，不把用户输入直接当作可信标签。
  listTags: (search = '') => request<{ data: string[] }>(`/tags?${new URLSearchParams({ q: search })}`),
  // get 对路径 ID 编码并读取最新元数据，供详情抽屉展示。
  get: (id: string) => request<{ data: Resource }>(`/resources/${encodeURIComponent(id)}`),
  // setName 只更新展示名称，原始上传名称和服务端存储键由后端保留。
  setName: (id: string, name: string) =>
    request<{ data: Resource }>(`/resources/${encodeURIComponent(id)}/name`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name }),
    }),
  // setFavorite 发送明确的目标状态，网络重试不会把已收藏资料意外取消。
  setFavorite: (id: string, favorite: boolean) =>
    request<{ data: Resource }>(`/resources/${encodeURIComponent(id)}/favorite`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ favorite }),
    }),
  // setTags 以完整数组替换标签，空数组表示清空，网络重试不会产生重复关联。
  setTags: (id: string, tags: string[]) =>
    request<{ data: Resource }>(`/resources/${encodeURIComponent(id)}/tags`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ tags }),
    }),
  // deleteResource 请求服务端软删除元数据并清理受控原件，不直接操作浏览器文件系统。
  deleteResource: (id: string) => request<void>(`/resources/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  // previewText 读取 TXT/Markdown 的安全纯文本内容，由 Vue 以文本节点渲染而不是执行 HTML。
  previewText: (id: string) => requestText(`/resources/${encodeURIComponent(id)}/preview`),
  // createTextJob 手动创建幂等的文本提取任务，不在上传时自动消耗处理资源。
  createTextJob: (id: string) =>
    request<{ data: ProcessingJob }>(`/resources/${encodeURIComponent(id)}/jobs`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ type: 'extract_text' }),
    }),
  // listJobs 读取资料最近的处理记录，用于详情面板展示状态和派生产物。
  listJobs: (id: string) => request<{ data: ProcessingJob[] }>(`/resources/${encodeURIComponent(id)}/jobs`),
  // getJob 读取单个任务的最新状态，支持前端轮询而不重新加载整份资料。
  getJob: (id: string) => request<{ data: ProcessingJob }>(`/jobs/${encodeURIComponent(id)}`),
  // derivedDownloadURL 生成同源下载地址，权限仍由服务端会话和派生产物 ID 控制。
  derivedDownloadURL: (id: string) => `/api/derived-assets/${encodeURIComponent(id)}/download`,
  // upload 用 FormData 交给浏览器设置 multipart 边界，不能手写 Content-Type。
  upload: (file: File) => {
    const body = new FormData()
    body.append('file', file)
    return request<{ data: Resource }>('/resources', { method: 'POST', body })
  },
}
