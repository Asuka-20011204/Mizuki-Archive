<script setup lang="ts">
import { computed, nextTick, onMounted, ref } from 'vue'
import { api, type ExternalResource, type InboxSelection, type Resource, type TopicDetail, type TopicInput, type TopicSummary } from './api'
import { moveItem, newTopicDraft, topicPayload } from './topic-editor'
import { loadTopicCandidates } from './topic-picker'

// TopicDraft 保留选择时的人类可读名称；提交仍只携带引用，详情展示取服务端当前名称。
type TopicDraft = Omit<TopicInput, 'sections'> & { sections: { title: string; items: (InboxSelection & { name?: string })[] }[] }

const emit = defineEmits<{ (event: 'open', item: InboxSelection): void }>()
const topics = ref<TopicSummary[]>([])
const selected = ref<TopicDetail | null>(null)
const draft = ref<TopicDraft>({ title: '', intro: '', cover_file_id: null, sections: [] })
const draftId = ref<string | null>(null)
const original = ref('')
const mode = ref<'list' | 'view' | 'edit'>('list')
const loading = ref(true)
const detailLoading = ref(false)
const saving = ref(false)
const deleting = ref(false)
const error = ref('')
const detailError = ref('')
const notice = ref('')
const pickerMode = ref<'cover' | 'item' | null>(null)
const pickerSection = ref(0)
const pickerSource = ref<'file' | 'external'>('file')
const pickerQuery = ref('')
const pickerPage = ref(1)
const pickerLoading = ref(false)
const pickerError = ref('')
const pickerFiles = ref<Resource[]>([])
const pickerCards = ref<ExternalResource[]>([])
const pickerHasMore = ref(false)
const pickerLimitedExternal = ref(false)
const pickerQueryInput = ref<HTMLInputElement | null>(null)
const editorTitle = ref<HTMLElement | null>(null)
const pageTitle = ref<HTMLElement | null>(null)
const createButton = ref<HTMLButtonElement | null>(null)
const coverFailed = ref(false)
let pickerTrigger: HTMLElement | null = null
let listRequestId = 0
let detailRequestId = 0
let pickerRequestId = 0

// itemCount 统计草稿所有分区的条目，新增操作不能超出单专题 100 条。
const itemCount = computed(() => draft.value.sections.reduce((total, section) => total + section.items.length, 0))
// dirty 只比较可写字段，浏览模式下服务端生成的名称变化不算草稿变更。
const dirty = computed(() => mode.value === 'edit' && JSON.stringify(draft.value) !== original.value)

// previewURL 只生成同源文件预览地址，Cookie 与本人图片校验交给服务器。
function previewURL(id: string) {
  return `/api/resources/${encodeURIComponent(id)}/preview`
}

// loadTopics 重新读取当前账号索引，旧响应不会覆盖较新的刷新结果。
async function loadTopics() {
  const requestId = ++listRequestId
  loading.value = true
  error.value = ''
  try {
    const result = await api.listTopics()
    if (requestId === listRequestId) topics.value = result.data
  } catch (reason) {
    if (requestId === listRequestId) error.value = reason instanceof Error ? reason.message : '无法读取专题'
  } finally {
    if (requestId === listRequestId) loading.value = false
  }
}

// confirmDiscard 在切换专题或退出编辑时提醒尚未保存的编排会丢失。
function confirmDiscard() {
  return !dirty.value || window.confirm('当前专题尚未保存，放弃这些修改吗？')
}

// openTopic 重新读取详情以获得最新名称和已清理的引用，不复用过期列表数据。
async function openTopic(id: string) {
  if (saving.value || deleting.value) return
  if (!confirmDiscard()) return
  const requestId = ++detailRequestId
  mode.value = 'view'
  selected.value = null
  draftId.value = null
  detailLoading.value = true
  detailError.value = ''
  notice.value = ''
  closePicker()
  try {
    const result = await api.getTopic(id)
    if (requestId === detailRequestId) selected.value = result.data
  } catch (reason) {
    if (requestId === detailRequestId) detailError.value = reason instanceof Error ? reason.message : '无法打开专题'
  } finally {
    if (requestId === detailRequestId) detailLoading.value = false
  }
}

// beginCreate 使用空编排创建私人专题，不在前端伪造服务端 ID。
function beginCreate() {
  if (saving.value || deleting.value) return
  if (!confirmDiscard()) return
  ++detailRequestId
  detailLoading.value = false
  closePicker()
  selected.value = null
  const fresh = newTopicDraft()
  draftId.value = fresh.id
  draft.value = fresh.input
  original.value = JSON.stringify(draft.value)
  detailError.value = ''
  notice.value = ''
  mode.value = 'edit'
  void nextTick(() => editorTitle.value?.focus())
}

// beginEdit 复制当前详情成为草稿，服务端生成的条目名称只用于编辑展示。
function beginEdit() {
  if (!selected.value || saving.value || deleting.value) return
  coverFailed.value = false
  draftId.value = selected.value.id
  draft.value = {
    title: selected.value.title,
    intro: selected.value.intro,
    cover_file_id: selected.value.cover_file_id,
    sections: selected.value.sections.map((section) => ({
      title: section.title,
      items: section.items.map((item) => ({ ...item })),
    })),
  }
  original.value = JSON.stringify(draft.value)
  detailError.value = ''
  mode.value = 'edit'
  void nextTick(() => editorTitle.value?.focus())
}

// cancelEdit 保留已保存的详情，并取消未提交的本地编排与选择器请求。
function cancelEdit() {
  if (saving.value) return
  if (!confirmDiscard()) return
  closePicker()
  mode.value = selected.value ? 'view' : 'list'
  detailError.value = ''
  void nextTick(() => (selected.value ? pageTitle.value : createButton.value)?.focus())
}

// addSection 在本地追加一个空分区，达到上限后只提示不发送请求。
function addSection() {
  if (draft.value.sections.length >= 10) {
    detailError.value = '每个专题最多 10 个分区'
    return
  }
  draft.value.sections.push({ title: '', items: [] })
  detailError.value = ''
}

// removeSection 仅移除草稿中的分区和引用，不删除任何资料。
function removeSection(index: number) {
  draft.value.sections.splice(index, 1)
}

// moveSection 用按钮调整分区顺序，键盘用户无需拖拽。
function moveSection(index: number, step: -1 | 1) {
  draft.value.sections = moveItem(draft.value.sections, index, step)
}

// moveEntry 在同一分区内调整条目顺序，保持每项来源与 ID 不变。
function moveEntry(sectionIndex: number, index: number, step: -1 | 1) {
  const section = draft.value.sections[sectionIndex]
  section.items = moveItem(section.items, index, step)
}

// removeEntry 只修改草稿引用，原件仍保留在资料库。
function removeEntry(sectionIndex: number, index: number) {
  draft.value.sections[sectionIndex].items.splice(index, 1)
}

// openPicker 按封面或条目类型打开本人资料搜索，不使用本地缓存代替权限判断。
function openPicker(type: 'cover' | 'item', sectionIndex = 0) {
  if (saving.value) return
  pickerTrigger = document.activeElement instanceof HTMLElement ? document.activeElement : null
  pickerMode.value = type
  pickerSection.value = sectionIndex
  pickerSource.value = 'file'
  pickerQuery.value = ''
  pickerPage.value = 1
  void loadPicker()
  // 等选择器输入框渲染后移入焦点，键盘用户可直接检索。
  void nextTick(() => pickerQueryInput.value?.focus())
}

// closePicker 使旧搜索响应失效，避免关闭后的私有结果重新出现。
function closePicker() {
  const trigger = pickerTrigger
  pickerTrigger = null
  ++pickerRequestId
  pickerMode.value = null
  pickerFiles.value = []
  pickerCards.value = []
  pickerError.value = ''
  pickerLimitedExternal.value = false
  // 关闭后将焦点还给原按钮；按钮已随页面切换移除时不强行转移。
  if (trigger) void nextTick(() => { if (document.contains(trigger)) trigger.focus() })
}

// loadPicker 封面仅搜索图片，条目搜索当前账号的文件或外部卡片并保留分页。
async function loadPicker() {
  if (!pickerMode.value) return
  const requestId = ++pickerRequestId
  pickerLoading.value = true
  pickerError.value = ''
  pickerFiles.value = []
  pickerCards.value = []
  pickerLimitedExternal.value = false
  try {
    const result = await loadTopicCandidates(api, pickerMode.value, pickerSource.value, pickerQuery.value, pickerPage.value)
    if (requestId === pickerRequestId) {
      pickerFiles.value = result.files
      pickerCards.value = result.cards
      pickerHasMore.value = result.hasMore
      pickerLimitedExternal.value = result.limitedExternal
    }
  } catch (reason) {
    if (requestId === pickerRequestId) pickerError.value = reason instanceof Error ? reason.message : '无法查找资料'
  } finally {
    if (requestId === pickerRequestId) pickerLoading.value = false
  }
}

// searchPicker 从第一页开始按当前关键词和来源查找，避免混合旧页结果。
function searchPicker() {
  pickerPage.value = 1
  void loadPicker()
}

// changePickerPage 只在允许的方向切换服务端结果页。
function changePickerPage(step: -1 | 1) {
  if (pickerPage.value + step < 1 || (step === 1 && !pickerHasMore.value)) return
  pickerPage.value += step
  void loadPicker()
}

// chooseFile 对封面只接受图片，对普通条目委托统一的引用新增流程。
function chooseFile(file: Resource) {
  if (pickerMode.value === 'cover') {
    if (file.kind !== 'image') return
    draft.value.cover_file_id = file.id
    coverFailed.value = false
    closePicker()
  } else {
    addEntry({ source: 'file', id: file.id }, file.name)
  }
}

// chooseCard 把站外位置当作私人引用，不直接访问第三方链接。
function chooseCard(card: ExternalResource) {
  addEntry({ source: 'external', id: card.id }, card.title)
}

// addEntry 防止同专题重复引用并即时限制总数；最终归属由服务端复核。
function addEntry(item: InboxSelection, name: string) {
  if (itemCount.value >= 100) {
    pickerError.value = '每个专题最多 100 个条目'
    return
  }
  if (draft.value.sections.some((section) => section.items.some((entry) => entry.source === item.source && entry.id === item.id))) {
    pickerError.value = '这项资料已经在专题中'
    return
  }
  draft.value.sections[pickerSection.value].items.push({ ...item, name })
  closePicker()
}

// saveTopic 校验可见上限后一次性提交完整编排，失败时保留草稿供修正。
async function saveTopic() {
  if (saving.value) return
  const payload = topicPayload(draft.value)
  if (!payload.title || payload.sections.some((section) => !section.title)) {
    detailError.value = '请填写专题标题和每个分区的标题'
    return
  }
  if (payload.sections.length > 10 || itemCount.value > 100) {
    detailError.value = '每个专题最多 10 个分区、100 个条目'
    return
  }
  saving.value = true
  detailError.value = ''
  const editingId = draftId.value
  closePicker()
  try {
    const result = editingId ? await api.updateTopic(editingId, payload) : await api.createTopic(payload)
    draftId.value = result.data.id
    selected.value = result.data
    mode.value = 'view'
    notice.value = '专题已保存'
    void loadTopics()
    void nextTick(() => pageTitle.value?.focus())
  } catch (reason) {
    detailError.value = reason instanceof Error ? reason.message : '无法保存专题'
  } finally {
    saving.value = false
  }
}

// deleteTopic 确认后仅删除编排；资料和外部卡片不会随专题删除。
async function deleteTopic() {
  if (!selected.value || deleting.value || !window.confirm(`删除专题「${selected.value.title}」？原始资料不会删除。`)) return
  deleting.value = true
  detailError.value = ''
  try {
    await api.deleteTopic(selected.value.id)
    selected.value = null
    mode.value = 'list'
    notice.value = '专题已删除，原始资料仍在资料库'
    await loadTopics()
  } catch (reason) {
    detailError.value = reason instanceof Error ? reason.message : '无法删除专题'
  } finally {
    deleting.value = false
  }
}

// 专题面板只在已登录的资料库中挂载，进入时读取本人索引。
onMounted(() => { void loadTopics() })
</script>

<template>
  <section id="topics" class="topics-panel" aria-labelledby="topics-title">
    <header class="topics-heading">
      <div><p class="eyebrow">CURATED IN YOUR ARCHIVE</p><h2 id="topics-title">专题展页</h2><p>把自己的资料排成一页。只有登录的你可以查看。</p></div>
      <button ref="createButton" type="button" class="primary-button" :disabled="saving || deleting" @click="beginCreate">新建专题</button>
    </header>
    <p v-if="notice" class="topics-notice" role="status">{{ notice }}</p>
    <p v-if="error" class="message error" role="alert">{{ error }} <button type="button" class="text-button" @click="loadTopics">重试</button></p>
    <p v-if="loading" class="topics-state" role="status">正在读取专题…</p>
    <p v-else-if="!topics.length && !error" class="topics-state">还没有专题。从一组喜欢的资料开始编排吧。</p>
    <ul v-else-if="topics.length" class="topics-index" aria-label="我的专题">
      <li v-for="topic in topics" :key="topic.id">
        <button type="button" class="topics-index-card" :disabled="saving || deleting" :aria-current="selected?.id === topic.id ? 'true' : undefined" @click="openTopic(topic.id)">
          <span class="topics-index-cover" aria-hidden="true"><img v-if="topic.cover_file_id" :src="previewURL(topic.cover_file_id)" alt="" loading="lazy" /><span v-else>集</span></span>
          <span class="topics-index-copy"><strong>{{ topic.title }}</strong><span>{{ topic.intro || '一页属于自己的资料集' }}</span><small>{{ topic.section_count }} 个分区 · {{ topic.item_count }} 项资料</small></span>
          <span class="topics-index-arrow" aria-hidden="true">↗</span>
        </button>
      </li>
    </ul>

    <p v-if="detailLoading" class="topics-state" role="status">正在打开专题…</p>
    <p v-if="detailError" class="message error" role="alert">{{ detailError }}</p>
    <article v-if="mode === 'view' && selected" class="topics-page" aria-labelledby="topic-page-title">
      <div class="topics-hero">
        <div class="topics-hero-copy"><p class="eyebrow">PRIVATE COLLECTION · {{ selected.section_count }} CHAPTERS</p><h3 id="topic-page-title" ref="pageTitle" tabindex="-1">{{ selected.title }}</h3><p class="topics-intro">{{ selected.intro }}</p><span class="topics-count">{{ selected.item_count }} 项资料</span></div>
        <div v-if="selected.cover_file_id" class="topics-hero-image"><img :src="previewURL(selected.cover_file_id)" :alt="`${selected.title} 的封面`" /></div>
      </div>
      <div class="topics-actions"><button type="button" class="secondary-button" @click="beginEdit">编辑编排</button><button type="button" class="text-button topics-delete" :disabled="deleting" @click="deleteTopic">{{ deleting ? '删除中…' : '删除专题' }}</button></div>
      <p v-if="!selected.sections.length" class="topics-state">此专题还没有分区。可以编辑加入资料。</p>
      <section v-for="(section, sectionIndex) in selected.sections" :key="sectionIndex" class="topics-chapter" :aria-labelledby="`topic-chapter-${sectionIndex}`">
        <div class="topics-chapter-heading"><span class="topics-chapter-number">{{ String(sectionIndex + 1).padStart(2, '0') }}</span><h4 :id="`topic-chapter-${sectionIndex}`">{{ section.title }}</h4><span>{{ section.items.length }} 项</span></div>
        <p v-if="!section.items.length" class="topics-state">这个分区还没有条目。</p>
        <ol v-else class="topics-entries"><li v-for="(item, itemIndex) in section.items" :key="`${item.source}-${item.id}`"><button type="button" class="topics-entry" @click="emit('open', item)"><span class="topics-entry-number">{{ String(itemIndex + 1).padStart(2, '0') }}</span><span class="topics-entry-name">{{ item.name }}</span><small>{{ item.source === 'file' ? '站内文件' : '外部卡片' }}</small><span aria-hidden="true">↗</span></button></li></ol>
      </section>
    </article>

    <section v-if="mode === 'edit'" class="topics-editor" aria-labelledby="topics-editor-title">
      <div class="topics-editor-heading"><p class="eyebrow">COMPOSE A COLLECTION</p><h3 id="topics-editor-title" ref="editorTitle" tabindex="-1">{{ selected ? '编辑专题' : '新建专题' }}</h3><span>{{ draft.sections.length }}/10 分区 · {{ itemCount }}/100 条目</span></div>
      <form @submit.prevent="saveTopic">
        <fieldset class="topics-edit-lock" :disabled="saving">
        <div class="topics-fields"><label>专题标题 <input v-model="draft.title" required maxlength="200" placeholder="例如：我的游戏收藏" /></label><label>简介 <textarea v-model="draft.intro" rows="3" maxlength="2000" placeholder="写下这组资料的线索" /></label></div>
        <div class="topics-cover-edit"><div><strong>图片封面</strong><p>仅可选择当前账号的图片文件。留空也能展出。</p></div><img v-if="draft.cover_file_id && !coverFailed" :src="previewURL(draft.cover_file_id)" alt="当前选择的封面" @error="coverFailed = true" /><div class="topics-actions"><button type="button" class="secondary-button" @click="openPicker('cover')">选择图片</button><button v-if="draft.cover_file_id" type="button" class="text-button" @click="draft.cover_file_id = null; coverFailed = false">移除封面</button></div></div>
        <div class="topics-editor-heading"><h4>分区与条目</h4><button type="button" class="secondary-button" :disabled="draft.sections.length >= 10" @click="addSection">添加分区</button></div>
        <p v-if="!draft.sections.length" class="topics-state">还没有分区。先添加一个章节，再从资料库选条目。</p>
        <fieldset v-for="(section, sectionIndex) in draft.sections" :key="sectionIndex" class="topics-section-edit"><legend>分区 {{ sectionIndex + 1 }}</legend><div class="topics-section-toolbar"><label>分区标题 <input v-model="section.title" required maxlength="200" placeholder="例如：入门资料" /></label><div class="topics-order"><button type="button" :disabled="sectionIndex === 0" :aria-label="`上移分区 ${sectionIndex + 1}`" @click="moveSection(sectionIndex, -1)">上移</button><button type="button" :disabled="sectionIndex === draft.sections.length - 1" :aria-label="`下移分区 ${sectionIndex + 1}`" @click="moveSection(sectionIndex, 1)">下移</button><button type="button" :aria-label="`移除分区 ${sectionIndex + 1}`" @click="removeSection(sectionIndex)">移除</button></div></div>
          <ol class="topics-edit-entries"><li v-for="(item, itemIndex) in section.items" :key="`${item.source}-${item.id}`"><span class="topics-entry-name">{{ item.name || (item.source === 'file' ? '文件' : '外部卡片') }}</span><small>{{ item.source === 'file' ? '文件' : '卡片' }}</small><div class="topics-order"><button type="button" :disabled="itemIndex === 0" :aria-label="`上移条目 ${item.name}`" @click="moveEntry(sectionIndex, itemIndex, -1)">上移</button><button type="button" :disabled="itemIndex === section.items.length - 1" :aria-label="`下移条目 ${item.name}`" @click="moveEntry(sectionIndex, itemIndex, 1)">下移</button><button type="button" :aria-label="`移除条目 ${item.name}`" @click="removeEntry(sectionIndex, itemIndex)">移除</button></div></li></ol>
          <button type="button" class="text-button" :disabled="itemCount >= 100" @click="openPicker('item', sectionIndex)">＋ 从资料库添加条目</button>
        </fieldset>
        <div class="topics-actions topics-save"><button type="submit" class="primary-button" :disabled="saving">{{ saving ? '保存中…' : '保存专题' }}</button><button type="button" class="secondary-button" :disabled="saving" @click="cancelEdit">取消</button></div>
        </fieldset>
      </form>

      <section v-if="pickerMode" class="topics-picker" role="region" aria-labelledby="topic-picker-title" @keydown.esc="closePicker">
        <div class="topics-picker-body"><div class="topics-editor-heading"><h4 id="topic-picker-title">{{ pickerMode === 'cover' ? '选择图片封面' : '添加资料条目' }}</h4><button type="button" class="text-button" @click="closePicker">关闭</button></div>
          <div v-if="pickerMode === 'item'" class="topics-source" role="group" aria-label="资料来源"><button type="button" :aria-pressed="pickerSource === 'file'" @click="pickerSource = 'file'; searchPicker()">站内文件</button><button type="button" :aria-pressed="pickerSource === 'external'" @click="pickerSource = 'external'; searchPicker()">外部卡片</button></div>
          <div class="topics-picker-search"><label for="topic-picker-query">查找{{ pickerMode === 'cover' ? '图片' : '资料' }}</label><input id="topic-picker-query" ref="pickerQueryInput" v-model="pickerQuery" placeholder="输入名称关键词" @keydown.enter.prevent="searchPicker" /><button type="button" class="secondary-button" @click="searchPicker">查找</button></div>
          <p v-if="pickerError" class="message error" role="alert">{{ pickerError }}</p><p v-if="pickerLoading" role="status">正在查找…</p>
          <ul v-else class="topics-picker-results"><li v-for="file in pickerFiles" :key="file.id"><button type="button" @click="chooseFile(file)">{{ file.name }} <small>{{ file.kind === 'image' ? '图片' : '文件' }}</small></button></li><li v-for="card in pickerCards" :key="card.id"><button type="button" @click="chooseCard(card)">{{ card.title }} <small>外部卡片</small></button></li></ul>
          <p v-if="!pickerLoading && !pickerFiles.length && !pickerCards.length && !pickerError" class="topics-state">没有找到可选的资料。</p>
          <p v-if="pickerLimitedExternal && !pickerError" class="topics-picker-hint">这里只显示最近的最多 100 张外部卡片，无法翻页。输入关键词查找其余卡片。</p>
          <div class="topics-order topics-picker-pagination"><button type="button" :disabled="pickerLoading || pickerPage <= 1" @click="changePickerPage(-1)">上一页</button><span>第 {{ pickerPage }} 页</span><button type="button" :disabled="pickerLoading || !pickerHasMore" @click="changePickerPage(1)">下一页</button></div>
        </div>
      </section>
    </section>
  </section>
</template>
