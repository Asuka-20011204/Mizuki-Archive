<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { api, type Resource, type ResourceNote, type ResourceNoteInput } from './api'

const props = defineProps<{ resourceId: string; kind: Resource['kind'] }>()
const notes = ref<ResourceNote[]>([])
const loading = ref(false)
const busy = ref(false)
const error = ref('')
const notice = ref('')
const editingId = ref('')
const pageText = ref('')
const excerpt = ref('')
const content = ref('')
const source = ref('')
const savedDraft = ref('')

// currentDraft 保持输入与服务端可编辑字段一致，PDF 以外不发送页码。
const currentDraft = computed<ResourceNoteInput>(() => ({
  page_number: props.kind === 'pdf' && pageText.value ? Number(pageText.value) : null,
  excerpt: excerpt.value, content: content.value, source: source.value,
}))

// hasUnsavedDraft 让父级在切换文件前确认草稿丢失，不以笔记正文代替保存状态。
function hasUnsavedDraft() {
  return JSON.stringify(currentDraft.value) !== savedDraft.value
}
defineExpose({ hasUnsavedDraft })

// resetDraft 清空编辑目标并记录空表单基线，取消编辑不会误报未保存。
function resetDraft() {
  editingId.value = ''
  pageText.value = ''
  excerpt.value = ''
  content.value = ''
  source.value = ''
  savedDraft.value = JSON.stringify(currentDraft.value)
}

// loadNotes 从服务端读取当前文件的私人笔记，失败时保留现有列表供用户重试。
async function loadNotes() {
  loading.value = true
  error.value = ''
  try {
    notes.value = (await api.listResourceNotes(props.resourceId)).data
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '无法读取笔记'
  } finally {
    loading.value = false
  }
}

// editNote 复制服务器返回的注记到表单，避免在取消编辑时修改列表快照。
function editNote(note: ResourceNote) {
  if (hasUnsavedDraft() && !window.confirm('当前笔记草稿尚未保存，改为编辑另一条笔记会丢失草稿。继续吗？')) return
  editingId.value = note.id
  pageText.value = note.page_number ? String(note.page_number) : ''
  excerpt.value = note.excerpt
  content.value = note.content
  source.value = note.source
  savedDraft.value = JSON.stringify(currentDraft.value)
  notice.value = ''
}

// saveNote 显式创建或替换注记；失败时保留草稿和错误提示，不假装已保存。
async function saveNote() {
  if (busy.value) return
  busy.value = true
  error.value = ''
  notice.value = ''
  try {
    const value = currentDraft.value
    if (editingId.value) await api.updateResourceNote(props.resourceId, editingId.value, value)
    else await api.createResourceNote(props.resourceId, value)
    resetDraft()
    notice.value = '笔记已保存，可通过关键词检索'
    await loadNotes()
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '无法保存笔记'
  } finally {
    busy.value = false
  }
}

// removeNote 确认后删除当前笔记；不会删除原文件、摘录来源或其他笔记。
async function removeNote(note: ResourceNote) {
  if (!window.confirm('确定删除这条私人笔记？此操作不可撤销。')) return
  busy.value = true
  error.value = ''
  try {
    await api.deleteResourceNote(props.resourceId, note.id)
    if (editingId.value === note.id) resetDraft()
    notice.value = '笔记已删除'
    await loadNotes()
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '无法删除笔记'
  } finally {
    busy.value = false
  }
}

resetDraft()
onMounted(() => { void loadNotes() })
</script>

<template>
  <section class="resource-notes processing-panel" aria-labelledby="resource-notes-title">
    <div class="detail-section-heading"><div><p class="eyebrow">私人记录</p><h3 id="resource-notes-title">笔记与摘录</h3></div><small>{{ notes.length }}/100</small></div>
    <p class="processing-help">只在当前账号内保存；PDF 可填写页码。摘录、正文和来源说明均可用关键词找回。</p>
    <form class="resource-note-form" @submit.prevent="saveNote">
      <label v-if="kind === 'pdf'">PDF 页码（可选）<input v-model="pageText" type="number" min="1" max="100000" step="1" inputmode="numeric" :disabled="busy" /></label>
      <label>摘录<textarea v-model="excerpt" maxlength="2000" rows="2" placeholder="记录原文片段，可留空" :disabled="busy" /></label>
      <label>私人注记<textarea v-model="content" maxlength="5000" rows="3" placeholder="写下自己的理解或待办" :disabled="busy" /></label>
      <label>来源说明<input v-model="source" maxlength="500" placeholder="例如：第 3 章、会议记录" :disabled="busy" /></label>
      <small>摘录或注记至少填写一项；不会自动从 PDF 抓取页码。</small>
      <div class="resource-note-actions">
        <button type="submit" class="primary-button" :disabled="busy || (!excerpt.trim() && !content.trim())">{{ busy ? '保存中…' : editingId ? '保存修改' : '添加笔记' }}</button>
        <button v-if="editingId || hasUnsavedDraft()" type="button" class="secondary-button" :disabled="busy" @click="resetDraft">取消编辑</button>
      </div>
    </form>
    <p v-if="error" class="message error" role="alert">{{ error }}</p>
    <p v-if="notice" class="message success" role="status">{{ notice }}</p>
    <p v-if="loading" role="status">正在读取笔记…</p>
    <p v-else-if="!notes.length" class="processing-empty">还没有笔记，先记录一段摘录或想法。</p>
    <ul v-else class="resource-note-list" aria-label="当前资料的笔记">
      <li v-for="note in notes" :key="note.id">
        <small v-if="note.page_number">PDF 第 {{ note.page_number }} 页</small>
        <blockquote v-if="note.excerpt">{{ note.excerpt }}</blockquote>
        <p v-if="note.content">{{ note.content }}</p>
        <small v-if="note.source">来源：{{ note.source }}</small>
        <div class="resource-note-actions"><button type="button" class="secondary-button" :disabled="busy" @click="editNote(note)">编辑</button><button type="button" class="danger-button" :disabled="busy" @click="removeNote(note)">删除</button></div>
      </li>
    </ul>
  </section>
</template>
