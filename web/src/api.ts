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
  organization_status: 'pending' | 'organized'
  tags: string[]
  created_at: string
}

// ExternalResource 只描述站外位置卡片；服务器不保管或下载站外文件。
export interface ExternalResource {
  id: string
  title: string
  location: string
  resource_type: string
  version: string
  note: string
  status: 'pending' | 'available' | 'uncertain' | 'broken' | 'downloaded'
  organization_status: 'pending' | 'organized'
  tags: string[]
  created_at: string
  updated_at: string
}

// ExternalResourceInput 只提交可编辑字段，身份、ID 和时间由服务端决定。
export type ExternalResourceInput = Pick<ExternalResource, 'title' | 'location' | 'resource_type' | 'version' | 'note' | 'status' | 'tags'>

// InboxPage 将文件和外部卡片区分展示，页码是两类来源各自的窗口。
export interface InboxPage {
  files: Resource[]
  external_resources: ExternalResource[]
  page: number
  has_more: boolean
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

// AuthCapabilities 描述服务端当前公开的登录能力，避免前端展示未配置完成的注册入口。
export interface AuthCapabilities {
  email_verification: boolean
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
  // listInbox 读取当前用户的两类待整理资料，避免浏览器在本地拼装越权列表。
  listInbox: (page: number) => request<{ data: InboxPage }>(`/inbox?${new URLSearchParams({ page: String(page) })}`),
  // setInboxStatus 显式指定目标状态，重复提交不会反转状态或触碰外部链接可用性。
  setInboxStatus: (source: 'file' | 'external', id: string, status: 'pending' | 'organized') =>
    request<void>(`/inbox/${source}/${encodeURIComponent(id)}`, {
      method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ status }),
    }),
  // listExternalResources 只查询当前用户卡片，服务器限制返回数量。
  listExternalResources: (query = '') => request<{ data: ExternalResource[] }>(`/external-resources?${new URLSearchParams({ q: query })}`),
  // createExternalResource 保存资源位置文本，绝不上传或自动访问外部链接。
  createExternalResource: (value: ExternalResourceInput) => request<{ data: ExternalResource }>('/external-resources', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(value),
  }),
  // updateExternalResource 使用完整字段替换卡片，标签与卡片在同一事务变更。
  updateExternalResource: (id: string, value: ExternalResourceInput) => request<{ data: ExternalResource }>(`/external-resources/${encodeURIComponent(id)}`, {
    method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(value),
  }),
  // deleteExternalResource 仅删除本站卡片，不触碰站外原件。
  deleteExternalResource: (id: string) => request<void>(`/external-resources/${encodeURIComponent(id)}`, { method: 'DELETE' }),
  // authCapabilities 读取运行时认证能力；能力开关由服务端配置决定，浏览器不自行猜测。
  authCapabilities: () => request<AuthCapabilities>('/auth/capabilities'),
  // me 由服务端会话确认当前身份，不读取浏览器可伪造的本地用户名。
  me: () => request<{ username: string }>('/me'),
  // login 使用 JSON 提交凭据；会话 Cookie 由浏览器按同源策略保存。
  login: (username: string, password: string) =>
    request<{ username: string }>('/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username, password }),
    }),
  // requestEmailRegistrationCode 只请求验证码，不把邮箱是否存在的判断交给浏览器。
  requestEmailRegistrationCode: (email: string) =>
    request<{ message: string }>('/email/register/request', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ email }),
    }),
  // registerWithEmailCode 完成一次性验证码注册并接收服务端会话 Cookie。
  registerWithEmailCode: (email: string, code: string) =>
    request<{ username: string }>('/email/register', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ email, code }),
    }),
  // requestEmailLoginCode 向已注册邮箱请求短时验证码。
  requestEmailLoginCode: (email: string) =>
    request<{ message: string }>('/email/login/request', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ email }),
    }),
  // loginWithEmailCode 用一次性验证码建立与密码登录相同的服务端会话。
  loginWithEmailCode: (email: string, code: string) =>
    request<{ username: string }>('/email/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ email, code }),
    }),
  // logout 请求服务端撤销会话，单纯隐藏页面不足以完成退出。
  logout: () => request<void>('/logout', { method: 'POST' }),
  // list 显式编码筛选和页码，避免文件名关键词破坏查询字符串。
  list: (search: string, kind: string, tag: string, page: number) =>
    request<{ data: Resource[]; meta: { page: number; has_more: boolean } }>(
      `/resources?${new URLSearchParams({ q: search, kind, tag, page: String(page) })}`,
    ),
  // recent 读取最近访问的资料元数据；缓存缺失时服务端返回空数组，不影响主列表。
  recent: () => request<{ data: Resource[] }>('/resources/recent'),
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
  // createThumbnailJob 手动创建幂等的图片缩略图任务，原图不会被覆盖。
  createThumbnailJob: (id: string) =>
    request<{ data: ProcessingJob }>(`/resources/${encodeURIComponent(id)}/jobs`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ type: 'generate_thumbnail' }),
    }),
  // listJobs 读取资料最近的处理记录，用于详情面板展示状态和派生产物。
  listJobs: (id: string) => request<{ data: ProcessingJob[] }>(`/resources/${encodeURIComponent(id)}/jobs`),
  // getJob 读取单个任务的最新状态，支持前端轮询而不重新加载整份资料。
  getJob: (id: string) => request<{ data: ProcessingJob }>(`/jobs/${encodeURIComponent(id)}`),
  // derivedDownloadURL 生成同源下载地址，权限仍由服务端会话和派生产物 ID 控制。
  derivedDownloadURL: (id: string) => `/api/derived-assets/${encodeURIComponent(id)}/download`,
  // derivedPreviewURL 生成同源缩略图预览地址，服务端仍会校验会话和派生产物权限。
  derivedPreviewURL: (id: string) => `/api/derived-assets/${encodeURIComponent(id)}/preview`,
  // upload 用 FormData 交给浏览器设置 multipart 边界，不能手写 Content-Type。
  upload: (file: File) => {
    const body = new FormData()
    body.append('file', file)
    return request<{ data: Resource }>('/resources', { method: 'POST', body })
  },
}
