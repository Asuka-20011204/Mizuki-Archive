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

interface ApiError {
  error?: { code: string; message: string }
}

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  // 与 Vite 代理或同源部署配合，浏览器自动携带 HttpOnly 会话 Cookie。
  const response = await fetch(`/api${path}`, { credentials: 'same-origin', ...options })
  if (!response.ok) {
    const body = (await response.json().catch(() => ({}))) as ApiError
    throw new Error(body.error?.message || `请求失败（${response.status}）`)
  }
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}

export const api = {
  me: () => request<{ username: string }>('/me'),
  login: (username: string, password: string) =>
    request<{ username: string }>('/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username, password }),
    }),
  logout: () => request<void>('/logout', { method: 'POST' }),
  list: (search: string, kind: string, page: number) =>
    request<{ data: Resource[]; meta: { page: number; has_more: boolean } }>(
      `/resources?${new URLSearchParams({ q: search, kind, page: String(page) })}`,
    ),
  get: (id: string) => request<{ data: Resource }>(`/resources/${encodeURIComponent(id)}`),
  upload: (file: File) => {
    const body = new FormData()
    body.append('file', file)
    return request<{ data: Resource }>('/resources', { method: 'POST', body })
  },
}
