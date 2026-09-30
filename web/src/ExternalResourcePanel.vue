<script setup lang="ts">
import { nextTick, onMounted, ref } from 'vue'
import { api, type ExternalResource, type ExternalResourceInput } from './api'
import { parseShareText, ShareParseError } from './share-parser'

const emit = defineEmits<{ changed: [] }>()

const items = ref<ExternalResource[]>([])
const query = ref('')
const loading = ref(false)
const busy = ref(false)
const error = ref('')
const notice = ref('')
const editingId = ref('')
const formOpen = ref(false)
const tagText = ref('')
const pastedText = ref('')
const titleInput = ref<HTMLInputElement | null>(null)
const draft = ref<ExternalResourceInput>({ title: '', location: '', resource_type: '', version: '', note: '', status: 'pending', tags: [] })

// applyShareText 仅在浏览器中解析剪贴板文字并打开可编辑表单，不执行保存或外部请求。
async function applyShareText(text: string) {
  try {
    const prefill = parseShareText(text)
    openNewCard()
    draft.value = { ...draft.value, ...prefill }
    pastedText.value = text
    notice.value = '已预填卡片，请核对标题、位置和提取码后再保存。'
    await nextTick()
    titleInput.value?.focus()
  } catch (reason) {
    error.value = reason instanceof ShareParseError ? reason.message : '无法识别这段分享信息'
  }
}

// pasteShare 自动预填粘贴的链接或网盘信息；失败保留原文供用户检查。
function pasteShare(event: ClipboardEvent) {
  const text = event.clipboardData?.getData('text/plain')
  if (!text) return
  event.preventDefault()
  pastedText.value = text
  void applyShareText(text)
}

// loadExternalCards 从会话范围内检索卡片，失败时保留已有结果供重试。
async function loadExternalCards() {
  loading.value = true
  error.value = ''
  try {
    items.value = (await api.listExternalResources(query.value)).data
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '无法加载外部资源'
  } finally {
    loading.value = false
  }
}

// openNewCard 清空旧表单，默认放入待整理状态，减少收录时的填写负担。
function openNewCard() {
  editingId.value = ''
  draft.value = { title: '', location: '', resource_type: '', version: '', note: '', status: 'pending', tags: [] }
  tagText.value = ''
  pastedText.value = ''
  formOpen.value = true
  notice.value = ''
  error.value = ''
}

// editCard 拷贝当前卡片，取消编辑时不污染列表里的已保存内容。
function editCard(item: ExternalResource) {
  editingId.value = item.id
  draft.value = { title: item.title, location: item.location, resource_type: item.resource_type, version: item.version, note: item.note, status: item.status, tags: [...item.tags] }
  tagText.value = item.tags.join(', ')
  formOpen.value = true
  notice.value = ''
  // 表单挂载后把键盘焦点交给标题，方便从待整理收件箱直接接续编辑。
  void nextTick(() => titleInput.value?.focus())
}

// saveCard 提交完整卡片；输入错误和网络失败时保持表单供用户修改。
async function saveCard() {
  busy.value = true
  error.value = ''
  try {
    const value = { ...draft.value, tags: tagText.value.split(/[,，]/).map((tag) => tag.trim()).filter(Boolean) }
    const result = editingId.value
      ? await api.updateExternalResource(editingId.value, value)
      : await api.createExternalResource(value)
    formOpen.value = false
    pastedText.value = ''
    if (result.data.duplicate) {
      notice.value = `卡片已保存；疑似与「${result.data.duplicate.name}」链接重复。不会自动删除。`
    } else if (result.data.duplicate_check_unavailable) {
      notice.value = '卡片已保存，但重复检查暂不可用，请稍后自行核对。'
    } else {
      notice.value = '卡片已保存'
    }
    emit('changed')
    await loadExternalCards()
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '无法保存卡片'
  } finally {
    busy.value = false
  }
}

// removeCard 在用户确认后删除卡片；外部原件不在本站，不能被本站删除。
async function removeCard(item: ExternalResource) {
  if (!window.confirm(`仅删除「${item.title}」的本站卡片？外部文件不会删除。`)) return
  busy.value = true
  error.value = ''
  try {
    await api.deleteExternalResource(item.id)
    notice.value = '卡片已删除；外部原件不受影响'
    emit('changed')
    await loadExternalCards()
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '删除失败'
  } finally {
    busy.value = false
  }
}

// setCardFavorite 使用单项批量接口设置明确状态，已有整理资料也可直接收藏。
async function setCardFavorite(item: ExternalResource) {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try {
    await api.batchUpdateFavorites([{ source: 'external', id: item.id }], !item.favorite)
    await loadExternalCards()
    emit('changed')
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '收藏状态更新失败'
  } finally {
    busy.value = false
  }
}

// statusLabel 将服务端状态码映射成清楚的人工维护状态，不自动探测链接。
function statusLabel(status: ExternalResource['status']) {
  return { pending: '未核对', available: '可用', uncertain: '待确认', broken: '链接失效', downloaded: '已下载' }[status]
}

onMounted(loadExternalCards)

// 将现有卡片的编辑动作暴露给同一资料页的待整理收件箱，不开放给未登录页面。
defineExpose({ editCard, refresh: loadExternalCards })
</script>

<template>
  <section class="external-panel" aria-labelledby="external-title">
    <div class="external-heading">
      <div><p class="eyebrow">COLLECT · ELSEWHERE</p><h2 id="external-title">外部资源索引</h2><p>只收藏位置与线索，不上传、抓取或验证外部文件。</p></div>
      <button class="primary-button" type="button" @click="openNewCard">＋ 新建卡片</button>
    </div>
    <div class="external-paste">
      <label for="external-share-text">粘贴链接或网盘分享信息</label>
      <textarea id="external-share-text" v-model="pastedText" maxlength="8192" rows="2" placeholder="例如：名称：课程资料；链接：https://example.com/share；提取码：1234" @paste="pasteShare" />
      <button class="secondary-button" type="button" @click="applyShareText(pastedText)">解析并预填卡片</button>
      <small>只在浏览器中解析文字。不会自动保存、访问网盘或下载文件。</small>
    </div>
    <form class="external-search" @submit.prevent="loadExternalCards">
      <label for="external-query">搜索站外位置或标题</label>
      <input id="external-query" v-model="query" type="search" maxlength="100" placeholder="输入关键词" />
      <button class="secondary-button" type="submit" :disabled="loading">{{ loading ? '查找中…' : '查找' }}</button>
    </form>
    <p v-if="error" class="message error" role="alert">{{ error }}</p>
    <p v-if="notice" class="message success" role="status">{{ notice }}</p>
    <form v-if="formOpen" class="external-form" @submit.prevent="saveCard">
      <h3>{{ editingId ? '编辑资源卡片' : '收录外部资源' }}</h3>
      <label>标题 <input ref="titleInput" v-model="draft.title" required maxlength="200" /></label>
      <label>链接或本地位置 <input v-model="draft.location" required maxlength="2048" placeholder="只保存文字，不会访问此位置" /></label>
      <label>资源类型 <input v-model="draft.resource_type" required maxlength="40" placeholder="如游戏、课程、文档" /></label>
      <label>版本 <input v-model="draft.version" maxlength="80" /></label>
      <label>标签（用逗号分隔，最多 10 个） <input v-model="tagText" placeholder="例如：教程, 待看" /></label>
      <label>链接状态 <select v-model="draft.status"><option value="pending">未核对</option><option value="available">可用</option><option value="uncertain">待确认</option><option value="broken">链接失效</option><option value="downloaded">已下载</option></select></label>
      <label class="external-note">备注 <textarea v-model="draft.note" maxlength="2000" rows="3" /></label>
      <div class="external-actions"><button class="primary-button" type="submit" :disabled="busy">{{ busy ? '保存中…' : '保存卡片' }}</button><button class="secondary-button" type="button" :disabled="busy" @click="formOpen = false">取消</button></div>
    </form>
    <p v-if="loading && !items.length" role="status">正在加载卡片…</p>
    <p v-else-if="!items.length && !error" class="external-empty">暂无卡片。可以先记下网盘链接、网站或本地位置，稍后再整理。</p>
    <ul v-else class="external-list">
      <li v-for="item in items" :key="item.id" class="external-card">
        <div class="external-card-main"><span class="external-status">{{ statusLabel(item.status) }}</span><h3>{{ item.title }}</h3><p class="external-location">{{ item.location }}</p><p v-if="item.note">{{ item.note }}</p><small>{{ item.resource_type }}<span v-if="item.version"> · {{ item.version }}</span><span v-for="tag in item.tags" :key="tag"> · #{{ tag }}</span></small></div>
        <div class="external-actions"><button class="secondary-button" type="button" :disabled="busy" :aria-pressed="item.favorite" @click="setCardFavorite(item)">{{ item.favorite ? '★ 已收藏' : '☆ 收藏' }}</button><button class="secondary-button" type="button" :disabled="busy" @click="editCard(item)">编辑</button><button class="secondary-button" type="button" :disabled="busy" @click="removeCard(item)">删除</button></div>
      </li>
    </ul>
  </section>
</template>
