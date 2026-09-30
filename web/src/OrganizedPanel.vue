<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { api, type ExternalResource, type InboxSelection, type Resource, type SearchFilter } from './api'

const props = defineProps<{ refreshKey: number }>()
const emit = defineEmits<{ openFile: [resource: Resource, siblings: Resource[]]; editExternal: [resource: ExternalResource]; changed: [] }>()
const files = ref<Resource[]>([])
const cards = ref<ExternalResource[]>([])
const page = ref(1)
const hasMore = ref(false)
const loading = ref(false)
const busy = ref(false)
const error = ref('')
const notice = ref('')
const selectedKeys = ref<string[]>([])
const tag = ref('')
const tagMode = ref<'add' | 'remove'>('add')
const favorite = ref(true)
type UndoAction =
  | { kind: 'tags'; items: InboxSelection[]; tag: string; mode: 'add' | 'remove' }
  | { kind: 'favorites'; items: InboxSelection[]; favorite: boolean }
const lastUndo = ref<UndoAction | null>(null)
let requestVersion = 0

// organizedFilter 只请求当前账号的已整理、未归档条目，不从全部资料里做客户端筛选。
const organizedFilter: SearchFilter = { q: '', source: '', kind: '', tag: '', organization_status: 'organized' }

// selectionKey 用来源区分同一 ID 的文件和卡片，避免批量操作错选。
function selectionKey(item: InboxSelection) {
  return `${item.source}:${item.id}`
}

// visibleItems 只允许本页两类结果进入操作批次，翻页后选择会清空。
const visibleItems = computed<InboxSelection[]>(() => [
  ...files.value.map((file) => ({ source: 'file' as const, id: file.id })),
  ...cards.value.map((card) => ({ source: 'external' as const, id: card.id })),
])

// chosenItems 按当前页和选中键重建请求体，不从浏览器提交用户身份。
const chosenItems = computed(() => visibleItems.value.filter((item) => selectedKeys.value.includes(selectionKey(item))))

// loadOrganized 按两类来源各 20 项分页读取；旧请求不能覆盖新页或新账号的结果。
async function loadOrganized(target = page.value) {
  const version = ++requestVersion
  loading.value = true
  error.value = ''
  try {
    const result = (await api.searchAll(organizedFilter, target)).data
    if (version !== requestVersion) return
    if (target > 1 && !result.files.length && !result.external_resources.length) {
      await loadOrganized(target - 1)
      return
    }
    files.value = result.files
    cards.value = result.external_resources
    hasMore.value = result.has_more_files || result.has_more_external
    page.value = target
    selectedKeys.value = []
  } catch (reason) {
    if (version === requestVersion) error.value = reason instanceof Error ? reason.message : '已整理资料暂时无法加载'
  } finally {
    if (version === requestVersion) loading.value = false
  }
}

// changePage 只切换服务端确有数据的页面，不能把跨页选择带入一次批量请求。
function changePage(target: number) {
  if (target < 1 || busy.value || loading.value || (target > page.value && !hasMore.value)) return
  void loadOrganized(target)
}

// selectVisible 选中当前页全部可见条目；服务端 20+20 的窗口始终小于批次 50 项上限。
function selectVisible() {
  selectedKeys.value = visibleItems.value.map(selectionKey)
}

// finishChange 刷新当前面板和其他资料视图，反向操作不会被误称为并发安全的撤销。
function finishChange(message: string) {
  notice.value = message
  selectedKeys.value = []
  lastUndo.value = null
  emit('changed')
}

// returnToInbox 把已整理条目改回待整理；并发修改时以服务端当前状态为准。
async function returnToInbox() {
  const items = chosenItems.value
  if (!items.length || busy.value || !window.confirm(`将 ${items.length} 项资料退回待整理收件箱？`)) return
  busy.value = true
  error.value = ''
  try {
    const result = await api.batchSetInboxStatus(items, 'pending')
    finishChange(`已退回 ${result.count} 项；可在收件箱重新标记完成整理。并发修改请刷新核对。`)
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '操作失败，请刷新后核对状态'
  } finally {
    busy.value = false
  }
}

// updateTags 只更改一个标签，反向操作需用户重新选择并确认，以免覆盖其他设备的修改。
async function updateTags() {
  const items = chosenItems.value
  const name = tag.value.trim()
  if (!items.length || !name || busy.value || !window.confirm(`为 ${items.length} 项资料${tagMode.value === 'add' ? '添加' : '移除'}标签「${name}」？`)) return
  busy.value = true
  error.value = ''
  try {
    const result = await api.batchUpdateTags(items, name, tagMode.value)
    finishChange(`已更新 ${result.count} 项；仅实际变化项可反向恢复，并发修改请先核对。`)
    if (result.changed.length) lastUndo.value = { kind: 'tags', items: result.changed, tag: name, mode: tagMode.value === 'add' ? 'remove' : 'add' }
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '标签更新失败，请刷新后核对状态'
  } finally {
    busy.value = false
  }
}

// updateFavorites 提交明确目标值，避免重试时把收藏状态再次反转。
async function updateFavorites() {
  const items = chosenItems.value
  if (!items.length || busy.value || !window.confirm(`将 ${items.length} 项资料${favorite.value ? '设为收藏' : '取消收藏'}？`)) return
  busy.value = true
  error.value = ''
  try {
    const result = await api.batchUpdateFavorites(items, favorite.value)
    finishChange(`已更新 ${result.count} 项；仅实际变化项可反向恢复，并发修改请先核对。`)
    if (result.changed.length) lastUndo.value = { kind: 'favorites', items: result.changed, favorite: !favorite.value }
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '收藏更新失败，请刷新后核对状态'
  } finally {
    busy.value = false
  }
}

// undoLastChange 仅对服务端确认变化的条目执行反向操作；并发编辑可能被覆盖，必须再次确认。
async function undoLastChange() {
  const action = lastUndo.value
  if (!action || busy.value || !window.confirm(`对 ${action.items.length} 项实际变化的资料执行反向操作？若其他设备随后修改过，可能覆盖新状态。`)) return
  busy.value = true
  error.value = ''
  try {
    const result = action.kind === 'tags'
      ? await api.batchUpdateTags(action.items, action.tag, action.mode)
      : await api.batchUpdateFavorites(action.items, action.favorite)
    finishChange(`反向操作实际改变 ${result.count} 项；请核对其他设备的并发修改。`)
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '反向操作失败，请刷新后核对状态'
  } finally {
    busy.value = false
  }
}

// archiveSelected 从活动列表隐藏本批条目，归档不删除原件和标签。
async function archiveSelected() {
  const items = chosenItems.value
  if (!items.length || busy.value || !window.confirm(`归档 ${items.length} 项已整理资料？可在归档区恢复。`)) return
  busy.value = true
  error.value = ''
  try {
    const result = await api.batchSetArchived(items, true)
    finishChange(`已归档 ${result.count} 项；可在归档区恢复，并发修改请先核对。`)
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '归档失败，请刷新后核对状态'
  } finally {
    busy.value = false
  }
}

// deleteSelected 要求再次输入数量；数据库提交后不可撤销，文件待清理不应重试整批。
async function deleteSelected() {
  const items = chosenItems.value
  if (!items.length || busy.value) return
  const confirmation = window.prompt(`永久删除 ${items.length} 项资料及其标签？此操作不可撤销。请输入「删除 ${items.length}」确认：`)
  if (confirmation !== `删除 ${items.length}`) return
  busy.value = true
  error.value = ''
  try {
    const result = await api.batchDelete(items)
    finishChange(result.cleanup_pending
      ? `已删除 ${result.deleted} 项，${result.cleanup_pending} 个文件待后台清理；不要重试整批。`
      : `已删除 ${result.deleted} 项，此操作不可撤销。`)
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '删除状态不明，请刷新核对后再操作'
  } finally {
    busy.value = false
  }
}

// 外部资料变化后重新读取，避免已删除或已归档条目仍留在可选列表。
watch(() => props.refreshKey, () => { void loadOrganized(page.value) })
// 首次挂载只请求当前用户的已整理筛选视图。
onMounted(() => { void loadOrganized(1) })
// 退出或卸载后旧请求不得回填到另一账号的页面。
onUnmounted(() => { requestVersion++ })
</script>

<template>
  <section class="inbox-panel organized-panel" aria-labelledby="organized-title">
    <div class="inbox-heading">
      <div>
        <p class="eyebrow">FILE · CARD</p>
        <h2 id="organized-title">已整理资料</h2>
        <p>仅显示当前账号未归档的已整理文件与外部卡片；每次只操作本页所选。</p>
      </div>
    </div>
    <p v-if="loading" role="status">正在读取已整理资料…</p>
    <p v-if="error" class="message error" role="alert">{{ error }} <button class="secondary-button" type="button" @click="loadOrganized()">重试</button></p>
    <p v-if="notice" class="message success" role="status">{{ notice }}</p>
    <p v-if="!loading && !error && !files.length && !cards.length" class="inbox-empty">暂无已整理资料，先在收件箱完成整理。</p>
    <div v-if="files.length || cards.length" class="inbox-groups">
      <div>
        <h3>站内文件 <small>本页 {{ files.length }} 项</small></h3>
        <ul class="inbox-list">
          <li v-for="file in files" :key="file.id">
            <label class="inbox-select"><input v-model="selectedKeys" type="checkbox" :value="selectionKey({ source: 'file', id: file.id })" :disabled="busy || loading" /><span class="inbox-select-name">{{ file.name }}</span></label>
            <small>{{ file.kind }} · {{ file.tags.join(' · ') || '无标签' }}{{ file.favorite ? ' · ★ 已收藏' : '' }}</small>
            <button class="secondary-button" type="button" :disabled="busy" @click="emit('openFile', file, files)">查看详情</button>
          </li>
        </ul>
      </div>
      <div>
        <h3>外部卡片 <small>本页 {{ cards.length }} 项</small></h3>
        <ul class="inbox-list">
          <li v-for="card in cards" :key="card.id">
            <label class="inbox-select"><input v-model="selectedKeys" type="checkbox" :value="selectionKey({ source: 'external', id: card.id })" :disabled="busy || loading" /><span class="inbox-select-name">{{ card.title }}</span></label>
            <small>{{ card.resource_type }} · {{ card.tags.join(' · ') || '无标签' }}{{ card.favorite ? ' · ★ 已收藏' : '' }}</small>
            <button class="secondary-button" type="button" :disabled="busy" @click="emit('editExternal', card)">编辑卡片</button>
          </li>
        </ul>
      </div>
    </div>
    <div v-if="files.length || cards.length" class="inbox-batch-actions" role="group" aria-label="已整理资料批量操作">
      <button class="secondary-button" type="button" :disabled="busy || loading" @click="selectVisible">选中本页（{{ visibleItems.length }} 项）</button>
      <button class="secondary-button" type="button" :disabled="!selectedKeys.length || busy || loading" @click="selectedKeys = []">清空选择</button>
      <button class="secondary-button" type="button" :disabled="!selectedKeys.length || busy || loading" @click="returnToInbox">退回待整理（{{ selectedKeys.length }} 项）</button>
      <button class="secondary-button" type="button" :disabled="!selectedKeys.length || busy || loading" @click="archiveSelected">归档所选（{{ selectedKeys.length }} 项）</button>
      <button class="danger-button" type="button" :disabled="!selectedKeys.length || busy || loading" @click="deleteSelected">永久删除（{{ selectedKeys.length }} 项）</button>
      <div class="inbox-batch-tag"><label for="organized-tag">标签</label><input id="organized-tag" v-model="tag" maxlength="24" placeholder="例如：课程" /><select v-model="tagMode" aria-label="已整理资料标签动作"><option value="add">添加</option><option value="remove">移除</option></select><button class="secondary-button" type="button" :disabled="!selectedKeys.length || !tag.trim() || busy || loading" @click="updateTags">执行标签</button></div>
      <div class="inbox-batch-favorite"><label for="organized-favorite">收藏</label><select id="organized-favorite" v-model="favorite"><option :value="true">收藏</option><option :value="false">取消收藏</option></select><button class="secondary-button" type="button" :disabled="!selectedKeys.length || busy || loading" @click="updateFavorites">更新收藏</button></div>
    </div>
    <div v-if="lastUndo" class="inbox-batch-undo"><button class="secondary-button" type="button" :disabled="busy || loading" @click="undoLastChange">反向恢复上次{{ lastUndo.kind === 'tags' ? '标签' : '收藏' }}操作（{{ lastUndo.items.length }} 项）</button></div>
    <div v-if="page > 1 || hasMore" class="inbox-pages"><button class="secondary-button" type="button" :disabled="page === 1 || busy || loading" @click="changePage(page - 1)">上一页</button><span>第 {{ page }} 页 · 每类最多 20 项</span><button class="secondary-button" type="button" :disabled="!hasMore || busy || loading" @click="changePage(page + 1)">下一页</button></div>
  </section>
</template>
