<script setup lang="ts">
import { ref } from 'vue'
import { api, type ExternalResource, type Resource, type SearchResults } from './api'

const emit = defineEmits<{ openFile: [resource: Resource]; editExternal: [resource: ExternalResource] }>()
const query = ref('')
const submitted = ref('')
const results = ref<SearchResults | null>(null)
const loading = ref(false)
const error = ref('')
let requestVersion = 0

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
  const text = query.value.trim()
  if (!text || Array.from(text).length > 100 || /\p{Cc}/u.test(query.value)) {
    ++requestVersion
    loading.value = false
    submitted.value = ''
    results.value = null
    error.value = '请输入 1 至 100 个不含控制字符的关键词'
    return
  }
  submitted.value = text
  void searchPage(1)
}

// changePage 显式更新分页，避免搜索词变更前误翻旧结果。
function changePage(page: number) {
  if (page < 1 || page > 1000 || loading.value) return
  void searchPage(page)
}
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
    </form>
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
