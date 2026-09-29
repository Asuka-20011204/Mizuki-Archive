<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { api, type Resource } from './api'

// 登录状态、列表筛选和详情面板分别在本视图中管理；服务端始终是权限与资料的权威来源。
const username = ref('')
const loginName = ref('')
const password = ref('')
// 各操作分别记录忙碌状态：上传不应让搜索和退出按钮无故禁用。
const loadingSession = ref(true)
const loggingIn = ref(false)
const uploading = ref(false)
const searching = ref(false)
const error = ref('')
const notice = ref('')
const resources = ref<Resource[]>([])
const selected = ref<Resource | null>(null)
// 搜索条件与页码交给 API 查询，不在浏览器里模拟 MySQL 的筛选和分页。
const search = ref('')
const kind = ref('')
const page = ref(1)
const hasMore = ref(false)
const fileInput = ref<HTMLInputElement | null>(null)
const detailPanel = ref<HTMLElement | null>(null)
const closeButton = ref<HTMLButtonElement | null>(null)
let previousFocus: HTMLElement | null = null
// 并发列表请求使用单调序号，防止旧筛选的响应覆盖新筛选。
let listRequestId = 0

const kinds = [
  { value: '', label: '全部资料', short: '全部' },
  { value: 'pdf', label: 'PDF 文档', short: 'PDF' },
  { value: 'image', label: '图片素材', short: '图片' },
  { value: 'markdown', label: 'Markdown', short: 'Markdown' },
  { value: 'text', label: '文本记录', short: '文本' },
]

const sectionName = computed(() => kinds.find((item) => item.value === kind.value)?.label || '全部资料')

function formatSize(size: number) {
  if (size < 1024 * 1024) {
    return `${Math.max(1, Math.round(size / 1024))} KB`
  }
  return `${(size / 1024 / 1024).toFixed(1)} MB`
}

function formatDate(date: string) {
  return new Intl.DateTimeFormat('zh-CN', { year: 'numeric', month: 'short', day: 'numeric' }).format(new Date(date))
}

function kindLabel(value: Resource['kind']) {
  return kinds.find((item) => item.value === value)?.short || value
}

async function loadResources() {
  // 搜索、类型和页码统一走同一个列表请求，页面不在客户端伪造筛选结果。
  // 快速切换筛选条件时，旧请求可能比新请求晚返回；只有最后一次请求能更新列表。
  if (!username.value) return
  const requestId = ++listRequestId
  searching.value = true
  error.value = ''
  try {
    const result = await api.list(search.value, kind.value, page.value)
    if (requestId !== listRequestId) return
    resources.value = result.data
    hasMore.value = result.meta.has_more
    if (selected.value && !resources.value.some((item) => item.id === selected.value?.id)) {
      selected.value = null
    }
  } catch (reason) {
    if (requestId === listRequestId) {
      error.value = reason instanceof Error ? reason.message : '无法加载资料'
    }
  } finally {
    if (requestId === listRequestId) searching.value = false
  }
}

async function checkSession() {
  // 页面刷新后先确认 HttpOnly Cookie 是否仍有效；不能从本地存储推断登录身份。
  try {
    username.value = (await api.me()).username
  } catch {
    // 未登录时展示登录页；资料列表错误由列表请求自己报告，不能误当成会话失效。
    username.value = ''
  } finally {
    if (username.value) await loadResources()
    loadingSession.value = false
  }
}

async function login() {
  // 只在登录成功后写入界面身份，密码成功后从组件状态移除。
  loggingIn.value = true
  error.value = ''
  try {
    username.value = (await api.login(loginName.value, password.value)).username
    password.value = ''
    await loadResources()
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '登录失败'
  } finally {
    loggingIn.value = false
  }
}

async function logout() {
  // 以服务端撤销会话为准；若请求失败则保留当前界面并提示用户重试。
  try {
    await api.logout()
    // 退出后使所有尚未返回的列表请求失效，避免私有资料重新出现在页面上。
    listRequestId++
    searching.value = false
    username.value = ''
    selected.value = null
    resources.value = []
    notice.value = ''
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '退出失败'
  }
}

async function upload(event: Event) {
  // 前端限制用于及时反馈；实际大小和类型仍以 Go Service 校验为准。
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file) return
  error.value = ''
  notice.value = ''
  if (file.size > 50 * 1024 * 1024) {
    error.value = '单个文件不能超过 50 MB'
    input.value = ''
    return
  }
  uploading.value = true
  try {
    const result = await api.upload(file)
    search.value = ''
    kind.value = ''
    page.value = 1
    await loadResources()
    selected.value = result.data
    notice.value = '资料已安全存入资料库'
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '上传失败'
  } finally {
    input.value = ''
    uploading.value = false
  }
}

async function selectResource(resource: Resource) {
  // 详情重新请求服务端，避免列表快照被误当作最新的资料记录。
  error.value = ''
  try {
    selected.value = (await api.get(resource.id)).data
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '无法打开资料'
  }
}

function chooseKind(value: string) {
  kind.value = value
  page.value = 1
}

function trapDetailFocus(event: KeyboardEvent) {
  // 详情抽屉作为模态层，键盘焦点不能落到背后的资料列表。
  if (event.key === 'Escape') {
    selected.value = null
    return
  }
  if (event.key !== 'Tab') return
  const focusable = detailPanel.value?.querySelectorAll<HTMLElement>('button:not([disabled]), a[href]')
  if (!focusable?.length) return
  if (event.shiftKey && document.activeElement === focusable[0]) {
    event.preventDefault()
    focusable[focusable.length - 1]?.focus()
  } else if (!event.shiftKey && document.activeElement === focusable[focusable.length - 1]) {
    event.preventDefault()
    focusable[0]?.focus()
  }
}

watch(selected, async (current) => {
  // 模态抽屉打开时移动焦点，关闭时还给原触发控件，方便键盘用户继续浏览。
  if (current) {
    previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null
    await nextTick()
    closeButton.value?.focus()
  } else {
    previousFocus?.focus()
    previousFocus = null
  }
})

watch([search, kind, page], (_current, _previous, onCleanup) => {
  if (!username.value) return
  // 筛选变化立即让旧响应失效；输入停止后再请求，避免短暂显示不匹配的资料。
  listRequestId++
  resources.value = []
  hasMore.value = false
  selected.value = null
  error.value = ''
  searching.value = true
  const timeout = window.setTimeout(loadResources, 250)
  onCleanup(() => window.clearTimeout(timeout))
})

onMounted(checkSession)
</script>

<template>
  <div v-if="loadingSession" class="boot-screen" role="status">正在打开你的资料库…</div>

  <main v-else-if="!username" class="login-screen">
    <div class="login-art" aria-hidden="true">
      <span class="login-art-index">MIZUKI / 01</span>
      <div class="login-art-content">
        <span class="login-art-symbol">M.</span>
        <p>让重要的资料，<br />拥有自己的位置。</p>
      </div>
      <span class="login-art-foot">A PRIVATE SPACE FOR WHAT MATTERS</span>
    </div>
    <section class="login-card" aria-labelledby="login-title">
      <div class="brand"><span class="brand-mark">水</span><span>Mizuki Archive</span></div>
      <p class="eyebrow">PRIVATE ARCHIVE</p>
      <h1 id="login-title">欢迎回来</h1>
      <p class="login-description">从这里继续整理、查找和取回你的资料。</p>
      <form class="login-form" @submit.prevent="login">
        <label for="username">管理员账号</label>
        <input id="username" v-model="loginName" autocomplete="username" required placeholder="输入账号" />
        <label for="password">密码</label>
        <input
          id="password"
          v-model="password"
          type="password"
          autocomplete="current-password"
          required
          placeholder="输入密码"
        />
        <p v-if="error" class="form-error" role="alert">{{ error }}</p>
        <button class="primary-button" type="submit" :disabled="loggingIn">
          {{ loggingIn ? '正在进入…' : '进入资料库' }} <span aria-hidden="true">↗</span>
        </button>
      </form>
      <p class="login-footnote">私人资料库 · 请勿在共享设备上保持登录</p>
    </section>
  </main>

  <div v-else class="workspace">
    <header class="site-header">
      <div class="site-header-inner">
        <div class="brand"><span class="brand-mark">水</span><span>Mizuki Archive</span></div>
        <span class="site-header-label">个人资料库</span>
        <div class="account">
          <span class="avatar" aria-hidden="true">{{ username.slice(0, 1).toUpperCase() }}</span>
          <span class="account-name">{{ username }}</span>
          <button type="button" class="text-button mobile-logout" @click="logout">退出</button>
        </div>
      </div>
    </header>

    <main class="content">
      <div class="content-inner">
        <section class="welcome" aria-labelledby="page-title">
          <p class="eyebrow">MIZUKI · PERSONAL ARCHIVE</p>
          <h1 id="page-title">每一份资料，<br />都有自己的位置。</h1>
          <p>从这里整理、查找和取回你的文件。你的内容只在登录后可见。</p>
        </section>

        <nav class="filter-nav" aria-label="按资料类型筛选">
          <button
            v-for="item in kinds"
            :key="item.value"
            type="button"
            class="nav-item"
            :class="{ active: kind === item.value }"
            :aria-pressed="kind === item.value"
            @click="chooseKind(item.value)"
          ><span class="nav-label">{{ item.label }}</span><span class="nav-short">{{ item.short }}</span></button>
        </nav>

        <section class="library-section" aria-labelledby="library-title">
          <div class="section-heading">
            <div>
              <h2 id="library-title">{{ sectionName }}</h2>
            </div>
            <button class="primary-button upload-button" type="button" :disabled="uploading" @click="fileInput?.click()">
              <span aria-hidden="true">＋</span> {{ uploading ? '正在上传…' : '添加资料' }}
            </button>
            <input
              ref="fileInput"
              class="visually-hidden"
              type="file"
              accept=".pdf,.png,.jpg,.jpeg,.webp,.md,.txt"
              @change="upload"
            />
          </div>
          <div class="toolbar">
            <label class="search-box">
              <span aria-hidden="true">⌕</span>
              <span class="visually-hidden">搜索文件名</span>
              <input v-model="search" type="search" placeholder="搜索文件名…" maxlength="100" />
            </label>
            <span class="toolbar-hint">支持 PDF、图片、Markdown 与文本 · 单文件 ≤ 50 MB</span>
          </div>
          <p v-if="error" class="message error" role="alert">{{ error }}</p>
          <p v-if="notice" class="message success" role="status">{{ notice }}</p>

          <div v-if="searching && resources.length === 0" class="empty-state" role="status">正在查找资料…</div>
          <div v-else-if="resources.length === 0 && !error" class="empty-state">
            <div class="empty-illustration" aria-hidden="true"><span>＋</span></div>
            <h3>{{ search || kind ? '没有找到匹配的资料' : '这里还没有资料' }}</h3>
            <p>{{ search || kind ? '试试其他关键词，或切换资料类型。' : '上传第一份文件，让你的个人资料库从这里开始。' }}</p>
            <button v-if="!search && !kind" class="secondary-button" type="button" @click="fileInput?.click()">
              上传第一份资料 <span aria-hidden="true">↗</span>
            </button>
          </div>
          <div v-else-if="resources.length" class="resource-list" aria-label="资料列表">
            <button
              v-for="resource in resources"
              :key="resource.id"
              type="button"
              class="resource-row"
              :class="{ selected: selected?.id === resource.id }"
              @click="selectResource(resource)"
            >
              <span class="file-icon" :class="resource.kind">{{ resource.kind === 'image' ? '◈' : resource.kind === 'pdf' ? 'PDF' : resource.kind === 'markdown' ? 'MD' : 'TXT' }}</span>
              <span class="file-main">
                <strong>{{ resource.name }}</strong>
                <small>{{ kindLabel(resource.kind) }} <span aria-hidden="true">·</span> {{ formatSize(resource.size) }}</small>
              </span>
              <span class="file-date">{{ formatDate(resource.created_at) }}</span>
              <span class="row-arrow" aria-hidden="true">↗</span>
            </button>
          </div>
          <div v-if="resources.length" class="pagination">
            <button type="button" :disabled="page <= 1" @click="page--">上一页</button>
            <span>第 {{ page }} 页</span>
            <button type="button" :disabled="!hasMore" @click="page++">下一页</button>
          </div>
        </section>
      </div>
    </main>

    <!-- 详情是模态抽屉：焦点限制与关闭后的焦点恢复由脚本统一处理。 -->
    <div v-if="selected" class="detail-backdrop" @click.self="selected = null">
      <section
        ref="detailPanel"
        class="detail-panel"
        role="dialog"
        aria-modal="true"
        aria-labelledby="detail-title"
        @keydown="trapDetailFocus"
      >
        <button ref="closeButton" type="button" class="close-button" aria-label="关闭资料详情" @click="selected = null">×</button>
        <p class="eyebrow">资料详情</p>
        <div class="detail-icon" :class="selected.kind">{{ selected.kind === 'image' ? '◈' : kindLabel(selected.kind) }}</div>
        <h2 id="detail-title">{{ selected.name }}</h2>
        <p class="detail-description">这份资料已安全保存在你的私人资料库中。</p>
        <dl class="detail-meta">
          <div><dt>类型</dt><dd>{{ kindLabel(selected.kind) }}</dd></div>
          <div><dt>大小</dt><dd>{{ formatSize(selected.size) }}</dd></div>
          <div><dt>加入时间</dt><dd>{{ formatDate(selected.created_at) }}</dd></div>
          <div><dt>文件指纹</dt><dd class="hash">{{ selected.sha256.slice(0, 18) }}…</dd></div>
        </dl>
        <a class="primary-button download-button" :href="`/api/resources/${selected.id}/download`">
          下载原文件 <span aria-hidden="true">↗</span>
        </a>
        <p class="detail-footnote">预览与处理记录将在后续阶段加入。</p>
      </section>
    </div>
  </div>
</template>
