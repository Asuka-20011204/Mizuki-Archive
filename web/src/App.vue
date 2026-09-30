<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import ArchivePanel from './ArchivePanel.vue'
import ExternalResourcePanel from './ExternalResourcePanel.vue'
import RelationsPanel from './RelationsPanel.vue'
import InboxPanel from './InboxPanel.vue'
import OrganizedPanel from './OrganizedPanel.vue'
import SearchPanel from './SearchPanel.vue'
import ResourceNotes from './ResourceNotes.vue'
import { adjacentResource } from './detail-navigation'
import { api, type ExternalResource, type InboxSelection, type ProcessingJob, type Resource } from './api'

// 登录状态、列表筛选和详情面板分别在本视图中管理；服务端始终是权限与资料的权威来源。
const username = ref('')
const loginName = ref('')
const password = ref('')
const emailAddress = ref('')
const phoneNumber = ref('')
const verificationCode = ref('')
type EmailAuthMode = 'login' | 'register' | 'phone-login' | 'phone-register' | 'password'
const emailAuthMode = ref<EmailAuthMode>('password')
const emailVerificationAvailable = ref(false)
const phoneVerificationAvailable = ref(false)
const requestingCode = ref(false)
const codeRequested = ref(false)
// 各操作分别记录忙碌状态：上传不应让搜索和退出按钮无故禁用。
const loadingSession = ref(true)
const loggingIn = ref(false)
const uploading = ref(false)
const exporting = ref(false)
const exportError = ref('')
const searching = ref(false)
const savingFavorite = ref(false)
const savingTags = ref(false)
const savingName = ref(false)
const deleting = ref(false)
const detailError = ref('')
const error = ref('')
const notice = ref('')
const resources = ref<Resource[]>([])
const inboxRefreshKey = ref(0)
const organizedRefreshKey = ref(0)
const archiveRefreshKey = ref(0)
const externalPanel = ref<InstanceType<typeof ExternalResourcePanel> | null>(null)
const recentResources = ref<Resource[]>([])
const selected = ref<Resource | null>(null)
const detailNavigationItems = ref<Resource[]>([])
const navigatingDetail = ref(false)
// 搜索条件与页码交给 API 查询，不在浏览器里模拟 MySQL 的筛选和分页。
const search = ref('')
const kind = ref('')
const tagFilter = ref('')
const page = ref(1)
const hasMore = ref(false)
const fileInput = ref<HTMLInputElement | null>(null)
const searchInput = ref<HTMLInputElement | null>(null)
const detailPanel = ref<HTMLElement | null>(null)
const notesPanel = ref<InstanceType<typeof ResourceNotes> | null>(null)
const closeButton = ref<HTMLButtonElement | null>(null)
const downloadLink = ref<HTMLAnchorElement | null>(null)
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
// 最近访问请求也使用序号，防止退出后较晚返回的私有资料重新出现在页面。
let recentRequestId = 0
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

// detailPosition 提示当前文件在打开详情的列表页中的位置，不推测未加载的其他页。
const detailPosition = computed(() => detailNavigationItems.value.findIndex((item) => item.id === selected.value?.id))
// previousDetail 和 nextDetail 只允许在当前列表页内切换，单项入口会自动禁用两端按钮。
const previousDetail = computed(() => selected.value ? adjacentResource(detailNavigationItems.value, selected.value.id, -1) : null)
const nextDetail = computed(() => selected.value ? adjacentResource(detailNavigationItems.value, selected.value.id, 1) : null)

// PublicView 控制未登录前台的单屏内容切换，避免宣传内容堆成长页面。
type PublicView = 'home' | 'features' | 'principles' | 'privacy' | 'contact' | 'login'
const publicViews: PublicView[] = ['home', 'features', 'principles', 'privacy', 'contact', 'login']
const publicView = ref<PublicView>('home')
const qqCopyStatus = ref('')

// syncPublicViewFromURL 支持刷新直达和浏览器前进/后退；无效片段回到首页。
function syncPublicViewFromURL() {
  const view = window.location.hash.slice(1) as PublicView
  publicView.value = publicViews.includes(view) ? view : 'home'
}

// showPublicView 用浏览器历史记录切换视图，保留查询参数和可分享地址。
function showPublicView(view: PublicView) {
  if (publicView.value === view) return
  window.history.pushState(null, '', `${window.location.pathname}${window.location.search}${view === 'home' ? '' : `#${view}`}`)
  publicView.value = view
  qqCopyStatus.value = ''
  window.scrollTo({ top: 0, behavior: 'auto' })
}

// focusPublicHeading 在过场后把键盘焦点交给新视图的标题。
function focusPublicHeading() {
  document.querySelector<HTMLElement>('.public-view-stage h1')?.focus({ preventScroll: true })
}

// copyContactQQ 将公开 QQ 号码复制到剪贴板，失败时保留可手动选择的号码。
async function copyContactQQ() {
  try {
    await navigator.clipboard.writeText('3178203745')
    qqCopyStatus.value = 'QQ 号码已复制'
  } catch {
    qqCopyStatus.value = '复制失败，请手动选择号码'
  }
}

// archiveStats 只统计当前服务端返回的视图，避免把分页数据误报成整库总量。
const archiveStats = computed(() => {
  const favoriteCount = resources.value.filter((resource) => resource.favorite).length
  const tagCount = new Set(resources.value.flatMap((resource) => resource.tags || [])).size
  const typeCount = new Set(resources.value.map((resource) => resource.kind)).size
  return [
    { value: String(resources.value.length).padStart(2, '0'), label: '当前视图资料' },
    { value: String(favoriteCount).padStart(2, '0'), label: '已收藏内容' },
    { value: String(tagCount).padStart(2, '0'), label: '正在使用的标签' },
    { value: String(typeCount).padStart(2, '0'), label: '资料类型' },
  ]
})

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
      // 筛选后的列表不再包含当前资料时，允许用户先保存仍在编辑的笔记。
      if (!notesPanel.value?.hasUnsavedDraft()) closeDetail(true)
    }
  } catch (reason) {
    if (requestId === listRequestId) {
      error.value = reason instanceof Error ? reason.message : '无法加载资料'
    }
  } finally {
    if (requestId === listRequestId) searching.value = false
  }
}

// loadRecent 读取 Redis 记录的最近查看资料；缓存故障时不影响主列表和登录。
async function loadRecent() {
  if (!username.value) return
  const requestId = ++recentRequestId
  try {
    const result = await api.recent()
    if (requestId === recentRequestId && username.value) recentResources.value = result.data
  } catch {
    if (requestId === recentRequestId) recentResources.value = []
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
  // 认证能力和会话都以服务端为准；未配置发送器时隐藏对应验证码入口。
  try {
    const capabilities = await api.authCapabilities()
    emailVerificationAvailable.value = capabilities.email_verification
    phoneVerificationAvailable.value = capabilities.phone_verification
    if (emailVerificationAvailable.value) emailAuthMode.value = 'login'
    else if (phoneVerificationAvailable.value) emailAuthMode.value = 'phone-login'
  } catch {
    emailVerificationAvailable.value = false
    phoneVerificationAvailable.value = false
    emailAuthMode.value = 'password'
  }
  // 页面刷新后先确认 HttpOnly Cookie 是否仍有效；不能从本地存储推断登录身份。
  try {
    username.value = (await api.me()).username
  } catch {
    // 会话检查失败（包括网络错误）时先显示登录页；列表请求错误由列表自己报告。
    username.value = ''
  } finally {
    if (username.value) {
      await Promise.all([loadResources(), loadTagSuggestions(), loadRecent()])
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
    await Promise.all([loadResources(), loadTagSuggestions(), loadRecent()])
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '登录失败'
  } finally {
    loggingIn.value = false
  }
}

// requestEmailCode 发送邮箱验证码；服务端会统一处理未知邮箱，前端不据此判断账号是否存在。
async function requestEmailCode() {
  requestingCode.value = true
  error.value = ''
  try {
    if (emailAuthMode.value === 'register') {
      await api.requestEmailRegistrationCode(emailAddress.value)
    } else {
      await api.requestEmailLoginCode(emailAddress.value)
    }
    codeRequested.value = true
    notice.value = '如果邮箱符合当前流程且邮件服务可用，验证码会发送到邮箱。'
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '验证码请求失败'
  } finally {
    requestingCode.value = false
  }
}

// loginWithEmail 使用一次性邮箱验证码注册或登录，并沿用密码登录后的私有资料加载流程。
async function loginWithEmail() {
  loggingIn.value = true
  error.value = ''
  try {
    const result = emailAuthMode.value === 'register'
      ? await api.registerWithEmailCode(emailAddress.value, verificationCode.value)
      : await api.loginWithEmailCode(emailAddress.value, verificationCode.value)
    username.value = result.username
    verificationCode.value = ''
    await Promise.all([loadResources(), loadTagSuggestions(), loadRecent()])
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '邮箱登录失败'
  } finally {
    loggingIn.value = false
  }
}

// switchEmailAuthMode 清理上一种流程的验证码状态，避免把注册验证码误提交到登录接口。
function switchEmailAuthMode(mode: EmailAuthMode) {
  emailAuthMode.value = mode
  verificationCode.value = ''
  codeRequested.value = false
  error.value = ''
  notice.value = ''
}

// requestPhoneCode 在功能开启时按当前注册或登录模式请求短信，保留统一响应避免枚举。
async function requestPhoneCode() {
  requestingCode.value = true
  error.value = ''
  try {
    if (emailAuthMode.value === 'phone-register') await api.requestPhoneRegistrationCode(phoneNumber.value)
    else await api.requestPhoneLoginCode(phoneNumber.value)
    codeRequested.value = true
    notice.value = '如果手机号符合当前流程且短信服务可用，验证码会发送到手机。'
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '验证码请求失败'
  } finally {
    requestingCode.value = false
  }
}

// loginWithPhone 在验证码验证成功后使用原有的私有资料加载流程。
async function loginWithPhone() {
  loggingIn.value = true
  error.value = ''
  try {
    const result = emailAuthMode.value === 'phone-register'
      ? await api.registerWithPhoneCode(phoneNumber.value, verificationCode.value)
      : await api.loginWithPhoneCode(phoneNumber.value, verificationCode.value)
    username.value = result.username
    verificationCode.value = ''
    await Promise.all([loadResources(), loadTagSuggestions(), loadRecent()])
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '手机号验证失败'
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
    recentRequestId++
    searching.value = false
    username.value = ''
    closeDetail(true)
    resources.value = []
    recentResources.value = []
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
    refreshInbox()
    search.value = ''
    kind.value = ''
    page.value = 1
    await loadResources()
    // 上传成功后复用详情加载流程，确保文本预览、标签建议和任务记录与服务端最新状态同步。
    const detailLoaded = await selectResource(result.data)
    if (result.data.duplicate) {
      notice.value = `资料已保存；疑似与「${result.data.duplicate.name}」内容重复。不会自动删除。`
    } else if (result.data.duplicate_check_unavailable) {
      notice.value = '资料已保存，但重复检查暂不可用，请稍后自行核对。'
    } else {
      notice.value = '资料已安全存入资料库'
    }
    if (!detailLoaded) notice.value += '；详情暂时无法加载'
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '上传失败'
  } finally {
    input.value = ''
    uploading.value = false
  }
}

// downloadPortableArchive 成功后才保存完整 JSON；限流与容量错误留在页面供用户处理。
async function downloadPortableArchive() {
  if (exporting.value) return
  exporting.value = true
  exportError.value = ''
  try {
    const blob = await api.downloadPortableExport()
    const url = URL.createObjectURL(blob)
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = `mizuki-archive-${new Date().toISOString().slice(0, 10)}.json`
    document.body.append(anchor)
    anchor.click()
    anchor.remove()
    // 浏览器接管下载后立即释放只含当前账号私人资料的临时 URL。
    window.setTimeout(() => URL.revokeObjectURL(url), 1000)
  } catch (reason) {
    exportError.value = reason instanceof Error ? reason.message : '无法导出资料清单'
  } finally {
    exporting.value = false
  }
}

// refreshInbox 通知独立面板读取服务端最新的待整理列表，不在前端拼装归属状态。
function refreshInbox() {
  inboxRefreshKey.value++
  organizedRefreshKey.value++
}

// refreshCollectionViews 让归档、恢复及删除后各独立视图重新读取服务端状态。
function refreshCollectionViews() {
  refreshInbox()
  archiveRefreshKey.value++
  void loadResources()
  void loadRecent()
  void loadTagSuggestions()
  externalPanel.value?.refresh()
}

// openExternalFromInbox 直接打开目标卡片的编辑表单，避免用户再次搜索同一条记录。
function openExternalFromInbox(item: ExternalResource) {
  externalPanel.value?.editCard(item)
  document.getElementById('external-title')?.scrollIntoView({ behavior: 'auto' })
}

// openRelated 先确认未保存的详情草稿，再从服务端重新读取目标和权限。
async function openRelated(item: InboxSelection) {
  const current = selected.value
  if (current) {
    const tagsChanged = draftTags.value.length !== current.tags.length || draftTags.value.some((tag, index) => tag !== current.tags[index])
    if ((nameDraft.value !== current.name || tagsChanged || notesPanel.value?.hasUnsavedDraft()) && !window.confirm('名称、标签或笔记尚未保存，跳转后草稿会丢失。继续吗？')) return
  }
  try {
    if (item.source === 'file') {
      const resource = (await api.get(item.id)).data
      await selectResource(resource)
    } else {
      const card = (await api.getExternalResource(item.id)).data
      if (selected.value) closeDetail(true)
      openExternalFromInbox(card)
    }
  } catch (reason) {
    const message = reason instanceof Error ? reason.message : '无法打开关联资料'
    if (selected.value) detailError.value = message
    else error.value = message
  }
}

// closeDetail 主动关闭时确认笔记草稿；退出或跳转后强制清理旧请求。
function closeDetail(force = false) {
  if (!force && notesPanel.value?.hasUnsavedDraft() && !window.confirm('笔记草稿尚未保存，关闭后会丢失。继续吗？')) return
  detailRequestId++
  previewRequestId++
  jobsRequestId++
  stopJobPolling()
  selected.value = null
  detailNavigationItems.value = []
  navigatingDetail.value = false
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
async function selectResource(resource: Resource, siblings: Resource[] = resources.value): Promise<boolean> {
  const requestId = ++detailRequestId
  error.value = ''
  detailError.value = ''
  try {
    const detail = (await api.get(resource.id)).data
    if (requestId !== detailRequestId) return false
    detailNavigationItems.value = siblings.some((item) => item.id === detail.id) ? [...siblings] : [detail]
    selected.value = detail
    void loadRecent()
    nameDraft.value = selected.value.name
    draftTags.value = [...(selected.value.tags || [])]
    tagInput.value = ''
    await Promise.all([loadTagSuggestions(), loadTextPreview(detail), loadJobs(detail.id)])
    return true
  } catch (reason) {
    if (requestId !== detailRequestId) return false
    const message = reason instanceof Error ? reason.message : '无法打开资料'
    if (selected.value) detailError.value = message
    else error.value = message
    return false
  }
}

// navigateDetail 保留当前页上下文；跳转前提醒未保存的名称或标签不会自动写入。
async function navigateDetail(step: -1 | 1) {
  const current = selected.value
  const next = step < 0 ? previousDetail.value : nextDetail.value
  if (!current || !next || navigatingDetail.value) return
  const tagsChanged = draftTags.value.length !== current.tags.length || draftTags.value.some((tag, index) => tag !== current.tags[index])
  if ((nameDraft.value !== current.name || tagsChanged || notesPanel.value?.hasUnsavedDraft()) && !window.confirm('名称、标签或笔记尚未保存，切换后草稿会丢失。继续吗？')) return
  navigatingDetail.value = true
  try {
    await selectResource(next, detailNavigationItems.value)
    await nextTick()
    if (selected.value && !detailPanel.value?.contains(document.activeElement)) closeButton.value?.focus()
  } finally {
    navigatingDetail.value = false
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
    recentResources.value = recentResources.value.map((item) => item.id === updated.id ? updated : item)
    refreshInbox()
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
    refreshInbox()
    recentRequestId++
    resources.value = resources.value.filter((item) => item.id !== resource.id)
    recentResources.value = recentResources.value.filter((item) => item.id !== resource.id)
    void loadRecent()
    closeDetail(true)
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
    refreshInbox()
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
    refreshInbox()
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

// trapDetailFocus 处理详情快捷键和 Tab 循环；输入框与系统组合键仍保持原生行为。
function trapDetailFocus(event: KeyboardEvent) {
  if (!selected.value) return
  // 详情抽屉作为模态层，键盘焦点不能落到背后的资料列表。
  if (event.key === 'Escape') {
    event.preventDefault()
    closeDetail()
    return
  }
  const target = event.target instanceof HTMLElement ? event.target : null
  const editing = target?.closest('input, textarea, select, [contenteditable="true"], [role="textbox"]')
  if (!editing && !event.altKey && !event.ctrlKey && !event.metaKey && !event.isComposing && !event.repeat) {
    if (event.key === 'ArrowLeft' && previousDetail.value) {
      event.preventDefault()
      void navigateDetail(-1)
      return
    }
    if (event.key === 'ArrowRight' && nextDetail.value) {
      event.preventDefault()
      void navigateDetail(1)
      return
    }
    if (event.key.toLowerCase() === 'd' && !navigatingDetail.value) {
      event.preventDefault()
      downloadLink.value?.click()
      return
    }
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
  window.addEventListener('popstate', syncPublicViewFromURL)
  window.addEventListener('hashchange', syncPublicViewFromURL)
  syncPublicViewFromURL()
  void checkSession()
})
// 离开页面时同时清理任务轮询与键盘监听，避免组件销毁后继续处理用户输入。
onUnmounted(() => {
  stopJobPolling()
  document.removeEventListener('keydown', trapDetailFocus)
  window.removeEventListener('popstate', syncPublicViewFromURL)
  window.removeEventListener('hashchange', syncPublicViewFromURL)
})
</script>

<template>
  <div v-if="loadingSession" class="boot-screen" role="status">正在打开你的资料库…</div>

  <main v-else-if="!username" class="login-screen">
    <div class="public-shell">
      <header class="public-header">
        <button class="brand brand-button" type="button" aria-label="返回首页" @click="showPublicView('home')"><span class="brand-mark">水</span><span>Mizuki Archive</span></button>
        <nav class="public-nav" aria-label="前台导航">
          <a href="#features" :aria-current="publicView === 'features' ? 'page' : undefined" @click.prevent="showPublicView('features')">产品</a>
          <a href="#principles" :aria-current="publicView === 'principles' ? 'page' : undefined" @click.prevent="showPublicView('principles')">原则</a>
          <a href="#privacy" :aria-current="publicView === 'privacy' ? 'page' : undefined" @click.prevent="showPublicView('privacy')">隐私</a>
          <a href="#contact" :aria-current="publicView === 'contact' ? 'page' : undefined" @click.prevent="showPublicView('contact')">联系</a>
          <a href="#login" :aria-current="publicView === 'login' ? 'page' : undefined" @click.prevent="showPublicView('login')">登录</a>
        </nav>
        <div class="public-header-right">
          <span class="public-header-note"><span class="status-dot" aria-hidden="true"></span> PRIVATE · LOCAL FIRST</span>
          <a class="public-header-link" href="https://github.com/Asuka-20011204/Mizuki-Archive" target="_blank" rel="noreferrer">GitHub <span aria-hidden="true">↗</span></a>
        </div>
      </header>

      <Transition name="public-view" mode="out-in" @after-enter="focusPublicHeading">
        <section v-if="publicView === 'home'" key="home" class="public-view-stage public-home-view" aria-labelledby="public-title">
          <div class="public-hero">
            <div class="public-copy">
              <p class="eyebrow">A QUIET SYSTEM FOR WHAT MATTERS</p>
              <h1 id="public-title" tabindex="-1">把分散的资料，<em>整理成自己的秩序。</em></h1>
              <p class="public-lede">Mizuki Archive 是一个为个人而生的数字资料空间。收进来、找得到、继续处理，让文件不再只是被保存，而是随时可以被重新使用。</p>
              <div class="public-actions"><a class="public-cta" href="#login" @click.prevent="showPublicView('login')">打开我的资料库 <span aria-hidden="true">↗</span></a><span class="public-action-note">独立空间 · 登录后可见</span></div>
              <div class="public-proof" aria-label="产品能力概览"><span><strong>01</strong> 收纳</span><span><strong>02</strong> 检索</span><span><strong>03</strong> 处理</span></div>
            </div>
            <div class="hero-stage" aria-hidden="true">
              <div class="stage-orbit stage-orbit-one"></div><div class="stage-orbit stage-orbit-two"></div><div class="stage-label stage-label-top">PRIVATE ARCHIVE / 2026</div>
              <div class="stage-card stage-card-back"><span>INDEX / 03</span><strong>Notes<br />& traces</strong></div>
              <div class="stage-card stage-card-main"><div class="stage-card-head"><span class="stage-card-mark">水</span><span>ARCHIVE / 01</span><span>•••</span></div><div class="stage-card-line stage-card-line-long"></div><div class="stage-card-line stage-card-line-short"></div><div class="stage-card-file"><span class="stage-file-icon">PDF</span><span><b>一份资料</b><small>Organized for later</small></span><span class="stage-arrow">↗</span></div><div class="stage-card-tags"><i>#收藏</i><i>#可检索</i><i>#私有</i></div></div>
              <div class="stage-card stage-card-front"><span class="stage-mini-index">03</span><strong>Find<br />your way<br />back.</strong><span class="stage-mini-line"></span></div><div class="stage-caption"><span>管</span><span>找</span><span>处理</span></div>
            </div>
          </div>
        </section>

        <section v-else-if="publicView === 'features'" key="features" class="public-view-stage public-features-view" aria-labelledby="features-title">
          <div class="public-page-heading"><p class="eyebrow">THE ARCHIVE SYSTEM</p><h1 id="features-title" tabindex="-1">不是把文件堆起来，<br /><em>而是让它们重新有用。</em></h1><p>为游戏资源、学习资料、创作素材和日常文件建立一条清晰的回路。</p></div>
          <div class="public-feature-grid" aria-label="产品特点"><article><span class="feature-index">01 / COLLECT</span><h2>先把资料收进来。</h2><p>文件、图片、PDF 和文字拥有统一入口，不需要先想好复杂的分类。</p></article><article><span class="feature-index">02 / RETURN</span><h2>再把它们找回来。</h2><p>关键词、标签、收藏和最近查看，让你从当下的需要出发。</p></article><article><span class="feature-index">03 / PROCESS</span><h2>让资料继续发生。</h2><p>文本提取和图片处理在后台完成，结果与原件并存并可追踪。</p></article></div>
          <button class="view-back-link" type="button" @click="showPublicView('home')">← 返回首页</button>
        </section>

        <section v-else-if="publicView === 'principles'" key="principles" class="public-view-stage public-principles-view" aria-labelledby="principles-title">
          <div class="principles-heading"><p class="eyebrow">THE ARCHIVE PRINCIPLES</p><h1 id="principles-title" tabindex="-1">安静、私有，<br />并且始终可找回。</h1><p>高端感不只来自视觉，也来自产品对边界和细节的尊重。</p></div>
          <div class="principles-list"><article><span>01 / PRIVATE BY DEFAULT</span><h2>你的资料不做展品。</h2><p>未登录时不加载私有列表；进入资料库后，服务端会话才决定你能看到什么。</p></article><article><span>02 / ORIGINALS STAY INTACT</span><h2>原件和处理结果分开保留。</h2><p>文本提取、缩略图等派生产物不会覆盖原件，每次处理都有状态和结果可追踪。</p></article><article><span>03 / MADE TO RETURN TO</span><h2>不是囤积，是为了再次使用。</h2><p>从名称、标签、收藏到最近查看，让资料在需要的时候回到你的手边。</p></article></div>
          <button class="view-back-link" type="button" @click="showPublicView('home')">← 返回首页</button>
        </section>

        <section v-else-if="publicView === 'privacy'" key="privacy" class="public-view-stage public-privacy-view" aria-labelledby="privacy-title">
          <div class="privacy-heading"><p class="eyebrow">PRIVACY, WITHOUT THE FINE PRINT</p><h1 id="privacy-title" tabindex="-1">你的资料，<br /><em>不该成为<br />公开内容。</em></h1><p>这里说明当前版本实际如何使用和保存资料，也坦诚说明尚未解决的边界。</p><span class="privacy-version">隐私说明 · 2026-09-29</span></div>
          <div class="privacy-content">
            <p class="privacy-lead">每个账号拥有独立资料空间，访客只能浏览公开介绍。<strong>注册、上传和查看文件都必须经过服务端会话授权。</strong>系统不会因为知道资源 ID 就允许跨账号读取。</p>
            <div class="privacy-points">
              <article><span>01 / COLLECT</span><div><h2>上传了什么</h2><p>登录后保存原始文件、名称、类型、标签等元数据；按需处理时会保存提取文本或缩略图，登录会使用会话 Cookie。</p></div></article>
              <article><span>02 / ACCESS</span><div><h2>谁能访问</h2><p>资源列表、预览、下载和处理接口要求有效登录会话；文件不作为公开静态目录提供。请勿上传无权保存的他人敏感资料。</p></div></article>
              <article><span>03 / RETENTION</span><div><h2>删除与备份</h2><p>当前管理员删除后资料从列表隐藏，系统会尝试清理原件；派生文件、软删除元数据和已有备份可能继续留存。目前没有承诺统一的彻底删除期限。</p></div></article>
              <article><span>04 / CONTACT</span><div><h2>遇到隐私问题</h2><p>不承诺网络或存储绝对安全。若发现异常访问、误上传或希望询问数据处理方式，请通过 <a href="#contact" @click.prevent="showPublicView('contact')">联系页的 QQ</a> 告知，便于核查和处理。</p></div></article>
            </div>
            <p class="privacy-caveat">本页描述当前产品行为，不是“绝对安全”或免除责任的保证；若将来开放他人上传，需先制定正式隐私政策、保留期限与问题处理流程。</p>
            <button class="view-back-link" type="button" @click="showPublicView('home')">← 返回首页</button>
          </div>
        </section>

        <section v-else-if="publicView === 'contact'" key="contact" class="public-view-stage public-contact-view" aria-labelledby="contact-title">
          <div class="public-page-heading"><p class="eyebrow">KEEP IN TOUCH</p><h1 id="contact-title" tabindex="-1">一个项目，<br /><em>也应该有自己的来处。</em></h1><p>欢迎通过下面的入口了解项目、查看源码，或联系我交流想法。</p></div>
          <div class="contact-grid"><a class="contact-card" href="https://github.com/Asuka-20011204/Mizuki-Archive" target="_blank" rel="noreferrer"><span>OPEN SOURCE / 01</span><strong>GitHub</strong><small>查看项目源码与开发记录</small><b aria-hidden="true">↗</b></a><a class="contact-card" href="http://admin.asuka2001.cloud/" target="_blank" rel="noreferrer"><span>PERSONAL SPACE / 02</span><strong>博客 / 联系</strong><small>在博客中了解更多，也可以联系我</small><b aria-hidden="true">↗</b></a><button class="contact-card contact-card-qq" type="button" @click="copyContactQQ"><span>DIRECT CONTACT / 03</span><strong>QQ 3178203745</strong><small>点击复制 QQ 号码，也可手动记录</small><b aria-hidden="true">⧉</b></button></div>
          <p class="qq-copy-status" role="status">{{ qqCopyStatus }}</p>
          <button class="view-back-link" type="button" @click="showPublicView('home')">← 返回首页</button>
        </section>

        <section v-else key="login" class="public-view-stage public-login-view" aria-labelledby="login-heading">
          <section class="login-card" aria-labelledby="login-heading">
            <div class="login-card-intro"><span class="login-card-kicker">YOUR PRIVATE INDEX</span><span class="login-card-count">MIZUKI / 01</span></div>
            <div class="brand"><span class="brand-mark">水</span><span>Mizuki Archive</span></div>
            <p class="eyebrow">PRIVATE ARCHIVE</p>
            <h1 id="login-heading" tabindex="-1">欢迎回来</h1>
            <p class="login-description">每个账号都有自己的资料空间，登录后才能访问。</p>
            <div class="auth-mode-tabs" role="tablist" aria-label="身份验证方式">
              <template v-if="emailVerificationAvailable">
                <button type="button" :class="{ active: emailAuthMode === 'login' }" role="tab" :aria-selected="emailAuthMode === 'login'" @click="switchEmailAuthMode('login')">邮箱登录</button>
                <button type="button" :class="{ active: emailAuthMode === 'register' }" role="tab" :aria-selected="emailAuthMode === 'register'" @click="switchEmailAuthMode('register')">邮箱注册</button>
              </template>
              <template v-if="phoneVerificationAvailable">
                <button type="button" :class="{ active: emailAuthMode === 'phone-login' }" role="tab" :aria-selected="emailAuthMode === 'phone-login'" @click="switchEmailAuthMode('phone-login')">手机登录</button>
                <button type="button" :class="{ active: emailAuthMode === 'phone-register' }" role="tab" :aria-selected="emailAuthMode === 'phone-register'" @click="switchEmailAuthMode('phone-register')">手机注册</button>
              </template>
              <button type="button" :class="{ active: emailAuthMode === 'password' }" role="tab" :aria-selected="emailAuthMode === 'password'" @click="switchEmailAuthMode('password')">密码登录</button>
            </div>
            <form v-if="emailAuthMode === 'password'" class="login-form" @submit.prevent="login">
              <label for="username">兼容账号</label><input id="username" v-model="loginName" autocomplete="username" required placeholder="输入管理员账号" />
              <label for="password">密码</label><input id="password" v-model="password" type="password" autocomplete="current-password" required placeholder="输入密码" />
              <p v-if="error" class="form-error" role="alert">{{ error }}</p>
              <button class="primary-button" type="submit" :disabled="loggingIn">{{ loggingIn ? '正在进入…' : '进入资料库' }} <span aria-hidden="true">↗</span></button>
            </form>
            <form v-else-if="phoneVerificationAvailable && (emailAuthMode === 'phone-login' || emailAuthMode === 'phone-register')" class="login-form" @submit.prevent="loginWithPhone">
              <label for="phone">中国大陆手机号</label><input id="phone" v-model="phoneNumber" type="tel" inputmode="tel" autocomplete="tel-national" pattern="(?:1[3-9][0-9]{9}|\+861[3-9][0-9]{9})" maxlength="14" required placeholder="13800138000" />
              <div class="code-field"><label for="phone-verification-code">短信验证码</label><button type="button" class="code-button" :disabled="requestingCode || !phoneNumber" @click="requestPhoneCode">{{ requestingCode ? '发送中…' : codeRequested ? '重新获取' : '获取验证码' }}</button></div>
              <input id="phone-verification-code" v-model="verificationCode" inputmode="numeric" autocomplete="one-time-code" pattern="[0-9]{6}" maxlength="6" required placeholder="输入 6 位验证码" />
              <p class="form-hint">{{ emailAuthMode === 'phone-register' ? '验证成功后创建独立的私有资料空间。' : '验证码短时有效，且只能使用一次。' }}</p>
              <p v-if="error" class="form-error" role="alert">{{ error }}</p><p v-if="notice" class="form-notice" role="status">{{ notice }}</p>
              <button class="primary-button" type="submit" :disabled="loggingIn">{{ loggingIn ? '验证中…' : emailAuthMode === 'phone-register' ? '注册并进入' : '验证并进入' }} <span aria-hidden="true">↗</span></button>
            </form>
            <form v-else-if="emailVerificationAvailable" class="login-form" @submit.prevent="loginWithEmail">
              <label for="email">邮箱</label><input id="email" v-model="emailAddress" type="email" autocomplete="email" required placeholder="name@example.com" />
              <div class="code-field"><label for="verification-code">验证码</label><button type="button" class="code-button" :disabled="requestingCode || !emailAddress" @click="requestEmailCode">{{ requestingCode ? '发送中…' : codeRequested ? '重新获取' : '获取验证码' }}</button></div>
              <input id="verification-code" v-model="verificationCode" inputmode="numeric" autocomplete="one-time-code" pattern="[0-9]{6}" maxlength="6" required placeholder="输入 6 位验证码" />
              <p class="form-hint">{{ emailAuthMode === 'register' ? '验证成功后会创建独立的私有资料空间。' : '验证码短时有效，且只能使用一次。' }}</p>
              <p v-if="error" class="form-error" role="alert">{{ error }}</p><p v-if="notice" class="form-notice" role="status">{{ notice }}</p>
              <button class="primary-button" type="submit" :disabled="loggingIn">{{ loggingIn ? '验证中…' : emailAuthMode === 'register' ? '注册并进入' : '验证并进入' }} <span aria-hidden="true">↗</span></button>
            </form>
            <p class="login-footnote">资料按账号隔离 · 请勿在共享设备上保持登录</p>
          </section>
          <button class="view-back-link" type="button" @click="showPublicView('home')">← 返回首页</button>
        </section>
      </Transition>

      <footer class="public-footer"><span>© Mizuki Archive · Designed for a life of collected things.</span><nav class="public-footer-links" aria-label="站点与联系链接"><a href="#privacy" @click.prevent="showPublicView('privacy')">隐私说明</a><a href="https://github.com/Asuka-20011204/Mizuki-Archive" target="_blank" rel="noreferrer">GitHub ↗</a><a href="http://admin.asuka2001.cloud/" target="_blank" rel="noreferrer">博客 / 联系 ↗</a><button type="button" @click="showPublicView('contact')">QQ 3178203745</button></nav></footer>
    </div>
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
          <div class="welcome-copy">
            <p class="eyebrow">MIZUKI · PERSONAL ARCHIVE</p>
            <h1 id="page-title">每一份资料，<br />都有自己的位置。</h1>
            <p>从这里整理、查找和取回你的文件。你的内容只在登录后可见。</p>
          </div>
          <div class="welcome-manifest" aria-hidden="true"><span>ARCHIVE<br />MANIFEST</span><strong>01</strong><i></i><small>COLLECT / RETURN / PROCESS</small></div>
          <span class="welcome-orbit" aria-hidden="true"><span>M.</span></span>
        </section>
        <section class="archive-brief" aria-label="资料库状态">
          <div class="archive-brief-copy"><p class="eyebrow">ARCHIVE PULSE</p><h2>让整理变成一种轻盈的习惯。</h2><p>当前数字来自已加载的资料视图，列表筛选后会同步变化。</p></div>
          <div class="archive-stats"><div v-for="stat in archiveStats" :key="stat.label" class="archive-stat"><strong>{{ stat.value }}</strong><span>{{ stat.label }}</span></div></div>
        </section>

        <SearchPanel :refresh-key="archiveRefreshKey" @open-file="selectResource" @edit-external="openExternalFromInbox" />
        <InboxPanel :refresh-key="inboxRefreshKey" @open-file="selectResource" @edit-external="openExternalFromInbox" @archived="refreshCollectionViews" @deleted="refreshCollectionViews" />
        <OrganizedPanel :refresh-key="organizedRefreshKey" @open-file="selectResource" @edit-external="openExternalFromInbox" @changed="refreshCollectionViews" />
        <ArchivePanel :refresh-key="archiveRefreshKey" @open-file="selectResource" @restored="refreshCollectionViews" @deleted="refreshCollectionViews" />
        <ExternalResourcePanel ref="externalPanel" @changed="refreshInbox" @open-related="openRelated" />

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

        <section v-if="recentResources.length" class="recent-section" aria-labelledby="recent-title">
          <h2 id="recent-title">最近查看</h2>
          <div class="recent-items">
            <button
              v-for="resource in recentResources.slice(0, 5)"
              :key="resource.id"
              type="button"
              class="recent-item"
              @click="selectResource(resource, recentResources.slice(0, 5))"
            >
              <span class="recent-kind">{{ kindLabel(resource.kind) }}</span>
              <span class="recent-name">{{ resource.name }}</span>
              <span aria-hidden="true">↗</span>
            </button>
          </div>
        </section>

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
            <button class="secondary-button" type="button" :disabled="exporting" title="仅导出当前账号资料清单、笔记和关联；不是原件备份" @click="downloadPortableArchive">{{ exporting ? '正在导出…' : '导出清单（不含原件）' }}</button>
          </div>
          <p v-if="exportError" class="message error" role="alert">{{ exportError }}</p>
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
      <div v-if="selected" class="detail-backdrop" @click.self="closeDetail()">
        <section
          ref="detailPanel"
          class="detail-panel"
          role="dialog"
          aria-modal="true"
          aria-labelledby="detail-title"
        >
          <button ref="closeButton" type="button" class="close-button" aria-label="关闭资料详情" @click="closeDetail()">×</button>
          <header class="detail-header">
            <h2 id="detail-title" class="visually-hidden">{{ selected.name }}的详情</h2>
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
            <nav class="detail-navigation" aria-label="当前列表文件切换">
              <button type="button" class="secondary-button" :disabled="!previousDetail || navigatingDetail" @click="navigateDetail(-1)">← 上一项</button>
              <span class="detail-navigation-status" role="status">{{ detailPosition >= 0 ? `第 ${detailPosition + 1} / ${detailNavigationItems.length} 项` : '当前文件' }}</span>
              <button type="button" class="secondary-button" :disabled="!nextDetail || navigatingDetail" @click="navigateDetail(1)">下一项 →</button>
              <small>← / → 切换 · Esc 关闭 · D 下载；输入区保留编辑按键，PDF 内请用按钮</small>
            </nav>
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
                <iframe
                  v-else-if="selected.kind === 'pdf'"
                  class="preview-frame pdf-preview"
                  :src="previewURL(selected.id)"
                  :title="`PDF 预览：${selected.name}`"
                ></iframe>
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
              <ResourceNotes ref="notesPanel" :key="selected.id" :resource-id="selected.id" :kind="selected.kind" />
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
              <RelationsPanel source="file" :id="selected.id" @open="openRelated" />
              <div class="detail-actions">
                <button type="button" class="secondary-button favorite-button" :aria-pressed="selected.favorite" :aria-disabled="savingFavorite" @click="setFavorite"><span aria-hidden="true">{{ selected.favorite ? '★' : '☆' }}</span>{{ savingFavorite ? '正在保存…' : selected.favorite ? '已收藏' : '加入收藏' }}</button>
                <a ref="downloadLink" class="primary-button download-button" :href="`/api/resources/${selected.id}/download`">下载原文件 <span aria-hidden="true">↗</span></a>
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
