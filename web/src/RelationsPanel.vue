<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api, type InboxSelection, type ResourceRelation } from './api'

const props = defineProps<{ source: 'file' | 'external'; id: string }>()
const emit = defineEmits<{ open: [item: InboxSelection] }>()
const links = ref<ResourceRelation[]>([])
const candidates = ref<{ target: InboxSelection; name: string }[]>([])
const query = ref('')
const loading = ref(false)
const searching = ref(false)
const busy = ref(false)
const error = ref('')
const notice = ref('')
let requestVersion = 0
const source = computed<InboxSelection>(() => ({ source: props.source, id: props.id }))
const searchId = computed(() => `relations-search-${props.source}-${props.id}`)

// loadRelations 在切换详情时丢弃旧请求结果，避免上一条资料的私有线索短暂显示。
async function loadRelations() {
  const current = ++requestVersion
  links.value = []
  loading.value = true
  searching.value = false
  error.value = ''
  try {
    const result = await api.listRelations(source.value)
    if (current === requestVersion) links.value = result.data
  } catch (reason) {
    if (current === requestVersion) error.value = reason instanceof Error ? reason.message : '无法读取关联'
  } finally {
    if (current === requestVersion) loading.value = false
  }
}

// searchCandidates 使用受会话保护的统一搜索，只展示当前账号最多两类各 20 项。
async function searchCandidates() {
  const keyword = query.value.trim()
  if (!keyword || searching.value) return
  const current = requestVersion
  candidates.value = []
  searching.value = true
  error.value = ''
  try {
    const result = (await api.searchAll({ q: keyword, source: '', kind: '', tag: '', organization_status: '' }, 1)).data
    if (current !== requestVersion || keyword !== query.value.trim()) return
    candidates.value = [
      ...result.files.map((file) => ({ target: { source: 'file' as const, id: file.id }, name: file.name })),
      ...result.external_resources.map((card) => ({ target: { source: 'external' as const, id: card.id }, name: card.title })),
    ].filter((candidate) => !(candidate.target.source === props.source && candidate.target.id === props.id)
      && !links.value.some((link) => link.target.source === candidate.target.source && link.target.id === candidate.target.id))
  } catch (reason) {
    if (current === requestVersion && keyword === query.value.trim()) error.value = reason instanceof Error ? reason.message : '无法搜索资料'
  } finally {
    if (current === requestVersion) searching.value = false
  }
}

// addLink 建立线索后重新读取列表，保持两端展示的资料名称以服务端为准。
async function addLink(target: InboxSelection) {
  if (busy.value) return
  const current = requestVersion
  const item = source.value
  busy.value = true
  error.value = ''
  notice.value = ''
  try {
    await api.createRelation(item, target)
    if (current !== requestVersion) return
    candidates.value = candidates.value.filter((candidate) => candidate.target.source !== target.source || candidate.target.id !== target.id)
    notice.value = '已关联；两条资料仍各自独立'
    busy.value = false
    await loadRelations()
  } catch (reason) {
    if (current === requestVersion) error.value = reason instanceof Error ? reason.message : '无法建立关联'
  } finally {
    if (current === requestVersion) busy.value = false
  }
}

// removeLink 仅解除数据库关系，文件、卡片和外部位置均不受影响。
async function removeLink(link: ResourceRelation) {
  if (busy.value || !window.confirm(`解除与「${link.name}」的关联？两条资料不会被删除。`)) return
  const current = requestVersion
  busy.value = true
  error.value = ''
  notice.value = ''
  try {
    await api.deleteRelation(link.id)
    if (current !== requestVersion) return
    notice.value = '关联已解除，资料未删除'
    busy.value = false
    await loadRelations()
  } catch (reason) {
    if (current === requestVersion) error.value = reason instanceof Error ? reason.message : '无法解除关联'
  } finally {
    if (current === requestVersion) busy.value = false
  }
}

// openLink 只传类型和标识，父组件从服务端重新取得目标详情与权限。
function openLink(target: InboxSelection) { emit('open', target) }

// watch 清除前一个资料的搜索候选；关联列表也会重新查询当前资料。
watch(() => [props.source, props.id], () => {
  requestVersion++
  links.value = []
  candidates.value = []
  searching.value = false
  busy.value = false
  query.value = ''
  notice.value = ''
  void loadRelations()
}, { immediate: true })
</script>

<template>
  <section class="relations-panel" aria-label="关联线索">
    <h3>关联线索 <span>{{ links.length }}/100</span></h3>
    <p>把游戏、攻略或截图连在一起；关联不会合并或复制文件。</p>
    <p v-if="loading" role="status">正在读取关联…</p>
    <ul v-else-if="links.length" class="relations-list">
      <li v-for="link in links" :key="link.id">
        <button type="button" class="text-link" @click="openLink(link.target)">{{ link.target.source === 'file' ? '文件' : '卡片' }} · {{ link.name }}</button>
        <button type="button" class="secondary-button" :disabled="busy" :aria-label="`解除与${link.name}的关联`" @click="removeLink(link)">解除</button>
      </li>
    </ul>
    <p v-else-if="!loading">暂无关联线索。</p>
    <form class="relations-search" @submit.prevent="searchCandidates">
      <label :for="searchId">搜索要关联的资料</label>
      <div><input :id="searchId" v-model="query" type="search" maxlength="100" placeholder="输入标题或关键词" /><button type="submit" class="secondary-button" :disabled="searching || busy || !query.trim()">{{ searching ? '搜索中…' : '查找' }}</button></div>
    </form>
    <ul v-if="candidates.length" class="relations-list" aria-label="可关联的搜索结果">
      <li v-for="candidate in candidates" :key="`${candidate.target.source}:${candidate.target.id}`">
        <span>{{ candidate.target.source === 'file' ? '文件' : '卡片' }} · {{ candidate.name }}</span>
        <button type="button" class="secondary-button" :disabled="busy || links.length >= 100" :aria-label="`关联${candidate.name}`" @click="addLink(candidate.target)">关联</button>
      </li>
    </ul>
    <p v-else-if="query && !searching && !error" class="relations-hint">查找后会显示当前账号可关联的结果。</p>
    <p v-if="error" class="message error" role="alert">{{ error }}</p>
    <p v-if="notice" class="message success" role="status">{{ notice }}</p>
  </section>
</template>
