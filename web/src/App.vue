<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { api, type ProcessingJob, type Resource } from './api'

// 登录状态、列表筛选和详情面板分别在本视图中管理；服务端始终是权限与资料的权威来源。
const username = ref('')
const loginName = ref('')
const password = ref('')
// 各操作分别记录忙碌状态：上传不应让搜索和退出按钮无故禁用。
const loadingSession = ref(true)
const loggingIn = ref(false)
const uploading = ref(false)
const searching = ref(false)
const savingFavorite = ref(false)
const savingTags = ref(false)
const savingName = ref(false)
const deleting = ref(false)
const detailError = ref('')
const error = ref('')
const notice = ref('')
const resources = ref<Resource[]>([])
const selected = ref<Resource | null>(null)
// 搜索条件与页码交给 API 查询，不在浏览器里模拟 MySQL 的筛选和分页。
const search = ref('')
const kind = ref('')
const tagFilter = ref('')
const page = ref(1)
const hasMore = ref(false)
const fileInput = ref<HTMLInputElement | null>(null)
const searchInput = ref<HTMLInputElement | null>(null)
const detailPanel = ref<HTMLElement | null>(null)
const closeButton = ref<HTMLButtonElement | null>(null)
const tagInput = ref('')
const draftTags = ref<string[]>([])
const tagSuggestions = ref<string[]>([])
const nameDraft = ref('')
const previewText = ref('')
const previewLoading = ref(false)
const previewError = ref('')
const jobs = ref<ProcessingJob[]>([])
const jobsLoading = ref(false)
const startingJob = ref(false)
const jobError = ref('')
let previousFocus: HTMLElement | null = null
// 并发列表请求使用单调序号，防止旧筛选的响应覆盖新筛选。
let listRequestId = 0
// 并发详情请求使用单调序号，防止快速切换资料时旧响应覆盖新选择。
let detailRequestId = 0
// 并发文本预览请求使用独立序号，关闭详情或切换资料时立即使旧内容失效。
let previewRequestId = 0
// 任务轮询只保留一个定时器，关闭详情或退出登录时必须清理。
let jobPollTimer: number | undefined
// 任务列表请求序号避免切换资料后旧任务状态覆盖当前详情。
let jobsRequestId = 0

// kinds 同时驱动类型导航与文字标签，新增格式时需与后端允许列表一起更新。
const kinds = [
  { value: '', label: '全部资料', short: '全部' },
  { value: 'pdf', label: 'PDF 文档', short: 'PDF' },
  { value: 'image', label: '图片素材', short: '图片' },
  { value: 'markdown', label: 'Markdown', short: 'Markdown' },
  { value: 'text', label: '文本记录', short: '文本' },
]

// sectionName 的计算回调从类型表查找当前标题；找不到时回退为“全部资料”。
const sectionName = computed(() => kinds.find((item) => item.value === kind.value)?.label || '全部资料')

// formatSize 将字节数转为列表可扫读的单位，小文件仍显示至少 1 KB。
function formatSize(size: number) {
  if (size < 1024 * 1024) {
    return `${Math.max(1, Math.round(size / 1024))} KB`
  }
  return `${(size / 1024 / 1024).toFixed(1)} MB`
}

// formatDate 用中文地区格式显示服务端时间，不改变原始时间戳。
function formatDate(date: string) {
  return new Intl.DateTimeFormat('zh-CN', { year: 'numeric', month: 'short', day: 'numeric' }).format(new Date(date))
}

// kindLabel 为资料类型提供简短标签；查找回调匹配类型值，未知类型保留原值以便排查数据。
function kindLabel(value: Resource['kind']) {
  return kinds.find((item) => item.value === value)?.short || value
}

// loadResources 按当前筛选从服务端读取列表；过期响应不能覆盖更新的搜索结果。
async function loadResources() {
  // 搜索、类型和页码统一走同一个列表请求，页面不在客户端伪造筛选结果。
  // 快速切换筛选条件时，旧请求可能比新请求晚返回；只有最后一次请求能更新列表。
  if (!username.value) return
  const requestId = ++listRequestId
  searching.value = true
  error.value = ''
  try {
    const result = await api.list(search.value, kind.value, tagFilter.value, page.value)
    if (requestId !== listRequestId) return
    resources.value = result.data
    hasMore.value = result.meta.has_more
    // some 的比较回调只匹配当前详情 ID；新列表不含该资料时关闭旧抽屉。
    if (selected.value && !resources.value.some((item) => item.id === selected.value?.id)) {
      closeDetail()
    }
  } catch (reason) {
    if (requestId === listRequestId) {
      error.value = reason instanceof Error ? reason.message : '无法加载资料'
    }
  } finally {
    if (requestId === listRequestId) searching.value = false
  }
}

// loadTagSuggestions 读取已有标签建议；失败不阻断资料列表，用户仍可手动输入后保存。
async function loadTagSuggestions(searchValue = '') {
  try {
    tagSuggestions.value = (await api.listTags(searchValue)).data
  } catch {
    tagSuggestions.value = []
  }
}

// checkSession 在首屏询问服务端会话状态，决定展示登录页还是资料库。
async function checkSession() {
  // 页面刷新后先确认 HttpOnly Cookie 是否仍有效；不能从本地存储推断登录身份。
  try {
    username.value = (await api.me()).username
  } catch {
    // 会话检查失败（包括网络错误）时先显示登录页；列表请求错误由列表自己报告。
    username.value = ''
  } finally {
    if (username.value) {
      await Promise.all([loadResources(), loadTagSuggestions()])
    }
    loadingSession.value = false
  }
}

// login 提交凭据并加载私有列表；失败信息留在登录表单中。
async function login() {
  // 只在登录成功后写入界面身份，密码成功后从组件状态移除。
  loggingIn.value = true
  error.value = ''
  try {
    username.value = (await api.login(loginName.value, password.value)).username
    password.value = ''
    await Promise.all([loadResources(), loadTagSuggestions()])
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '登录失败'
  } finally {
    loggingIn.value = false
  }
}

// logout 先让服务端撤销会话，再清理页面上的个人资料状态。
async function logout() {
  // 以服务端撤销会话为准；若请求失败则保留当前界面并提示用户重试。
  try {
    await api.logout()
    // 退出后使所有尚未返回的列表请求失效，避免私有资料重新出现在页面上。
    listRequestId++
    searching.value = false
    username.value = ''
    closeDetail()
    resources.value = []
    tagFilter.value = ''
    draftTags.value = []
    tagSuggestions.value = []
    notice.value = ''
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '退出失败'
  }
}

// upload 先做即时大小提示，再把实际文件校验与保存交给 Go 服务。
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
    // 上传成功后复用详情加载流程，确保文本预览、标签建议和任务记录与服务端最新状态同步。
    await selectResource(result.data)
    notice.value = '资料已安全存入资料库'
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '上传失败'
  } finally {
    input.value = ''
    uploading.value = false
  }
}

// closeDetail 关闭详情并使尚未返回的详情请求失效，避免关闭后旧响应重新打开抽屉。
function closeDetail() {
  detailRequestId++
  previewRequestId++
  jobsRequestId++
  stopJobPolling()
  selected.value = null
  previewText.value = ''
  previewLoading.value = false
  previewError.value = ''
  jobs.value = []
  jobError.value = ''
}

// loadTextPreview 读取文本资料的预览内容；Vue 以纯文本节点渲染，避免 HTML/脚本执行。
async function loadTextPreview(resource: Resource) {
  const requestId = ++previewRequestId
  previewText.value = ''
  previewError.value = ''
  if (resource.kind !== 'text' && resource.kind !== 'markdown') {
    previewLoading.value = false
    return
  }
  previewLoading.value = true
  try {
    const content = await api.previewText(resource.id)
    if (requestId !== previewRequestId || selected.value?.id !== resource.id) return
    previewText.value = content
  } catch (reason) {
    if (requestId !== previewRequestId || selected.value?.id !== resource.id) return
    previewError.value = reason instanceof Error ? reason.message : '无法加载文本预览'
  } finally {
    if (requestId === previewRequestId && selected.value?.id === resource.id) previewLoading.value = false
  }
}

// stopJobPolling 清理任务状态轮询，避免关闭详情后继续请求私有接口。
function stopJobPolling() {
  if (jobPollTimer !== undefined) {
    window.clearTimeout(jobPollTimer)
    jobPollTimer = undefined
  }
}

// jobStatusLabel 将后端状态翻译成用户能直接理解的中文提示。
function jobStatusLabel(status: ProcessingJob['status']) {
  const labels: Record<ProcessingJob['status'], string> = {
    pending: '等待处理',
    processing: '处理中',
    succeeded: '已完成',
    failed: '处理失败',
  }
  return labels[status]
}

// scheduleJobPolling 只在任务未结束时安排下一次刷新，避免成功后继续轮询。
function scheduleJobPolling(resourceId: string) {
  stopJobPolling()
  if (!jobs.value.some((job) => job.status === 'pending' || job.status === 'processing')) return
  jobPollTimer = window.setTimeout(() => {
    if (selected.value?.id === resourceId) void loadJobs(resourceId)
  }, 1500)
}

// loadJobs 读取当前资料的任务历史，并在等待或处理状态下自动刷新详情。
async function loadJobs(resourceId: string) {
  const requestId = ++jobsRequestId
  jobsLoading.value = true
  jobError.value = ''
  try {
    const result = await api.listJobs(resourceId)
    if (requestId !== jobsRequestId || selected.value?.id !== resourceId) return
    jobs.value = result.data
    scheduleJobPolling(resourceId)
  } catch (reason) {
    if (requestId === jobsRequestId && selected.value?.id === resourceId) {
      jobError.value = reason instanceof Error ? reason.message : '无法读取处理记录'
    }
  } finally {
    if (requestId === jobsRequestId) jobsLoading.value = false
  }
}

// processingTitle 根据资料类型说明当前可手动触发的处理器，避免用户误以为图片也会提取文本。
function processingTitle(kindValue: Resource['kind']) {
  return kindValue === 'image' ? '图片缩略图' : '文本提取'
}

// processingHelp 解释处理器的结果和原件关系，明确说明任务不会覆盖用户原始文件。
function processingHelp(kindValue: Resource['kind']) {
  return kindValue === 'image' ? '生成适合预览和分享的 PNG 缩略图，原文件不会被修改。' : '生成独立的文本副本，原文件不会被修改。'
}

// canProcess 判断详情页当前资料是否支持本轮已开放的手动处理器。
function canProcess(kindValue: Resource['kind']) {
  return ['pdf', 'text', 'markdown', 'image'].includes(kindValue)
}

// jobOutputLabel 为不同派生产物提供明确的下载文字，避免所有结果都显示成“提取文本”。
function jobOutputLabel(job: ProcessingJob) {
  return job.type === 'generate_thumbnail' ? '下载缩略图' : '下载提取文本'
}

// startProcessingJob 手动提交文本或缩略图任务；重复点击由后端幂等返回已有任务。
async function startProcessingJob() {
  const resource = selected.value
  if (!resource || startingJob.value || !canProcess(resource.kind)) return
  startingJob.value = true
  jobError.value = ''
  try {
    const result = resource.kind === 'image' ? await api.createThumbnailJob(resource.id) : await api.createTextJob(resource.id)
    jobs.value = [result.data, ...jobs.value.filter((job) => job.id !== result.data.id)]
    const outputName = resource.kind === 'image' ? '缩略图' : '文本提取结果'
    notice.value = result.data.status === 'succeeded' ? `已有成功的${outputName}` : `${outputName}任务已提交`
    scheduleJobPolling(resource.id)
  } catch (reason) {
    jobError.value = reason instanceof Error ? reason.message : '无法提交处理任务'
  } finally {
    startingJob.value = false
  }
}
// selectResource 从服务端重新获取详情，避免依赖可能已过期的列表快照。
async function selectResource(resource: Resource) {
  const requestId = ++detailRequestId
  error.value = ''
  detailError.value = ''
  try {
    const detail = (await api.get(resource.id)).data
    if (requestId !== detailRequestId) return
    selected.value = detail
    nameDraft.value = selected.value.name
    draftTags.value = [...(selected.value.tags || [])]
    tagInput.value = ''
    await Promise.all([loadTagSuggestions(), loadTextPreview(detail), loadJobs(detail.id)])
  } catch (reason) {
    if (requestId !== detailRequestId) return
    error.value = reason instanceof Error ? reason.message : '无法打开资料'
  }
}

// saveName 保存展示名称，不改变原始上传名称；失败时保留输入内容方便修正后重试。
async function saveName() {
  const resource = selected.value
  if (!resource || savingName.value) return
  savingName.value = true
  detailError.value = ''
  try {
    const updated = (await api.setName(resource.id, nameDraft.value)).data
    if (selected.value?.id === updated.id) selected.value = updated
    resources.value = resources.value.map((item) => item.id === updated.id ? updated : item)
  } catch (reason) {
    detailError.value = reason instanceof Error ? reason.message : '名称保存失败'
  } finally {
    savingName.value = false
  }
}

// deleteResource 二次确认后删除资料；成功时从列表移除并关闭详情，避免继续展示已删除内容。
async function deleteResource() {
  const resource = selected.value
  if (!resource || deleting.value || !window.confirm(`确定删除“${resource.name}”吗？删除后原件不可恢复。`)) return
  deleting.value = true
  detailError.value = ''
  try {
    await api.deleteResource(resource.id)
    resources.value = resources.value.filter((item) => item.id !== resource.id)
    closeDetail()
    notice.value = '资料已删除'
  } catch (reason) {
    detailError.value = reason instanceof Error ? reason.message : '删除失败'
  } finally {
    deleting.value = false
  }
}

// previewURL 生成同源预览地址；权限仍由服务端会话校验，浏览器不会直连存储目录。
function previewURL(id: string) {
  return `/api/resources/${encodeURIComponent(id)}/preview`
}

// addTag 只更新详情草稿，不立即写数据库；保存时一次性提交，失败仍能继续编辑。
function addTag() {
  const value = tagInput.value.trim().toLowerCase()
  if (!value) return
  if (value.length > 24 || !/^[\p{L}\p{N}._\-/]+$/u.test(value)) {
    detailError.value = '标签只能包含中文、字母、数字和 - _ . /，长度不超过 24 个字符'
    return
  }
  if (draftTags.value.includes(value)) {
    tagInput.value = ''
    return
  }
  if (draftTags.value.length >= 10) {
    detailError.value = '一份资料最多设置 10 个标签'
    return
  }
  draftTags.value = [...draftTags.value, value].sort()
  tagInput.value = ''
  detailError.value = ''
}

// removeTag 从未保存草稿中移除一个标签，真正的删除在保存按钮确认后发生。
function removeTag(tag: string) {
  draftTags.value = draftTags.value.filter((item) => item !== tag)
}

// saveTags 以完整标签数组提交服务端，成功后同步详情和当前列表中的同一条资料。
async function saveTags() {
  const resource = selected.value
  if (!resource || savingTags.value) return
  savingTags.value = true
  detailError.value = ''
  try {
    const updated = (await api.setTags(resource.id, draftTags.value)).data
    if (selected.value?.id === updated.id) selected.value = updated
    resources.value = resources.value.map((item) => item.id === updated.id ? updated : item)
    tagSuggestions.value = (await api.listTags()).data
  } catch (reason) {
    detailError.value = reason instanceof Error ? reason.message : '标签保存失败'
  } finally {
    savingTags.value = false
  }
}

// chooseTag 设置单个精确标签筛选；再次点击当前标签即可清除筛选。
function chooseTag(value: string) {
  tagFilter.value = tagFilter.value === value ? '' : value
  page.value = 1
}

// setFavorite 将详情中的目标状态提交服务端，成功后同步当前列表；失败保留原状态并在抽屉内提示。
async function setFavorite() {
  const resource = selected.value
  if (!resource || savingFavorite.value) return
  savingFavorite.value = true
  detailError.value = ''
  try {
    const updated = (await api.setFavorite(resource.id, !resource.favorite)).data
    if (!username.value) return
    if (selected.value?.id === updated.id) selected.value = updated
    // 列表项只替换同一资料，避免重新请求时误清空当前筛选和页码。
    resources.value = resources.value.map((item) => item.id === updated.id ? updated : item)
  } catch (reason) {
    if (selected.value?.id === resource.id) {
      detailError.value = reason instanceof Error ? reason.message : '收藏状态更新失败'
    }
  } finally {
    savingFavorite.value = false
  }
}

// chooseKind 切换资料类型时回到第一页，避免旧页码导致空结果。
function chooseKind(value: string) {
  kind.value = value
  page.value = 1
}

// clearFilters 清除关键词、类型和标签筛选，并把焦点还给搜索框。
function clearFilters() {
  search.value = ''
  kind.value = ''
  tagFilter.value = ''
  page.value = 1
  searchInput.value?.focus()
}

// trapDetailFocus 处理 Escape 与 Tab 循环，让模态详情不把键盘焦点漏到背景。
function trapDetailFocus(event: KeyboardEvent) {
  if (!selected.value) return
  // 详情抽屉作为模态层，键盘焦点不能落到背后的资料列表。
  if (event.key === 'Escape') {
    event.preventDefault()
    closeDetail()
    return
  }
  if (event.key !== 'Tab') return
  const focusable = detailPanel.value?.querySelectorAll<HTMLElement>('button:not([disabled]), a[href], input:not([disabled])')
  if (!focusable?.length) return
  if (!detailPanel.value?.contains(document.activeElement)) {
    event.preventDefault()
    const target = event.shiftKey ? focusable[focusable.length - 1] : focusable[0]
    target?.focus()
    return
  }
  if (event.shiftKey && document.activeElement === focusable[0]) {
    event.preventDefault()
    focusable[focusable.length - 1]?.focus()
  } else if (!event.shiftKey && document.activeElement === focusable[focusable.length - 1]) {
    event.preventDefault()
    focusable[0]?.focus()
  }
}

// restoreDetailFocus 在抽屉退场完成后把焦点还给打开详情的资料行。
function restoreDetailFocus() {
  previousFocus?.focus()
  previousFocus = null
}

// 详情回调只在打开/关闭时移动焦点；收藏后替换详情对象不能把焦点从按钮抢走。
watch(selected, async (current, previous) => {
  if (current && !previous) {
    nameDraft.value = current.name
    draftTags.value = [...(current.tags || [])]
    previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null
    await nextTick()
    if (selected.value) closeButton.value?.focus()
  }
})

// 筛选回调立即作废旧请求，并用可清理的短延迟合并连续输入。
watch([search, kind, tagFilter, page], (_current, _previous, onCleanup) => {
  if (!username.value) return
  // 筛选变化立即让旧响应失效；输入停止后再请求，避免短暂显示不匹配的资料。
  listRequestId++
  resources.value = []
  hasMore.value = false
  closeDetail()
  error.value = ''
  searching.value = true
  const timeout = window.setTimeout(loadResources, 250)
  // 清理回调取消上次筛选的定时器，避免输入过程中发出过时的请求。
  onCleanup(() => window.clearTimeout(timeout))
})

// 模态详情打开期间在文档级处理键盘；即使异步任务使焦点暂时离开抽屉，Escape 和 Tab 仍有效。
onMounted(() => {
  document.addEventListener('keydown', trapDetailFocus)
  void checkSession()
})
// 离开页面时同时清理任务轮询与键盘监听，避免组件销毁后继续处理用户输入。
onUnmounted(() => {
  stopJobPolling()
  document.removeEventListener('keydown', trapDetailFocus)
})
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
          <span class="welcome-orbit" aria-hidden="true"><span>M.</span></span>
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
        <div v-if="tagSuggestions.length" class="tag-filter" aria-label="按标签筛选">
          <span class="tag-filter-label">标签</span>
          <button
            v-for="tag in tagSuggestions"
            :key="tag"
            type="button"
            class="tag-filter-item"
            :class="{ active: tagFilter === tag }"
            :aria-pressed="tagFilter === tag"
            @click="chooseTag(tag)"
          >#{{ tag }}</button>
        </div>

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
              <span class="visually-hidden">搜索名称或已提取正文</span>
              <input ref="searchInput" v-model="search" type="search" placeholder="搜索名称或已提取正文…" maxlength="100" />
            </label>
            <span class="toolbar-hint">支持 PDF、图片、Markdown 与文本 · 单文件 ≤ 50 MB</span>
          </div>
          <p v-if="error" class="message error" role="alert">{{ error }}</p>
          <p v-if="notice" class="message success" role="status">{{ notice }}</p>

          <Transition name="content-swap" mode="out-in">
            <div v-if="searching && resources.length === 0" key="loading" class="empty-state loading-state" role="status">
              <div class="empty-illustration" aria-hidden="true"><span>⌕</span></div>
              <h3>正在查找资料…</h3>
              <p>在你的资料库中寻找合适的内容。</p>
            </div>
            <div v-else-if="resources.length === 0 && !error" key="empty" class="empty-state" role="status">
              <div class="empty-illustration" aria-hidden="true"><span>＋</span></div>
              <h3>{{ search || kind || tagFilter ? '没有找到匹配的资料' : '这里还没有资料' }}</h3>
              <p>{{ search || kind || tagFilter ? '试试其他关键词，或切换资料类型。' : '上传第一份文件，让你的个人资料库从这里开始。' }}</p>
              <button v-if="search || kind || tagFilter" class="secondary-button" type="button" @click="clearFilters">清除筛选 <span aria-hidden="true">↗</span></button>
              <button v-else class="secondary-button" type="button" @click="fileInput?.click()">
                上传第一份资料 <span aria-hidden="true">↗</span>
              </button>
            </div>
            <div v-else-if="resources.length" key="list" class="resource-list" aria-label="资料列表">
              <button
                v-for="(resource, index) in resources"
                :key="resource.id"
                type="button"
                class="resource-row"
                :class="{ selected: selected?.id === resource.id }"
                :style="{ '--row-index': Math.min(index, 9) }"
                :aria-label="`单击查看资料详情：${resource.name}`"
                @click="selectResource(resource)"
              >
                <span class="file-icon" :class="resource.kind">{{ resource.kind === 'image' ? '◈' : resource.kind === 'pdf' ? 'PDF' : resource.kind === 'markdown' ? 'MD' : 'TXT' }}</span>
                <span class="file-main">
                  <strong>{{ resource.name }}</strong>
                  <small>{{ kindLabel(resource.kind) }} <span aria-hidden="true">·</span> {{ formatSize(resource.size) }}</small>
                  <span v-if="resource.tags.length" class="file-tags" aria-label="资料标签">{{ resource.tags.map((tag) => `#${tag}`).join(' · ') }}</span>
                </span>
                <span v-if="resource.favorite" class="favorite-marker" aria-label="已收藏" title="已收藏">★</span>
                <span class="file-date">{{ formatDate(resource.created_at) }}</span>
                <span class="row-arrow" aria-hidden="true">↗</span>
              </button>
            </div>
          </Transition>
          <div v-if="resources.length" class="pagination">
            <button type="button" :disabled="page <= 1" @click="page--">上一页</button>
            <span>第 {{ page }} 页</span>
            <button type="button" :disabled="!hasMore" @click="page++">下一页</button>
          </div>
        </section>
      </div>
    </main>

    <!-- 详情是模态检查器：焦点限制与关闭后的焦点恢复由脚本统一处理。 -->
    <Transition name="drawer" @after-leave="restoreDetailFocus">
      <div v-if="selected" class="detail-backdrop" @click.self="closeDetail">
        <section
          ref="detailPanel"
          class="detail-panel"
          role="dialog"
          aria-modal="true"
          aria-labelledby="detail-title"
        >
          <button ref="closeButton" type="button" class="close-button" aria-label="关闭资料详情" @click="closeDetail">×</button>
          <header class="detail-header">
            <p class="eyebrow">资料检查器 · {{ kindLabel(selected.kind) }}</p>
            <div class="detail-title-row">
              <div class="detail-icon" :class="selected.kind">{{ selected.kind === 'image' ? '◈' : kindLabel(selected.kind) }}</div>
              <div class="detail-title-content">
                <form class="name-editor" aria-labelledby="detail-title" @submit.prevent="saveName">
                  <label class="visually-hidden" for="resource-name">资料展示名称</label>
                  <input id="resource-name" v-model="nameDraft" maxlength="180" aria-describedby="name-help" />
                  <button type="submit" class="secondary-button" :aria-disabled="savingName">{{ savingName ? '保存中…' : '保存' }}</button>
                </form>
                <p id="name-help" class="detail-name-help">展示名称可修改，原始上传名称保持不变。</p>
              </div>
            </div>
            <p class="detail-description">私有资料 · 仅当前登录会话可访问</p>
          </header>
          <div class="detail-body">
            <div class="detail-main-column">
              <section class="preview-section" aria-labelledby="preview-title">
                <div class="detail-section-heading">
                  <div><p class="eyebrow">内容</p><h3 id="preview-title">预览</h3></div>
                  <span class="preview-format">{{ selected.mime }}</span>
                </div>
                <div v-if="selected.kind === 'image'" class="preview-frame">
                  <img :src="previewURL(selected.id)" :alt="`预览：${selected.name}`" />
                </div>
                <div
                  v-else-if="selected.kind === 'text' || selected.kind === 'markdown'"
                  class="preview-frame text-preview"
                  role="region"
                  :aria-label="`文本预览：${selected.name}`"
                >
                  <p v-if="previewLoading" class="preview-placeholder" role="status">正在加载文本预览…</p>
                  <p v-else-if="previewError" class="preview-placeholder error" role="alert">{{ previewError }}</p>
                  <pre v-else>{{ previewText }}</pre>
                </div>
                <p v-else class="preview-note">此格式暂不在线预览，请下载原文件查看。</p>
              </section>
              <section class="processing-panel" aria-labelledby="processing-title">
                <div class="processing-heading">
                  <div><p class="eyebrow">按需处理</p><h3 id="processing-title">{{ processingTitle(selected.kind) }}</h3></div>
                  <span v-if="jobsLoading" class="processing-loading" role="status">同步中…</span>
                </div>
                <p class="processing-help">{{ processingHelp(selected.kind) }}</p>
                <button type="button" class="secondary-button processing-trigger" :disabled="startingJob || !canProcess(selected.kind)" @click="startProcessingJob">
                  {{ startingJob ? '提交中…' : selected.kind === 'image' ? '手动生成缩略图' : '手动提取文本' }}
                </button>
                <p v-if="jobError" class="processing-error" role="alert">{{ jobError }}</p>
                <ul v-if="jobs.length" class="processing-list" aria-label="处理记录">
                  <li v-for="job in jobs" :key="job.id" class="processing-item">
                    <div><strong>{{ jobStatusLabel(job.status) }}</strong><span>第 {{ job.attempts }}/{{ job.max_attempts }} 次尝试</span><p v-if="job.last_error" class="processing-error">{{ job.last_error }}</p></div>
                    <div v-if="job.status === 'succeeded' && job.asset" class="processing-result">
                      <img v-if="job.asset.kind === 'thumbnail'" class="thumbnail-result" :src="api.derivedPreviewURL(job.asset.id)" alt="生成的图片缩略图" />
                      <div class="processing-result-links">
                        <a class="text-link" :href="api.derivedDownloadURL(job.asset.id)">{{ jobOutputLabel(job) }}</a>
                      </div>
                    </div>
                  </li>
                </ul>
                <p v-else-if="!jobsLoading" class="processing-empty">还没有处理记录</p>
              </section>
            </div>
            <aside class="detail-side-column" aria-label="资料属性与操作">
              <section class="inspector-section" aria-labelledby="properties-title">
                 <div class="detail-section-heading"><div><p class="eyebrow">属性检查器</p><h3 id="properties-title">属性</h3></div></div>
                <dl class="detail-meta"><div><dt>类型</dt><dd>{{ kindLabel(selected.kind) }}</dd></div><div><dt>大小</dt><dd>{{ formatSize(selected.size) }}</dd></div><div><dt>加入时间</dt><dd>{{ formatDate(selected.created_at) }}</dd></div><div><dt>文件指纹</dt><dd class="hash">{{ selected.sha256.slice(0, 18) }}…</dd></div></dl>
              </section>
              <section class="detail-tags inspector-section" aria-labelledby="detail-tags-title">
                <div class="detail-tags-heading"><h3 id="detail-tags-title">标签</h3><span>{{ draftTags.length }}/10</span></div>
                <div v-if="draftTags.length" class="tag-list" aria-label="当前标签"><span v-for="tag in draftTags" :key="tag" class="tag-chip">#{{ tag }}<button type="button" :aria-label="`移除标签 ${tag}`" @click="removeTag(tag)">×</button></span></div>
                <form class="tag-editor" @submit.prevent="addTag"><label class="visually-hidden" for="tag-input">添加标签</label><input id="tag-input" v-model="tagInput" list="tag-suggestions" maxlength="24" placeholder="输入标签后按回车" /><datalist id="tag-suggestions"><option v-for="tag in tagSuggestions" :key="`suggestion-${tag}`" :value="tag" /></datalist><button type="submit" class="secondary-button">添加</button></form>
                <button type="button" class="primary-button save-tags-button" :aria-disabled="savingTags" @click="saveTags">{{ savingTags ? '正在保存…' : '保存标签' }}</button>
              </section>
              <div class="detail-actions">
                <button type="button" class="secondary-button favorite-button" :aria-pressed="selected.favorite" :aria-disabled="savingFavorite" @click="setFavorite"><span aria-hidden="true">{{ selected.favorite ? '★' : '☆' }}</span>{{ savingFavorite ? '正在保存…' : selected.favorite ? '已收藏' : '加入收藏' }}</button>
                <a class="primary-button download-button" :href="`/api/resources/${selected.id}/download`">下载原文件 <span aria-hidden="true">↗</span></a>
                <button type="button" class="danger-button" :aria-disabled="deleting" @click="deleteResource">{{ deleting ? '正在删除…' : '删除资料' }}</button>
              </div>
              <p v-if="detailError" class="message error" role="alert">{{ detailError }}</p>
              <p class="detail-footnote">处理结果会保留在原件之外，并可单独下载。</p>
            </aside>
          </div>
        </section>
      </div>
    </Transition>
  </div>
</template>
