<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { api, type ExternalResource, type InboxSelection, type Resource } from './api'

const props = defineProps<{ refreshKey: number }>()
const emit = defineEmits<{ openFile: [resource: Resource, siblings: Resource[]]; restored: []; deleted: [] }>()
const files = ref<Resource[]>([])
const cards = ref<ExternalResource[]>([])
const page = ref(1)
const hasMore = ref(false)
const loading = ref(false)
const restoring = ref(false)
const deleting = ref(false)
// busy 统一阻止恢复和删除请求交叉执行，避免当前页选择状态同时被两种操作改变。
const busy = computed(() => restoring.value || deleting.value)
const error = ref('')
const notice = ref('')
const selectedKeys = ref<string[]>([])
let requestVersion = 0

// visibleItems 只提供当前页两类资料的选择项，恢复请求仍由服务端核验归属。
const visibleItems = computed<InboxSelection[]>(() => [
  ...files.value.map((file) => ({ source: 'file' as const, id: file.id })),
  ...cards.value.map((card) => ({ source: 'external' as const, id: card.id })),
])

// selectionKey 为不同来源构造独立的选择键，即使两类资料的 ID 恰好相同也不会混淆。
function selectionKey(item: InboxSelection) { return `${item.source}:${item.id}` }

// loadArchive 丢弃过时的请求结果，避免翻页或退出时重新展示旧的私人资料。
async function loadArchive(targetPage = page.value) {
  const version = ++requestVersion
  loading.value = true
  error.value = ''
  files.value = []
  cards.value = []
  selectedKeys.value = []
  try {
    const result = (await api.listArchive(targetPage)).data
    if (version !== requestVersion) return
    files.value = result.files
    cards.value = result.external_resources
    page.value = result.page
    hasMore.value = result.has_more
    selectedKeys.value = []
  } catch (reason) {
    if (version === requestVersion) error.value = reason instanceof Error ? reason.message : '归档列表暂时无法加载'
  } finally {
    if (version === requestVersion) loading.value = false
  }
}

// changePage 仅在服务端确认有下一页时翻页，不把其他页隐式加入选择。
function changePage(target: number) {
  if (target < 1 || loading.value || busy.value || (target > page.value && !hasMore.value)) return
  void loadArchive(target)
}

// restoreSelected 对当前页至多 50 项提交明确的“未归档”目标；不把它描述为并发安全的撤销。
async function restoreSelected() {
  const chosen = visibleItems.value.filter((item) => selectedKeys.value.includes(selectionKey(item)))
  if (!chosen.length || busy.value || !window.confirm(`恢复 ${chosen.length} 项资料到活动列表？原有标签和整理状态保持不变。`)) return
  restoring.value = true
  error.value = ''
  notice.value = ''
  try {
    const result = await api.batchSetArchived(chosen, false)
    notice.value = `已恢复 ${result.count} 项资料。若其他设备同时修改归档状态，仍以服务端最新结果为准。`
    await loadArchive(page.value)
    if (page.value > 1 && !files.value.length && !cards.value.length) await loadArchive(page.value - 1)
    emit('restored')
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '恢复失败，归档资料未修改'
  } finally {
    restoring.value = false
  }
}

// deleteSelected 要求用户输入数量确认；归档不等于回收站，删除后不提供原件恢复承诺。
async function deleteSelected() {
  const chosen = visibleItems.value.filter((item) => selectedKeys.value.includes(selectionKey(item)))
  if (!chosen.length || busy.value) return
  const confirmation = window.prompt(`将永久删除 ${chosen.length} 项归档资料及其标签，并清理站内原件；此操作不可撤销。请输入「删除 ${chosen.length}」确认：`)
  if (confirmation !== `删除 ${chosen.length}`) return
  deleting.value = true
  error.value = ''
  notice.value = ''
  try {
    const result = await api.batchDelete(chosen)
    notice.value = result.cleanup_pending
      ? `已删除 ${result.deleted} 项，其中 ${result.cleanup_pending} 个文件原件待管理员清理；请勿重试整个批次。`
      : `已删除 ${result.deleted} 项，此操作不可撤销。`
    await loadArchive(page.value)
    if (page.value > 1 && !files.value.length && !cards.value.length) await loadArchive(page.value - 1)
    emit('deleted')
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '删除失败，请刷新后核对状态'
  } finally {
    deleting.value = false
  }
}

// 外部操作后回到第一页，避免归档页继续展示已经恢复的过时条目。
watch(() => props.refreshKey, () => { void loadArchive(1) })
// 归档视图只在已登录工作区挂载，首次读取仍由会话保护的接口提供。
onMounted(() => { void loadArchive(1) })
// 退出后立即作废飞行中的列表请求，不把上一账号的资料带进下一次登录。
onUnmounted(() => { requestVersion++ })
</script>

<template>
  <section class="inbox-panel archive-panel" aria-labelledby="archive-title">
    <div class="inbox-heading">
      <div>
        <p class="eyebrow">KEEP · RETURN</p>
        <h2 id="archive-title">归档资料</h2>
        <p>归档只从活动列表隐藏资料，不删除文件、标签或外部卡片。</p>
      </div>
    </div>
    <p v-if="loading" role="status">正在读取归档资料…</p>
    <p v-if="error" class="message error" role="alert">
      {{ error }} <button class="secondary-button" type="button" @click="loadArchive()">重试</button>
    </p>
    <p v-if="notice" class="message success" role="status">{{ notice }}</p>
    <p v-if="!loading && !error && !files.length && !cards.length" class="inbox-empty">暂无归档资料。</p>
    <div v-if="files.length || cards.length" class="inbox-groups">
      <div>
        <h3>已归档文件 <small>本页 {{ files.length }} 项</small></h3>
        <ul class="inbox-list">
          <li v-for="file in files" :key="file.id">
            <label class="inbox-select">
              <input
                v-model="selectedKeys" type="checkbox"
                :value="selectionKey({ source: 'file', id: file.id })"
                :disabled="busy || (selectedKeys.length >= 50 && !selectedKeys.includes(selectionKey({ source: 'file', id: file.id })))"
              />
              <span class="inbox-select-name">{{ file.name }}</span>
            </label>
            <small>{{ file.kind }} · {{ file.tags.join(' · ') || '无标签' }}</small>
            <button class="secondary-button" type="button" :disabled="busy" @click="emit('openFile', file, files)">查看详情</button>
          </li>
        </ul>
      </div>
      <div>
        <h3>已归档卡片 <small>本页 {{ cards.length }} 项</small></h3>
        <ul class="inbox-list">
          <li v-for="card in cards" :key="card.id">
            <label class="inbox-select">
              <input
                v-model="selectedKeys" type="checkbox"
                :value="selectionKey({ source: 'external', id: card.id })"
                :disabled="busy || (selectedKeys.length >= 50 && !selectedKeys.includes(selectionKey({ source: 'external', id: card.id })))"
              />
              <span class="inbox-select-name">{{ card.title }}</span>
            </label>
            <small>{{ card.resource_type || '外部卡片' }} · {{ card.tags.join(' · ') || '无标签' }}</small>
          </li>
        </ul>
      </div>
    </div>
    <div v-if="files.length || cards.length" class="inbox-batch-actions" role="group" aria-label="恢复当前页归档资料">
      <button class="secondary-button" type="button" :disabled="loading || busy" @click="selectedKeys = visibleItems.slice(0, 50).map(selectionKey)">选中本页前 50 项</button>
      <button class="secondary-button" type="button" :disabled="!selectedKeys.length || busy" @click="selectedKeys = []">清空选择</button>
      <button class="primary-button" type="button" :disabled="!selectedKeys.length || busy || loading" @click="restoreSelected">{{ restoring ? '恢复中…' : `恢复所选（${selectedKeys.length} 项）` }}</button>
      <button class="danger-button" type="button" :disabled="!selectedKeys.length || busy || loading" @click="deleteSelected">{{ deleting ? '删除中…' : `永久删除所选（${selectedKeys.length} 项）` }}</button>
    </div>
    <div v-if="page > 1 || hasMore" class="inbox-pages">
      <button class="secondary-button" type="button" :disabled="page === 1 || loading || busy" @click="changePage(page - 1)">上一页</button>
      <span>第 {{ page }} 页 · 每类最多 50 项</span>
      <button class="secondary-button" type="button" :disabled="!hasMore || loading || busy" @click="changePage(page + 1)">下一页</button>
    </div>
  </section>
</template>
