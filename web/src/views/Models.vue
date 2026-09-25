<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import Modal from '../components/Modal.vue'
import TemplateEditor from '../components/TemplateEditor.vue'
import {
  api,
  type CatalogModel,
  type Model,
  type ModelConfig,
  type ModelInput,
  type Provider,
  type TemplateSpec,
  type VideoPreset,
} from '../api'
import { confirmDialog, models, providers, state, toast } from '../store'
import { capLabel, kindLabel, prettyJSON } from '../utils'

const loading = ref(true)
const editing = ref(false)
const saving = ref(false)
const catalog = ref<CatalogModel[]>([])

interface ModelForm {
  id: number
  inputs: ModelInput[]
  route_strategy: string
  tier: string
  image_mode: string
  context_length: string
  name: string
  capability: string
  enabled: boolean
  remark: string
  system_prompt: string
  defaultsText: string
  config: ModelConfig
}

function blank(): ModelForm {
  const first = providers.value[0]
  return {
    id: 0,
    inputs: first ? [{ provider_id: first.id, upstream_name: '' }] : [],
    route_strategy: 'failover',
    tier: '',
    image_mode: '',
    context_length: '',
    name: '',
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

const tierOptions = [
  { value: '', label: '自动（按模型名推断）' },
  { value: 'light', label: '轻量 · 短问答' },
  { value: 'standard', label: '标准 · 日常任务' },
  { value: 'pro', label: '强力 · 复杂推理' },
]

// currentContext 展示该模型当前生效的上下文窗口：
// 优先用表单里填写的值，其次是模型库探测值（都没填时后端还会按模型名推断）。
const currentContext = computed(() => {
  const typed = Number(form.context_length)
  if (typed > 0) return Math.round(typed)
  return models.value.find((m) => m.id === form.id)?.catalog_context_length || 0
})

const boundProviders = computed<Provider[]>(() =>
  form.inputs
    .map((i) => providers.value.find((p) => p.id === i.provider_id))
    .filter((p): p is Provider => !!p),
)

const hasTemplateProvider = computed(() => boundProviders.value.some((p) => p.kind === 'template'))
const needChat = computed(() => hasTemplateProvider.value && form.capability === 'chat')
const needImage = computed(() => hasTemplateProvider.value && form.capability === 'image')
const needVideo = computed(() => form.capability === 'video')

function catalogOf(providerID: number): CatalogModel[] {
  return catalog.value.filter((c) => c.provider_id === providerID && c.enabled)
}

async function load() {
  loading.value = true
  try {
    const [p, m, c] = await Promise.all([api.listProviders(), api.listModels(), api.listCatalog()])
    providers.value = p
    models.value = m
    catalog.value = c
  } catch (e: any) {
    toast(e.message || '加载失败', 'error')
  } finally {
    loading.value = false
  }
}

onMounted(load)

function ensureSpecs() {
  if (hasTemplateProvider.value && form.capability === 'chat' && !form.config.chat) form.config.chat = {}
  if (hasTemplateProvider.value && form.capability === 'image' && !form.config.image) form.config.image = {}
  if (form.capability === 'video') {
    if (!form.config.video) form.config.video = {}
    if (!form.config.video_query) form.config.video_query = {}
  }
}

watch([hasTemplateProvider, () => form.capability], ensureSpecs)

function addInput() {
  if (!providers.value.length) {
    toast('请先在「渠道」里创建上游', 'error')
    return
  }
  const used = new Set(form.inputs.map((i) => i.provider_id))
  const next = providers.value.find((p) => !used.has(p.id)) || providers.value[0]
  form.inputs.push({ provider_id: next.id, upstream_name: '' })
}

function moveInput(index: number, delta: number) {
  const target = index + delta
  if (target < 0 || target >= form.inputs.length) return
  const arr = form.inputs
  const tmp = arr[index]
  arr[index] = arr[target]
  arr[target] = tmp
}

function onProviderChange(input: ModelInput) {
  // 换了渠道，上游模型名一般也要换，交给用户重新选。
  input.upstream_name = ''
}

function openCreate() {
  if (!providers.value.length) {
    toast('请先在「渠道」里创建上游', 'error')
    return
  }
  Object.assign(form, blank())
  form.config = { chat: {}, image: {}, video: {}, video_query: {} }
  ensureSpecs()
  editing.value = true
}

function openEdit(m: Model) {
  let inputs: ModelInput[] = []
  if (m.inputs && m.inputs.length) {
    inputs = m.inputs.map((i) => ({
      provider_id: i.provider_id,
      upstream_name: i.upstream_name || m.upstream_name,
    }))
  } else if (m.provider_ids && m.provider_ids.length) {
    inputs = m.provider_ids.map((id) => ({ provider_id: id, upstream_name: m.upstream_name }))
  }
  Object.assign(form, {
    id: m.id,
    inputs,
    route_strategy: m.config?.route_strategy || 'failover',
    tier: m.config?.tier || '',
    image_mode: m.config?.image_mode || '',
    context_length: m.config?.context_length ? String(m.config.context_length) : '',
    name: m.name,
    capability: m.capability,
    enabled: m.enabled,
    remark: m.remark,
    system_prompt: m.config?.system_prompt || '',
    defaultsText:
      m.config?.defaults && Object.keys(m.config.defaults).length ? prettyJSON(m.config.defaults) : '{}',
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
  const inputs = form.inputs
    .filter((i) => i.provider_id > 0 && i.upstream_name.trim())
    .map((i) => ({ provider_id: i.provider_id, upstream_name: i.upstream_name.trim() }))
  if (!inputs.length) {
    toast('请至少配置一个上游（渠道 + 上游模型名）', 'error')
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
  if (inputs.length > 1) config.route_strategy = form.route_strategy
  if (form.tier) config.tier = form.tier
  if (form.capability === 'image' && form.image_mode) config.image_mode = form.image_mode
  // 上下文窗口：留空表示交给模型库探测值与启发式推断。
  const ctx = Number(form.context_length)
  if (ctx > 0) config.context_length = Math.round(ctx)
  if (needChat.value && !isSpecEmpty(form.config.chat)) config.chat = form.config.chat!
  if (needImage.value && !isSpecEmpty(form.config.image)) config.image = form.config.image!
  if (needVideo.value && !isSpecEmpty(form.config.video)) config.video = form.config.video!
  if (needVideo.value && !isSpecEmpty(form.config.video_query))
    config.video_query = form.config.video_query!

  saving.value = true
  try {
    await api.saveModel({
      id: form.id || undefined,
      inputs,
      upstream_name: inputs[0].upstream_name,
      name: form.name.trim(),
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
  const ok = await confirmDialog({
    title: '删除模型',
    message: `确定删除模型「${m.name}」？使用它的客户端将立即无法调用。`,
    confirmText: '删除',
  })
  if (!ok) return
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

function inputSummary(m: Model) {
  const list = m.inputs?.length ? m.inputs : []
  if (!list.length) return m.upstream_name || '—'
  const first = list[0]
  const name = providers.value.find((p) => p.id === first.provider_id)?.name || `#${first.provider_id}`
  return `${name} · ${first.upstream_name}`
}

function inputCount(m: Model): number {
  if (m.inputs?.length) return m.inputs.length
  return m.provider_ids?.length || 1
}

function tierLabel(m: Model) {
  const t = m.config?.tier
  if (!t) return '自动'
  return { light: '轻量', standard: '标准', pro: '强力' }[t] || t
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h2>模型</h2>
        <p>
          对外模型可以组合多个上游（每个上游是「渠道 + 模型名」），失败时自动切换；上游模型名可从
          <router-link to="/catalog">模型库</router-link> 里挑。
        </p>
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
            <th>档位</th>
            <th>上游</th>
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
            <td><span class="badge">{{ tierLabel(m) }}</span></td>
            <td>
              <div class="dim clamp-1">{{ inputSummary(m) }}</div>
              <div v-if="inputCount(m) > 1" class="muted" style="font-size: 11.5px">
                共 {{ inputCount(m) }} 个上游，按顺序自动切换
              </div>
            </td>
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
      <div>对外模型决定客户端能调用什么；上游模型名可以从「模型库」里挑。</div>
      <div style="margin-top: 16px"><button class="btn btn-primary" @click="openCreate">新建模型</button></div>
    </div>

    <Modal
      v-if="editing"
      wide
      :title="form.id ? '编辑模型' : '新建模型'"
      subtitle="可以配置多个上游，按顺序尝试，失败自动切换。"
      @close="editing = false"
    >
      <div class="grid cols-2" style="gap: 0 18px">
        <div class="field">
          <label>对外模型名</label>
          <input v-model="form.name" class="input code" placeholder="gpt-4o-mini" />
        </div>
        <div class="field">
          <label>能力</label>
          <select v-model="form.capability" class="select">
            <option v-for="c in capabilities" :key="c.value" :value="c.value">{{ c.label }}</option>
          </select>
          <div class="hint">{{ capabilities.find((c) => c.value === form.capability)?.endpoint }}</div>
        </div>
      </div>

      <div class="field">
        <label>上游列表（按顺序尝试，失败自动切换）</label>
        <div
          v-for="(inp, i) in form.inputs"
          :key="i"
          class="row"
          style="align-items: center; gap: 8px; margin-bottom: 8px; flex-wrap: nowrap"
        >
          <span class="badge brand">{{ i + 1 }}</span>
          <select
            v-model.number="inp.provider_id"
            class="select"
            style="flex: 1; min-width: 120px"
            @change="onProviderChange(inp)"
          >
            <option v-for="p in providers" :key="p.id" :value="p.id">{{ p.name }}</option>
          </select>
          <input
            v-model="inp.upstream_name"
            class="input code"
            style="flex: 1.4; min-width: 140px"
            :list="`cat-${i}`"
            placeholder="上游模型名（可从模型库选）"
          />
          <datalist :id="`cat-${i}`">
            <option v-for="c in catalogOf(inp.provider_id)" :key="c.id" :value="c.model_id" />
          </datalist>
          <button class="btn btn-sm btn-ghost" :disabled="i === 0" @click="moveInput(i, -1)">上移</button>
          <button
            class="btn btn-sm btn-ghost"
            :disabled="i === form.inputs.length - 1"
            @click="moveInput(i, 1)"
          >
            下移
          </button>
          <button class="btn btn-sm btn-ghost" style="color: var(--danger)" @click="form.inputs.splice(i, 1)">
            移除
          </button>
        </div>
        <div v-if="!form.inputs.length" class="hint">还没有上游，请点击下面的按钮添加。</div>
        <button class="btn btn-sm" @click="addInput">+ 添加上游</button>
        <div class="hint">
          下拉里带出的是模型库里该渠道探测到的模型；也可以直接手填。同一个模型在不同渠道上的上游名可以不同。
        </div>
      </div>

      <div class="grid" :class="form.capability === 'image' ? 'cols-3' : 'cols-2'" style="gap: 0 14px">
        <div class="field">
          <label>自动切换策略</label>
          <select v-model="form.route_strategy" class="select" :disabled="form.inputs.length < 2">
            <option value="failover">主备（按顺序，失败切换）</option>
            <option value="round_robin">轮询（依次轮流使用）</option>
            <option value="random">随机</option>
          </select>
        </div>
        <div class="field">
          <label>档位（供 auto 自动模型挑选）</label>
          <select v-model="form.tier" class="select">
            <option v-for="t in tierOptions" :key="t.value" :value="t.value">{{ t.label }}</option>
          </select>
        </div>
        <div v-if="form.capability === 'image'" class="field">
          <label>图片请求模式</label>
          <select v-model="form.image_mode" class="select">
            <option value="">标准（参数放顶层）</option>
            <option value="extra_body">extra_body（Agnes 等）</option>
          </select>
          <div class="hint">
            Agnes Image 2.1 Flash 要求 response_format 与参考图放在 extra_body 里，选这一项即可兼容。
          </div>
        </div>
      </div>

      <div class="field">
        <label>上下文窗口（token）</label>
        <input
          v-model="form.context_length"
          class="input"
          type="number"
          min="0"
          placeholder="留空自动推断"
        />
        <div class="hint">
          用于调用前的容量校验与 auto 路由。留空时依次回退到模型库探测值、按上游模型名推断。
          <template v-if="currentContext > 0">
            当前生效约 {{ currentContext.toLocaleString() }} tokens。
          </template>
          <template v-else>当前无法确定，将不做长度校验。</template>
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
