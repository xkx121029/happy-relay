<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import Modal from '../components/Modal.vue'
import TemplateEditor from '../components/TemplateEditor.vue'
import { api, type ModelConfig, type Model, type TemplateSpec, type VideoPreset } from '../api'
import { models, providers, state, toast } from '../store'
import { capLabel, kindLabel, prettyJSON } from '../utils'

const loading = ref(true)
const editing = ref(false)
const saving = ref(false)

interface ModelForm {
  id: number
  provider_id: number
  name: string
  upstream_name: string
  capability: string
  enabled: boolean
  remark: string
  system_prompt: string
  defaultsText: string
  config: ModelConfig
}

function blank(): ModelForm {
  return {
    id: 0,
    provider_id: providers.value[0]?.id || 0,
    name: '',
    upstream_name: '',
    capability: 'chat',
    enabled: true,
    remark: '',
    system_prompt: '',
    defaultsText: '{}',
    config: { chat: {}, image: {}, video: {}, video_query: {} },
  }
}

const form = reactive<ModelForm>(blank())

const capabilities = computed(
  () =>
    state.meta?.capabilities || [
      { value: 'chat', label: '文本对话' },
      { value: 'image', label: '图片生成' },
      { value: 'video', label: '视频生成' },
      { value: 'embedding', label: '文本向量' },
    ],
)

const presets = computed<VideoPreset[]>(() => state.meta?.video_presets || [])

const selectedKind = computed(() => providers.value.find((p) => p.id === form.provider_id)?.kind || '')
const needChat = computed(() => selectedKind.value === 'template' && form.capability === 'chat')
const needImage = computed(() => selectedKind.value === 'template' && form.capability === 'image')
const needVideo = computed(() => form.capability === 'video')

async function load() {
  loading.value = true
  try {
    const [p, m] = await Promise.all([api.listProviders(), api.listModels()])
    providers.value = p
    models.value = m
  } catch (e: any) {
    toast(e.message || '加载失败', 'error')
  } finally {
    loading.value = false
  }
}

onMounted(load)

function ensureSpecs() {
  const kind = selectedKind.value
  if (kind === 'template' && form.capability === 'chat' && !form.config.chat) form.config.chat = {}
  if (kind === 'template' && form.capability === 'image' && !form.config.image) form.config.image = {}
  if (form.capability === 'video') {
    if (!form.config.video) form.config.video = {}
    if (!form.config.video_query) form.config.video_query = {}
  }
}

watch([selectedKind, () => form.capability], ensureSpecs)

function openCreate() {
  if (!providers.value.length) {
    toast('请先在「渠道」里创建上游', 'error')
    return
  }
  const fresh = blank()
  Object.assign(form, fresh)
  form.config = { chat: {}, image: {}, video: {}, video_query: {} }
  ensureSpecs()
  editing.value = true
}

function openEdit(m: Model) {
  Object.assign(form, {
    id: m.id,
    provider_id: m.provider_id,
    name: m.name,
    upstream_name: m.upstream_name,
    capability: m.capability,
    enabled: m.enabled,
    remark: m.remark,
    system_prompt: m.config?.system_prompt || '',
    defaultsText: m.config?.defaults && Object.keys(m.config.defaults).length ? prettyJSON(m.config.defaults) : '{}',
    config: {
      chat: m.config?.chat ? JSON.parse(JSON.stringify(m.config.chat)) : undefined,
      image: m.config?.image ? JSON.parse(JSON.stringify(m.config.image)) : undefined,
      video: m.config?.video ? JSON.parse(JSON.stringify(m.config.video)) : undefined,
      video_query: m.config?.video_query ? JSON.parse(JSON.stringify(m.config.video_query)) : undefined,
    },
  })
  ensureSpecs()
  editing.value = true
}

function isSpecEmpty(s?: TemplateSpec | null): boolean {
  if (!s) return true
  const hasBody = !!s.body && Object.keys(s.body).length > 0
  const hasResp =
    !!s.response &&
    Object.values(s.response).some((v) => v && (typeof v !== 'object' || Object.keys(v as any).length > 0))
  return !s.path && !hasBody && !hasResp
}

function parseObject(text: string): { ok: boolean; value?: Record<string, any> } {
  const t = text.trim()
  if (!t) return { ok: true }
  try {
    const v = JSON.parse(t)
    if (v === null || typeof v !== 'object' || Array.isArray(v)) return { ok: false }
    return { ok: true, value: v }
  } catch {
    return { ok: false }
  }
}

async function save() {
  if (!form.name.trim()) {
    toast('请填写对外模型名', 'error')
    return
  }
  if (!form.provider_id) {
    toast('请选择所属渠道', 'error')
    return
  }
  const parsed = parseObject(form.defaultsText)
  if (!parsed.ok) {
    toast('默认参数必须是合法的 JSON 对象', 'error')
    return
  }

  const config: ModelConfig = {}
  if (form.system_prompt.trim()) config.system_prompt = form.system_prompt.trim()
  if (parsed.value && Object.keys(parsed.value).length) config.defaults = parsed.value
  if (needChat.value && !isSpecEmpty(form.config.chat)) config.chat = form.config.chat!
  if (needImage.value && !isSpecEmpty(form.config.image)) config.image = form.config.image!
  if (needVideo.value && !isSpecEmpty(form.config.video)) config.video = form.config.video!
  if (needVideo.value && !isSpecEmpty(form.config.video_query)) config.video_query = form.config.video_query!

  saving.value = true
  try {
    await api.saveModel({
      id: form.id || undefined,
      provider_id: form.provider_id,
      name: form.name.trim(),
      upstream_name: form.upstream_name.trim() || form.name.trim(),
      capability: form.capability,
      config,
      enabled: form.enabled,
      remark: form.remark.trim(),
    })
    toast('模型已保存')
    editing.value = false
    await load()
  } catch (e: any) {
    toast(e.message || '保存失败', 'error')
  } finally {
    saving.value = false
  }
}

async function remove(m: Model) {
  if (!confirm(`确定删除模型「${m.name}」？`)) return
  try {
    await api.deleteModel(m.id)
    toast('已删除')
    await load()
  } catch (e: any) {
    toast(e.message || '删除失败', 'error')
  }
}

function applyPreset(preset: VideoPreset) {
  form.capability = 'video'
  form.config.video = preset.video ? JSON.parse(JSON.stringify(preset.video)) : {}
  form.config.video_query = preset.video_query ? JSON.parse(JSON.stringify(preset.video_query)) : {}
  toast('已套用预设：' + preset.name)
}

function capBadge(cap: string) {
  return { chat: 'brand', image: 'warn', video: 'ok', embedding: '' }[cap] || ''
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h2>模型</h2>
        <p>对外模型名与上游解耦，可用同一个上游模型配出多个默认参数不同的对外模型。</p>
      </div>
      <div class="row">
        <button class="btn btn-sm" :disabled="loading" @click="load">刷新</button>
        <button class="btn btn-primary btn-sm" @click="openCreate">新建模型</button>
      </div>
    </div>

    <div v-if="loading" class="card"><div class="skeleton" style="width: 40%" /></div>

    <div v-else-if="models.length" class="table-wrap">
      <table class="table">
        <thead>
          <tr>
            <th>对外模型名</th>
            <th>能力</th>
            <th>渠道</th>
            <th>上游模型名</th>
            <th>状态</th>
            <th style="text-align: right">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="m in models" :key="m.id">
            <td>
              <div class="mono" style="font-weight: 600">{{ m.name }}</div>
              <div v-if="m.remark" class="muted" style="font-size: 12px">{{ m.remark }}</div>
            </td>
            <td><span class="badge" :class="capBadge(m.capability)">{{ capLabel(m.capability) }}</span></td>
            <td class="dim">
              {{ m.provider_name || '—' }}
              <span class="muted" style="font-size: 11.5px">（{{ kindLabel(m.provider_kind || '') }}）</span>
            </td>
            <td class="mono dim clamp-1">{{ m.upstream_name }}</td>
            <td>
              <span class="badge" :class="m.enabled ? 'ok' : ''">
                <span class="dot" />{{ m.enabled ? '启用' : '停用' }}
              </span>
            </td>
            <td>
              <div class="row" style="justify-content: flex-end; gap: 6px">
                <button class="btn btn-sm btn-ghost" @click="openEdit(m)">编辑</button>
                <button class="btn btn-sm btn-ghost" style="color: var(--danger)" @click="remove(m)">
                  删除
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-else class="card empty">
      <div class="big">还没有模型</div>
      <div>模型决定对外暴露的名字与能力，客户端只能调用这里启用过的模型。</div>
      <div style="margin-top: 16px"><button class="btn btn-primary" @click="openCreate">新建模型</button></div>
    </div>

    <Modal
      v-if="editing"
      wide
      :title="form.id ? '编辑模型' : '新建模型'"
      subtitle="默认参数会合并进上游请求体，客户端显式传入的同名参数优先。"
      @close="editing = false"
    >
      <div class="grid cols-2" style="gap: 0 18px">
        <div class="field">
          <label>所属渠道</label>
          <select v-model.number="form.provider_id" class="select">
            <option v-for="p in providers" :key="p.id" :value="p.id">{{ p.name }}</option>
          </select>
        </div>
        <div class="field">
          <label>能力</label>
          <select v-model="form.capability" class="select">
            <option v-for="c in capabilities" :key="c.value" :value="c.value">{{ c.label }}</option>
          </select>
          <div class="hint">{{ capabilities.find((c) => c.value === form.capability)?.endpoint }}</div>
        </div>
        <div class="field">
          <label>对外模型名</label>
          <input v-model="form.name" class="input code" placeholder="gpt-4o-mini" />
        </div>
        <div class="field">
          <label>上游模型名</label>
          <input v-model="form.upstream_name" class="input code" placeholder="留空则与对外名一致" />
        </div>
      </div>

      <div class="field">
        <label>系统提示（可选）</label>
        <textarea v-model="form.system_prompt" class="textarea" rows="2" placeholder="会自动加在对话最前面" />
      </div>

      <div class="field">
        <label>默认参数（JSON）</label>
        <textarea v-model="form.defaultsText" class="textarea code" rows="3" placeholder='{"temperature": 0.7}' />
      </div>

      <div v-if="presets.length && form.capability === 'video'" class="field">
        <label>视频模板预设</label>
        <div class="chips">
          <span v-for="p in presets" :key="p.name" class="chip" :title="p.description" @click="applyPreset(p)">
            {{ p.name }}
          </span>
        </div>
      </div>

      <template v-if="needChat">
        <TemplateEditor v-if="form.config.chat" :spec="form.config.chat" label="对话模板（自定义渠道）" />
      </template>
      <template v-if="needImage">
        <TemplateEditor v-if="form.config.image" :spec="form.config.image" label="图片模板（自定义渠道）" />
      </template>
      <template v-if="needVideo">
        <TemplateEditor v-if="form.config.video" :spec="form.config.video" label="视频提交模板" show-response />
        <TemplateEditor
          v-if="form.config.video_query"
          :spec="form.config.video_query"
          label="视频查询模板（异步任务需要）"
          show-response
        />
      </template>

      <div class="field">
        <label>备注</label>
        <input v-model="form.remark" class="input" placeholder="可选说明" />
      </div>

      <label class="switch">
        <input v-model="form.enabled" type="checkbox" />
        <span class="track" />
        <span>启用该模型</span>
      </label>

      <template #footer>
        <button class="btn" @click="editing = false">取消</button>
        <button class="btn btn-primary" :disabled="saving" @click="save">
          {{ saving ? '保存中…' : '保存' }}
        </button>
      </template>
    </Modal>
  </div>
</template>
