<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { api, type ExternalResource, type InboxSelection, type Resource } from './api'

const props = defineProps<{ refreshKey: number }>()
const emit = defineEmits<{ openFile: [resource: Resource]; editExternal: [resource: ExternalResource] }>()
const files = ref<Resource[]>([])
const externals = ref<ExternalResource[]>([])
const page = ref(1)
const hasMore = ref(false)
const loading = ref(false)
const busyId = ref('')
const batchBusy = ref(false)
const selectedKeys = ref<string[]>([])
const undoItems = ref<InboxSelection[]>([])
const tagUndoItems = ref<InboxSelection[]>([])
const batchTag = ref('')
const batchTagMode = ref<'add' | 'remove'>('add')
const tagUndoValue = ref('')
const tagUndoMode = ref<'add' | 'remove'>('remove')
const error = ref('')
const notice = ref('')
let requestVersion = 0

// visibleItems 根据当前页生成带来源的选择项，不把上一页或其他用户的列表缓存在勾选状态中。
const visibleItems = computed<InboxSelection[]>(() => [
  ...files.value.map((file) => ({ source: 'file' as const, id: file.id })),
  ...externals.value.map((item) => ({ source: 'external' as const, id: item.id })),
])

// selectionKey 让两类资料即便偶然同 ID 也保留独立的选择状态。
function selectionKey(source: InboxSelection['source'], id: string) { return `${source}:${id}` }

// isSelected 控制勾选框受状态驱动，避免刷新列表后 DOM 状态与实际提交不一致。
function isSelected(source: InboxSelection['source'], id: string) { return selectedKeys.value.includes(selectionKey(source, id)) }

// toggleSelection 限制最多 50 项；超限时保留原来的选择并给出提示。
function toggleSelection(source: InboxSelection['source'], id: string) {
  const key = selectionKey(source, id)
  if (selectedKeys.value.includes(key)) selectedKeys.value = selectedKeys.value.filter((item) => item !== key)
  else if (selectedKeys.value.length < 50) selectedKeys.value = [...selectedKeys.value, key]
  else notice.value = '每次最多选择 50 项，请先完成当前批次。'
}

// selectVisible 只选本页前 50 项，不跨页隐式选择未显示的资料。
function selectVisible() {
  selectedKeys.value = visibleItems.value.slice(0, 50).map((item) => selectionKey(item.source, item.id))
  notice.value = visibleItems.value.length > 50 ? '本页超过 50 项，已选择前 50 项。' : ''
}

// loadInbox 以请求序号丢弃过时响应，避免退出或翻页时显示旧的私有资料。
async function loadInbox(targetPage = page.value) {
  const currentVersion = ++requestVersion
  loading.value = true
  error.value = ''
  try {
    const result = (await api.listInbox(targetPage)).data
    if (currentVersion !== requestVersion) return
    files.value = result.files
    externals.value = result.external_resources
    page.value = result.page
    hasMore.value = result.has_more
    selectedKeys.value = []
  } catch (reason) {
    if (currentVersion === requestVersion) error.value = reason instanceof Error ? reason.message : '收件箱暂时无法加载'
  } finally {
    if (currentVersion === requestVersion) loading.value = false
  }
}

// completeItem 只切换本人的整理状态，失败时保留当前条目和错误提示。
async function completeItem(source: 'file' | 'external', id: string) {
  busyId.value = id
  error.value = ''
  notice.value = ''
  try {
    await api.setInboxStatus(source, id, 'organized')
    notice.value = '已移出待整理收件箱，资料仍保留在你的资料库。'
    await loadInbox()
    if (page.value > 1 && !files.value.length && !externals.value.length) await loadInbox(page.value - 1)
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '整理状态保存失败'
  } finally {
    busyId.value = ''
  }
}

// completeSelected 显式确认数量后整批更新；失败保留选择，成功只保留一次可撤销的操作。
async function completeSelected() {
  const chosen = visibleItems.value.filter((item) => selectedKeys.value.includes(selectionKey(item.source, item.id)))
  if (!chosen.length || !window.confirm(`将 ${chosen.length} 项资料移出待整理收件箱？原件和卡片不会删除，可在此处撤销。`)) return
  batchBusy.value = true
  error.value = ''
  notice.value = ''
  try {
    const result = await api.batchSetInboxStatus(chosen, 'organized')
    undoItems.value = chosen
    notice.value = `已整理 ${result.count} 项资料。可点击“撤销上一次批量整理”恢复待整理状态。`
    await loadInbox()
    if (page.value > 1 && !files.value.length && !externals.value.length) await loadInbox(page.value - 1)
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '批量整理失败，未修改所选资料'
  } finally {
    batchBusy.value = false
  }
}

// undoLastBatch 对上一次成功的选择项显式写回待整理状态；失败保留撤销入口以便重试。
async function undoLastBatch() {
  if (!undoItems.value.length) return
  batchBusy.value = true
  error.value = ''
  try {
    const result = await api.batchSetInboxStatus(undoItems.value, 'pending')
    undoItems.value = []
    notice.value = `已撤销 ${result.count} 项批量整理。`
    await loadInbox(1)
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '撤销失败，请重试'
  } finally {
    batchBusy.value = false
  }
}

// updateSelectedTags 确认后对当前选择批量增删标签；服务端返回的 changed 才进入撤销集合。
async function updateSelectedTags() {
  const tag = batchTag.value.trim()
  const chosen = visibleItems.value.filter((item) => selectedKeys.value.includes(selectionKey(item.source, item.id)))
  if (!chosen.length || !tag || !window.confirm(`${batchTagMode.value === 'add' ? '给' : '从'} ${chosen.length} 项资料${batchTagMode.value === 'add' ? '添加' : '移除'}标签“${tag}”？`)) return
  batchBusy.value = true
  error.value = ''
  notice.value = ''
  try {
    const result = await api.batchUpdateTags(chosen, tag, batchTagMode.value)
    tagUndoItems.value = result.changed
    tagUndoValue.value = tag
    tagUndoMode.value = batchTagMode.value === 'add' ? 'remove' : 'add'
    notice.value = result.count ? `已为 ${result.count} 项资料${batchTagMode.value === 'add' ? '添加' : '移除'}标签。可撤销。` : '没有资料发生标签变化。'
    batchTag.value = ''
    await loadInbox(page.value)
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '批量标签操作失败，未修改所选资料'
  } finally {
    batchBusy.value = false
  }
}

// undoLastTagBatch 是反向操作而非版本化回滚；明确提示用户并发编辑可能被覆盖。
async function undoLastTagBatch() {
  if (!tagUndoItems.value.length || batchBusy.value) return
  if (!window.confirm(`将对 ${tagUndoItems.value.length} 项资料执行反向标签操作；若此后有人修改了同一标签，可能覆盖新修改。是否继续？`)) return
  batchBusy.value = true
  error.value = ''
  notice.value = ''
  try {
    const result = await api.batchUpdateTags(tagUndoItems.value, tagUndoValue.value, tagUndoMode.value)
    notice.value = `反向操作改变了 ${result.count} 项资料；请核对并发编辑后的标签状态。`
    tagUndoItems.value = []
    await loadInbox(page.value)
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '撤销标签失败，请重试'
  } finally {
    batchBusy.value = false
  }
}

// changePage 只允许进入已知存在的下一页或前一页，翻页时不修改任何资料。
function changePage(target: number) {
  if (target < 1 || loading.value || batchBusy.value || (target > page.value && !hasMore.value)) return
  void loadInbox(target)
}

// 资料创建或删除后回到第一页重新查询，避免旧收件箱结果滞留。
watch(() => props.refreshKey, () => { void loadInbox(1) })
// 只在登录后挂载并读取当前会话的资料；退出时组件会卸载。
onMounted(() => { void loadInbox(1) })
</script>

<template>
  <section class="inbox-panel" aria-labelledby="inbox-title">
    <div class="inbox-heading"><div><p class="eyebrow">COLLECT · INBOX</p><h2 id="inbox-title">待整理收件箱</h2><p>先收录，再慢慢补上标签与说明。整理完成不会删除原件或卡片。</p></div><button class="secondary-button" type="button" :disabled="loading" @click="loadInbox()">刷新</button></div>
    <p v-if="error" class="message error" role="alert">{{ error }}</p>
    <p v-if="notice" class="message success" role="status">{{ notice }}</p>
    <div v-if="undoItems.length" class="inbox-batch-undo"><button class="secondary-button" type="button" :disabled="batchBusy || !!busyId" @click="undoLastBatch">撤销上一次批量整理（{{ undoItems.length }} 项）</button></div>
    <div v-if="tagUndoItems.length" class="inbox-batch-undo"><button class="secondary-button" type="button" :disabled="batchBusy || !!busyId" @click="undoLastTagBatch">反向恢复标签（{{ tagUndoItems.length }} 项，需确认）</button></div>
    <p v-if="loading && !files.length && !externals.length" role="status">正在加载待整理内容…</p>
    <p v-else-if="!files.length && !externals.length && !error" class="inbox-empty">{{ page === 1 ? '目前没有待整理条目。新上传文件或新建外部卡片会出现在这里。' : '本页暂无条目，可返回上一页。' }}</p>
    <div v-if="files.length || externals.length" class="inbox-groups">
      <div v-if="files.length"><h3>站内文件 <small>{{ files.length }} 项</small></h3><ul class="inbox-list"><li v-for="file in files" :key="file.id"><div><label class="inbox-select"><input type="checkbox" :checked="isSelected('file', file.id)" :disabled="loading || batchBusy || !!busyId || (selectedKeys.length >= 50 && !isSelected('file', file.id))" @change="toggleSelection('file', file.id)" /><span class="inbox-select-name">选择文件：{{ file.name }}</span></label><small>站内文件 · {{ file.kind }}</small></div><div class="inbox-actions"><button class="secondary-button" type="button" @click="emit('openFile', file)">查看并整理</button><button class="secondary-button" type="button" :disabled="!!busyId || batchBusy" @click="completeItem('file', file.id)">{{ busyId === file.id ? '保存中…' : '完成整理' }}</button></div></li></ul></div>
      <div v-if="externals.length"><h3>外部卡片 <small>{{ externals.length }} 项</small></h3><ul class="inbox-list"><li v-for="item in externals" :key="item.id"><div><label class="inbox-select"><input type="checkbox" :checked="isSelected('external', item.id)" :disabled="loading || batchBusy || !!busyId || (selectedKeys.length >= 50 && !isSelected('external', item.id))" @change="toggleSelection('external', item.id)" /><span class="inbox-select-name">选择卡片：{{ item.title }}</span></label><small>{{ item.resource_type }} · {{ item.location }}</small></div><div class="inbox-actions"><button class="secondary-button" type="button" @click="emit('editExternal', item)">编辑卡片</button><button class="secondary-button" type="button" :disabled="!!busyId || batchBusy" @click="completeItem('external', item.id)">{{ busyId === item.id ? '保存中…' : '完成整理' }}</button></div></li></ul></div>
    </div>
    <div v-if="files.length || externals.length" class="inbox-batch-actions" role="group" aria-label="批量操作当前页"><button class="secondary-button" type="button" :disabled="loading || batchBusy || !!busyId" @click="selectVisible">选中本页前 50 项</button><button class="secondary-button" type="button" :disabled="!selectedKeys.length || batchBusy || loading || !!busyId" @click="selectedKeys = []">清空选择</button><button class="primary-button" type="button" :disabled="!selectedKeys.length || batchBusy || loading || !!busyId" @click="completeSelected">{{ batchBusy ? '整理中…' : `批量完成整理（${selectedKeys.length} 项）` }}</button><label class="inbox-batch-tag">标签<input v-model="batchTag" maxlength="24" placeholder="例如：课程" /><select v-model="batchTagMode" aria-label="批量标签动作"><option value="add">添加</option><option value="remove">移除</option></select><button class="secondary-button" type="button" :disabled="!selectedKeys.length || !batchTag.trim() || batchBusy || loading || !!busyId" @click="updateSelectedTags">执行标签</button></label></div>
    <div v-if="page > 1 || hasMore" class="inbox-pages"><button type="button" class="secondary-button" :disabled="page === 1 || loading" @click="changePage(page - 1)">上一页</button><span>第 {{ page }} 页 · 每类最多 50 项</span><button type="button" class="secondary-button" :disabled="!hasMore || loading" @click="changePage(page + 1)">下一页</button></div>
  </section>
</template>
