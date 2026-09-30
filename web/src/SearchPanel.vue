<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { api, type ExternalResource, type Resource, type SavedSearch, type SearchFilter, type SearchResults } from './api'
import { normalizeSearchInput } from './search-filter'

const emit = defineEmits<{ openFile: [resource: Resource]; editExternal: [resource: ExternalResource] }>()
const props = defineProps<{ refreshKey: number }>()
const query = ref('')
const filter = ref<SearchFilter>({ q: '', source: '', kind: '', tag: '', organization_status: '' })
const submitted = ref<SearchFilter>({ ...filter.value })
const results = ref<SearchResults | null>(null)
const loading = ref(false)
const error = ref('')
const views = ref<SavedSearch[]>([])
const viewName = ref('')
const viewError = ref('')
const viewNotice = ref('')
const savingView = ref(false)
let requestVersion = 0

// currentFilter 在提交/保存前统一修剪文本，避免空条件触发无限制的私有数据列表。
function currentFilter(): SearchFilter | null {
  const value = normalizeSearchInput(query.value, filter.value)
  if (!value) {
    error.value = '请输入有效关键词或类型、标签、整理状态筛选条件'
    return null
  }
  return value
}

// searchPage 将同一关键词的每组结果独立限制在每页 20 项，旧响应不得覆盖新搜索。
async function searchPage(page: number) {
  const version = ++requestVersion
  loading.value = true
  error.value = ''
  results.value = null
  try {
    const response = await api.searchAll(submitted.value, page)
    if (version !== requestVersion) return
    results.value = response.data
  } catch (reason) {
    if (version === requestVersion) error.value = reason instanceof Error ? reason.message : '搜索暂时不可用'
  } finally {
    if (version === requestVersion) loading.value = false
  }
}

// submitSearch 校验后才记录查询词；空搜索清除上一轮结果，不读取整个私人资料库。
function submitSearch() {
  const nextFilter = currentFilter()
  if (!nextFilter) {
    ++requestVersion
    loading.value = false
    submitted.value = { q: '', source: '', kind: '', tag: '', organization_status: '' }
    results.value = null
    return
  }
  submitted.value = nextFilter
  void searchPage(1)
}

// changePage 显式更新分页，避免搜索词变更前误翻旧结果。
function changePage(page: number) {
  if (page < 1 || page > 1000 || loading.value) return
  void searchPage(page)
}

// loadViews 读取当前用户保存的筛选组合，不自动查询或暴露匹配的资料。
async function loadViews() {
  try {
    views.value = (await api.listSavedSearches()).data
  } catch (reason) {
    viewError.value = reason instanceof Error ? reason.message : '检索视图暂时无法加载'
  }
}

// saveView 保存输入框里的当前筛选组合，成功后刷新可点击的视图列表。
async function saveView() {
  const current = currentFilter()
  if (!current || savingView.value) return
  savingView.value = true
  viewError.value = ''
  viewNotice.value = ''
  try {
    const saved = (await api.createSavedSearch(viewName.value.trim(), current)).data
    views.value = [saved, ...views.value]
    viewName.value = ''
    viewNotice.value = `已保存检索视图「${saved.name}」`
  } catch (reason) {
    viewError.value = reason instanceof Error ? reason.message : '暂时无法保存检索视图'
  } finally {
    savingView.value = false
  }
}

// openView 应用已保存的组合条件，并按当前账号权限重新搜索最新资料。
function openView(view: SavedSearch) {
  filter.value = { ...view.filter }
  query.value = view.filter.q
  submitSearch()
}

// deleteView 先确认视图名称，仅删除当前账号的筛选条件，不删除任何资料。
async function deleteView(view: SavedSearch) {
  if (!window.confirm(`删除检索视图「${view.name}」？匹配的资料不会删除。`)) return
  viewError.value = ''
  viewNotice.value = ''
  try {
    await api.deleteSavedSearch(view.id)
    views.value = views.value.filter((item) => item.id !== view.id)
    viewNotice.value = `已删除检索视图「${view.name}」`
  } catch (reason) {
    viewError.value = reason instanceof Error ? reason.message : '暂时无法删除检索视图'
  }
}

onMounted(loadViews)
// 归档或恢复后重新运行当前搜索，避免结果仍展示已经离开活动列表的资料。
watch(() => props.refreshKey, () => { if (results.value || loading.value) void searchPage(results.value?.page ?? 1) })
</script>

<template>
  <section class="search-panel" aria-labelledby="unified-search-title">
    <div class="search-heading">
      <p class="eyebrow">FIND YOUR THREAD</p>
      <h2 id="unified-search-title">从线索，找到资料。</h2>
      <p>一次搜索文件名、标签、已提取正文，以及外部卡片的标题、地址和备注。结果按来源区分。</p>
    </div>
    <form class="search-form" role="search" @submit.prevent="submitSearch">
      <label for="unified-query">搜索我的资料</label>
      <div class="search-controls">
        <input id="unified-query" v-model="query" type="search" maxlength="100" placeholder="例如：课程、PDF 标注、游戏存档" />
        <button class="primary-button" type="submit" :disabled="loading">{{ loading ? '查找中…' : '查找线索' }}</button>
      </div>
      <div class="search-filters">
        <label>来源<select v-model="filter.source"><option value="">全部来源</option><option value="file">已上传文件</option><option value="external">外部卡片</option></select></label>
        <label>类型<input v-model="filter.kind" maxlength="40" placeholder="如 pdf、course" /></label>
        <label>标签<input v-model="filter.tag" maxlength="40" placeholder="如 学习" /></label>
        <label>整理状态<select v-model="filter.organization_status"><option value="">全部状态</option><option value="pending">待整理</option><option value="organized">已整理</option></select></label>
      </div>
    </form>
    <section class="saved-searches" aria-labelledby="saved-searches-title">
      <h3 id="saved-searches-title">我的检索视图</h3>
      <form class="saved-search-form" @submit.prevent="saveView">
        <label for="search-view-name">视图名称</label>
        <input id="search-view-name" v-model="viewName" maxlength="60" required placeholder="例如：尚未整理的课程 PDF" />
        <button class="secondary-button" type="submit" :disabled="savingView || views.length >= 30">{{ savingView ? '保存中…' : '保存当前条件' }}</button>
      </form>
      <p v-if="viewError" class="message error" role="alert">{{ viewError }}</p>
      <p v-if="viewNotice" role="status">{{ viewNotice }}</p>
      <ul v-if="views.length" class="saved-search-list">
        <li v-for="view in views" :key="view.id">
          <button class="secondary-button" type="button" @click="openView(view)">{{ view.name }}</button>
          <button class="secondary-button" type="button" :aria-label="`删除检索视图 ${view.name}`" @click="deleteView(view)">删除</button>
        </li>
      </ul>
      <p v-else class="search-empty">还没有保存的检索视图；选好筛选条件后可保存。</p>
    </section>
    <p v-if="error" class="message error" role="alert">{{ error }}</p>
    <p v-if="loading" role="status">正在搜索你的资料…</p>
    <p v-if="results && !loading" role="status">找到 {{ results.files.length }} 个文件和 {{ results.external_resources.length }} 张外部卡片（第 {{ results.page }} 页）。</p>
    <div v-if="results" class="search-groups">
      <section aria-label="已上传文件">
        <h3>已上传文件 <small>{{ results.files.length }} 项{{ results.has_more_files ? '，还有下一页' : '' }}</small></h3>
        <p v-if="!results.files.length" class="search-empty">本页没有匹配的文件。</p>
        <ul v-else class="search-list">
          <li v-for="item in results.files" :key="item.id">
            <div><strong>{{ item.name }}</strong><small>已上传 · {{ item.kind }} <span v-for="tag in item.tags" :key="tag"> · #{{ tag }}</span></small></div>
            <button class="secondary-button" type="button" :aria-label="`打开文件 ${item.name} 的详情`" @click="emit('openFile', item)">打开详情</button>
          </li>
        </ul>
      </section>
      <section aria-label="外部资源卡片">
        <h3>外部资源卡片 <small>{{ results.external_resources.length }} 项{{ results.has_more_external ? '，还有下一页' : '' }}</small></h3>
        <p v-if="!results.external_resources.length" class="search-empty">本页没有匹配的卡片。</p>
        <ul v-else class="search-list">
          <li v-for="item in results.external_resources" :key="item.id">
            <div><strong>{{ item.title }}</strong><small>仅记录位置 · {{ item.resource_type }} <span v-for="tag in item.tags" :key="tag"> · #{{ tag }}</span></small></div>
            <button class="secondary-button" type="button" :aria-label="`打开外部卡片 ${item.title}`" @click="emit('editExternal', item)">打开卡片</button>
          </li>
        </ul>
      </section>
    </div>
    <div v-if="results" class="search-pages">
      <button class="secondary-button" type="button" :disabled="loading || results.page <= 1" @click="changePage(results.page - 1)">上一页</button>
      <span>第 {{ results.page }} 页</span>
      <button class="secondary-button" type="button" :disabled="loading || (!results.has_more_files && !results.has_more_external)" @click="changePage(results.page + 1)">下一页</button>
    </div>
  </section>
</template>
