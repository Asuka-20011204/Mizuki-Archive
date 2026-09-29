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
  created_at: string
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
  list: (search: string, kind: string, page: number) =>
    request<{ data: Resource[]; meta: { page: number; has_more: boolean } }>(
      `/resources?${new URLSearchParams({ q: search, kind, page: String(page) })}`,
    ),
  // get 对路径 ID 编码并读取最新元数据，供详情抽屉展示。
  get: (id: string) => request<{ data: Resource }>(`/resources/${encodeURIComponent(id)}`),
  // setFavorite 发送明确的目标状态，网络重试不会把已收藏资料意外取消。
  setFavorite: (id: string, favorite: boolean) =>
    request<{ data: Resource }>(`/resources/${encodeURIComponent(id)}/favorite`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ favorite }),
    }),
  // upload 用 FormData 交给浏览器设置 multipart 边界，不能手写 Content-Type。
  upload: (file: File) => {
    const body = new FormData()
    body.append('file', file)
    return request<{ data: Resource }>('/resources', { method: 'POST', body })
  },
}
