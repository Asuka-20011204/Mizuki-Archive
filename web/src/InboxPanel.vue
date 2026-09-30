<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { api, type ExternalResource, type Resource } from './api'

const props = defineProps<{ refreshKey: number }>()
const emit = defineEmits<{ openFile: [resource: Resource]; editExternal: [resource: ExternalResource] }>()
const files = ref<Resource[]>([])
const externals = ref<ExternalResource[]>([])
const page = ref(1)
const hasMore = ref(false)
const loading = ref(false)
const busyId = ref('')
const error = ref('')
const notice = ref('')
let requestVersion = 0

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

// changePage 只允许进入已知存在的下一页或前一页，翻页时不修改任何资料。
function changePage(target: number) {
  if (target < 1 || loading.value || (target > page.value && !hasMore.value)) return
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
    <p v-if="loading && !files.length && !externals.length" role="status">正在加载待整理内容…</p>
    <p v-else-if="!files.length && !externals.length && !error" class="inbox-empty">{{ page === 1 ? '目前没有待整理条目。新上传文件或新建外部卡片会出现在这里。' : '本页暂无条目，可返回上一页。' }}</p>
    <div v-if="files.length || externals.length" class="inbox-groups">
      <div v-if="files.length"><h3>站内文件 <small>{{ files.length }} 项</small></h3><ul class="inbox-list"><li v-for="file in files" :key="file.id"><div><strong>{{ file.name }}</strong><small>站内文件 · {{ file.kind }}</small></div><div class="inbox-actions"><button class="secondary-button" type="button" @click="emit('openFile', file)">查看并整理</button><button class="secondary-button" type="button" :disabled="!!busyId" @click="completeItem('file', file.id)">{{ busyId === file.id ? '保存中…' : '完成整理' }}</button></div></li></ul></div>
      <div v-if="externals.length"><h3>外部卡片 <small>{{ externals.length }} 项</small></h3><ul class="inbox-list"><li v-for="item in externals" :key="item.id"><div><strong>{{ item.title }}</strong><small>{{ item.resource_type }} · {{ item.location }}</small></div><div class="inbox-actions"><button class="secondary-button" type="button" @click="emit('editExternal', item)">编辑卡片</button><button class="secondary-button" type="button" :disabled="!!busyId" @click="completeItem('external', item.id)">{{ busyId === item.id ? '保存中…' : '完成整理' }}</button></div></li></ul></div>
    </div>
    <div v-if="page > 1 || hasMore" class="inbox-pages"><button type="button" class="secondary-button" :disabled="page === 1 || loading" @click="changePage(page - 1)">上一页</button><span>第 {{ page }} 页 · 每类最多 50 项</span><button type="button" class="secondary-button" :disabled="!hasMore || loading" @click="changePage(page + 1)">下一页</button></div>
  </section>
</template>
