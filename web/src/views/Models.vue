<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import Modal from '../components/Modal.vue'
import TemplateEditor from '../components/TemplateEditor.vue'
import {
  api,
  type AICreateResponse,
  type AggCreateItem,
  type AggDeclareItem,
  type CatalogModel,
  type Model,
  type ModelConfig,
  type ModelInput,
  type Provider,
  type TemplateSpec,
  type UpstreamStat,
  type VideoPreset,
} from '../api'
import { confirmDialog, models, providers, state, toast } from '../store'
import { capLabel, prettyJSON } from '../utils'

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
  rpm: string
  tpm: string
  price_in: string
  price_out: string
  price_image: string
  config: ModelConfig
}

function blank(): ModelForm {
  const first = providers.value[0]
  return {
    id: 0,
    inputs: first ? [{ provider_id: first.id, upstream_name: '', input_modalities: [] }] : [],
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
    rpm: '',
    tpm: '',
    price_in: '',
    price_out: '',
    price_image: '',
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
    // 拉全量（含 usable 标记），由前端做「可用/不可用」筛选与统计；
    // 其余页面（调试台、秘钥等）默认拿到的就是已过滤的可用列表。
    const [p, m, c] = await Promise.all([
      api.listProviders(),
      api.listModels({ all: true }),
      api.listCatalog(),
    ])
    providers.value = p
    models.value = m
    catalog.value = c
  } catch (e: any) {
    toast(e.message || '加载失败', 'error')
  } finally {
    loading.value = false
  }
}

// 可用性筛选：默认只看可用的，调不通的模型不混在列表里误导人；
// 切到「不可用」或「全部」可以管理它们（配好渠道后自动恢复可用）。
const usableFilter = ref<'usable' | 'unusable' | 'all'>('usable')
const usableCount = computed(() => models.value.filter((m) => m.usable !== false).length)
const shownModels = computed(() => {
  if (usableFilter.value === 'all') return models.value
  if (usableFilter.value === 'unusable') return models.value.filter((m) => m.usable === false)
  return models.value.filter((m) => m.usable !== false)
})

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
  form.inputs.push({ provider_id: next.id, upstream_name: '', input_modalities: [] })
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
      input_modalities: i.input_modalities ? [...i.input_modalities] : [],
    }))
  } else if (m.provider_ids && m.provider_ids.length) {
    inputs = m.provider_ids.map((id) => ({ provider_id: id, upstream_name: m.upstream_name, input_modalities: [] }))
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
    rpm: m.config?.rpm ? String(m.config.rpm) : '',
    tpm: m.config?.tpm ? String(m.config.tpm) : '',
    price_in: m.config?.price_in ? String(m.config.price_in) : '',
    price_out: m.config?.price_out ? String(m.config.price_out) : '',
    price_image: m.config?.price_image ? String(m.config.price_image) : '',
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
    .map((i) => ({
      provider_id: i.provider_id,
      upstream_name: i.upstream_name.trim(),
      // 模态留空 = 交给后端按模型名自动推断；声明了才覆盖。
      input_modalities: i.input_modalities?.length ? i.input_modalities : undefined,
    }))
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
  // 限速与单价：留空或 0 表示不设限 / 不计费。
  const rpm = Number(form.rpm)
  if (rpm > 0) config.rpm = rpm
  const tpm = Number(form.tpm)
  if (tpm > 0) config.tpm = tpm
  const priceIn = Number(form.price_in)
  if (priceIn > 0) config.price_in = priceIn
  const priceOut = Number(form.price_out)
  if (priceOut > 0) config.price_out = priceOut
  const priceImage = Number(form.price_image)
  if (priceImage > 0) config.price_image = priceImage
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

// ---------- 多模态聚合：把多个单模态上游挂到同一个对外模型 ----------
//
// 引擎按「请求实际用到的输入模态」过滤目标（expandTargets + inputModalitiesMatch）：
// 纯文本请求可以命中任何上游；带图/带音频的请求只会路由到声明（或按名称推断）
// 支持对应模态的上游。因此把「纯文本模型」排在前面、「多模态模型」排在后面，
// 一个对外模型名就能同时服务两类请求，并保留渠道内故障转移。

const MODALITY_OPTIONS = [
  { value: 'text', label: '文本' },
  { value: 'image', label: '图片' },
  { value: 'audio', label: '音频' },
]

function toggleInputModality(inp: ModelInput, v: string) {
  const set = new Set(inp.input_modalities || [])
  if (set.has(v)) set.delete(v)
  else set.add(v)
  inp.input_modalities = Array.from(set)
}

function modalityLabel(inp: ModelInput): string {
  const list = inp.input_modalities || []
  if (!list.length) return '自动推断'
  return list
    .map((v) => MODALITY_OPTIONS.find((o) => o.value === v)?.label || v)
    .join('+')
}

// aggregationNote 生成模型列表里的聚合路由说明；没有声明模态时返回空串。
function aggregationNote(m: Model): string {
  const list = m.inputs || []
  if (m.capability !== 'chat' || list.length < 2) return ''
  const declared = list.filter((i) => i.input_modalities?.length)
  if (!declared.length) return ''
  const parts: string[] = []
  const textOnly = declared.filter((i) => i.input_modalities!.length === 1 && i.input_modalities![0] === 'text')
  const vision = declared.filter((i) => i.input_modalities!.includes('image'))
  const audio = declared.filter((i) => i.input_modalities!.includes('audio'))
  if (textOnly.length) parts.push('纯文本 → ' + textOnly.map((i) => i.upstream_name).join('/'))
  if (vision.length) parts.push('带图 → ' + vision.map((i) => i.upstream_name).join('/'))
  if (audio.length) parts.push('带音频 → ' + audio.map((i) => i.upstream_name).join('/'))
  return parts.join('；')
}

function inputCount(m: Model): number {
  if (m.inputs?.length) return m.inputs.length
  return m.provider_ids?.length || 1
}

// ---------- 多模态自动聚合 ----------
// 扫描模型库，把「能读图但没被利用起来」的视觉上游自动变成聚合配置：
// 已被模型引用的 → 补声明模态；没被引用的 → 新建聚合模型（视觉主上游 + 纯文本故障转移）。
// 先出计划、前端勾选、再应用，避免一键改一堆。

const aggOpen = ref(false)
const aggLoading = ref(false)
const aggApplying = ref(false)
const aggPlan = ref<{ declares: AggDeclareItem[]; creates: AggCreateItem[] } | null>(null)
const aggSelDeclares = ref<Record<string, boolean>>({})
const aggSelCreates = ref<Record<string, boolean>>({})

const aggSelCount = computed(
  () =>
    Object.values(aggSelDeclares.value).filter(Boolean).length +
    Object.values(aggSelCreates.value).filter(Boolean).length,
)

async function openAggregate() {
  aggOpen.value = true
  aggLoading.value = true
  try {
    const plan = await api.autoAggregate({})
    aggPlan.value = plan
    aggSelDeclares.value = Object.fromEntries(plan.declares.map((d) => [d.key, true]))
    aggSelCreates.value = Object.fromEntries(plan.creates.map((c) => [c.key, true]))
  } catch (e: any) {
    toast(e.message || '生成聚合计划失败', 'error')
    aggOpen.value = false
  } finally {
    aggLoading.value = false
  }
}

async function applyAggregate() {
  if (!aggPlan.value) return
  const declares = aggPlan.value.declares.filter((d) => aggSelDeclares.value[d.key]).map((d) => d.key)
  const creates = aggPlan.value.creates.filter((c) => aggSelCreates.value[c.key]).map((c) => c.key)
  if (!declares.length && !creates.length) {
    toast('请至少勾选一项', 'error')
    return
  }
  aggApplying.value = true
  try {
    const res = await api.autoAggregate({ apply: true, declares, creates })
    toast(`已应用 ${res.applied ?? 0} 项聚合配置`)
    aggOpen.value = false
    await load()
  } catch (e: any) {
    toast(e.message || '应用失败', 'error')
  } finally {
    aggApplying.value = false
  }
}

function modalityText(list: string[]): string {
  return list.map((v) => MODALITY_OPTIONS.find((o) => o.value === v)?.label || v).join('+')
}

// ---------- 上游健康画像 ----------
// 每次真实调用的滚动累计（成功率 / EWMA 延迟 / 连续失败），重启不丢。
// 路由自学习的数据来源：连续失败 >= 3 的上游会被自动排到尝试队列尾部。

const profileOpen = ref(false)
const profileLoading = ref(false)
const profileList = ref<UpstreamStat[]>([])

async function openProfile() {
  profileOpen.value = true
  profileLoading.value = true
  try {
    profileList.value = await api.upstreamStats()
  } catch (e: any) {
    toast(e.message || '加载画像失败', 'error')
  } finally {
    profileLoading.value = false
  }
}

function rateText(s: UpstreamStat): string {
  if (s.success_rate === undefined || s.success_rate < 0) return '—'
  return s.success_rate.toFixed(1) + '%'
}

function rateClass(s: UpstreamStat): string {
  const r = s.success_rate ?? -1
  if (r < 0) return ''
  if (r >= 95) return 'ok'
  if (r >= 70) return ''
  return 'danger'
}

function healthLabel(s: UpstreamStat): string {
  if (s.consec_fails >= 3) return '连续失败 ' + s.consec_fails + ' 次'
  if (s.consec_fails > 0) return '最近失败 ' + s.consec_fails + ' 次'
  if (s.ok_count > 0) return '正常'
  return '新上游'
}

function healthClass(s: UpstreamStat): string {
  if (s.consec_fails >= 3) return 'danger'
  if (s.consec_fails > 0) return 'warn'
  if (s.ok_count > 0) return 'ok'
  return 'brand'
}

// ---------- AI 建模：自然语言创建模型 ----------
// 主 AI（系统级模型）读取渠道/模型库/现有模型上下文，把「帮我把所有NIM的模型
// 整合为一个NIMout模型」这类自然语言转成模型配置草案；后端已做双重校验
// （渠道存在 + 上游在模型库），前端预览确认后走标准 saveModel 落库。

const aiOpen = ref(false)
const aiBusy = ref(false)
const aiCreating = ref(false)
const aiPrompt = ref('')
const aiResult = ref<AICreateResponse | null>(null)
const aiError = ref('')

const aiExamples = [
  '把所有 NIM 的模型整合为一个 NIMout 模型',
  '给智谱的全部文本模型建一个聚合，纯文本走便宜的上游',
  '用硅基流动的视觉模型建一个带图对话模型',
]

function openAI() {
  aiOpen.value = true
  aiResult.value = null
  aiError.value = ''
}

async function runAI(prompt?: string) {
  const text = (prompt ?? aiPrompt.value).trim()
  if (!text) {
    toast('先用一句话描述你想要的模型', 'error')
    return
  }
  aiBusy.value = true
  aiError.value = ''
  try {
    aiResult.value = await api.aiCreateModel(text)
    if (prompt !== undefined) aiPrompt.value = prompt
  } catch (e: any) {
    aiError.value = e.message || 'AI 调用失败'
  } finally {
    aiBusy.value = false
  }
}

async function createAIModels() {
  if (!aiResult.value?.models?.length) return
  aiCreating.value = true
  let ok = 0
  for (const m of aiResult.value.models) {
    try {
      await api.saveModel({ ...m, id: 0 } as any)
      ok++
    } catch (e: any) {
      toast(`创建「${m.name}」失败：${e.message || '未知错误'}`, 'error')
    }
  }
  aiCreating.value = false
  if (ok) {
    toast(`已创建 ${ok} 个模型`)
    aiOpen.value = false
    await load()
  }
}

function capName(cap: string): string {
  return { chat: '文本对话', image: '图片生成', video: '视频生成', embedding: '文本向量', speech: '语音合成', transcription: '语音识别' }[cap] || cap
}

function modalityShort(list?: string[]): string {
  if (!list?.length) return '自动推断'
  return list.map((v) => ({ text: '文', image: '图', audio: '音', video: '视', file: '文件' }[v] || v)).join('+')
}

// ---------- 一键连通测试 ----------
// 走调试台入口对模型发一条最小请求，验证「对外模型 → 上游 → 密钥」整条链路，
// 并回显实际命中的渠道与耗时。仅支持 chat（image 会真实出图、花钱，留去调试台试）。
const testResults = ref<
  Record<string, { state: 'running' | 'ok' | 'fail'; ms?: number; provider?: string; error?: string }>
>({})

async function testModel(m: Model) {
  const cur = testResults.value[m.name]
  if (cur?.state === 'running') return
  if (m.capability !== 'chat') {
    toast('一键测试目前只支持文本模型，其他能力请去调试台手动试', 'error')
    return
  }
  testResults.value = { ...testResults.value, [m.name]: { state: 'running' } }
  const started = performance.now()
  try {
    const res = await api.playgroundChat({
      model: m.name,
      stream: false,
      messages: [{ role: 'user', content: 'ping' }],
      max_tokens: 8,
    })
    testResults.value = {
      ...testResults.value,
      [m.name]: {
        state: 'ok',
        ms: Math.round(performance.now() - started),
        provider: res?.provider || '',
      },
    }
  } catch (e: any) {
    testResults.value = {
      ...testResults.value,
      [m.name]: {
        state: 'fail',
        ms: Math.round(performance.now() - started),
        error: e.message || '测试失败',
      },
    }
  }
}

function testTip(m: Model): string {
  const r = testResults.value[m.name]
  if (!r) return '发一条 ping 验证整条链路'
  if (r.state === 'running') return '测试中…'
  if (r.state === 'ok') return `命中 ${r.provider || '上游'} · ${r.ms} ms`
  return r.error || '测试失败'
}

function tierLabel(m: Model) {
  const t = m.config?.tier
  if (!t) return '自动'
  return tierName(t)
}

function tierName(t: string) {
  return ({ light: '轻量', standard: '标准', pro: '强力' })[t] || t
}

// ---------- 智力测评 ----------

// 主模型当考官：三道固定题（数学 / 逻辑 / 指令遵循）考每个文本模型，
// 主模型按参考答案打 0-100 分，档位随分数写回，auto 同档位内优先选高分模型。
const benchOpen = ref(false)
const benchBusy = ref('') // 正在测评的模型名；'__all__' 表示全部测评中
const benchErr = ref<Record<string, string>>({})
const systemModel = computed(() => state.meta?.settings?.system_model || '')

// 被测对象：启用且可用的文本模型，排除 auto（是规则不是模型）与主模型（考官本人）。
const benchModels = computed(() =>
  models.value.filter(
    (m) =>
      m.enabled &&
      m.usable !== false &&
      m.capability === 'chat' &&
      m.name !== 'auto' &&
      m.name !== systemModel.value,
  ),
)

function openBench() {
  if (!systemModel.value) {
    toast('请先在「设置 → 通用 → 系统级 AI 模型」里指定主模型，由它担任考官', 'error')
    return
  }
  benchErr.value = {}
  benchOpen.value = true
}

async function runBench(name: string) {
  benchErr.value = { ...benchErr.value, [name]: '' }
  try {
    const r = await api.benchmarkModel(name)
    // 测评产物就地写进本地列表，省一次整页刷新。
    const m = models.value.find((x) => x.name === name)
    if (m) m.config = { ...m.config, tier: r.tier, intelligence: r.score }
    toast(`「${name}」测评完成：${r.score} 分（${tierName(r.tier)}）`)
  } catch (e: any) {
    benchErr.value = { ...benchErr.value, [name]: e.message || '测评失败' }
  }
}

async function runBenchAll() {
  if (!benchModels.value.length) return
  benchBusy.value = '__all__'
  try {
    for (const m of benchModels.value) {
      benchBusy.value = m.name
      await runBench(m.name)
    }
  } finally {
    benchBusy.value = ''
  }
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
        <RouterLink class="btn btn-sm" to="/builtin">内置模型</RouterLink>
        <button class="btn btn-sm" @click="openBench">智力测评</button>
        <button class="btn btn-sm" @click="openAggregate">多模态自动聚合</button>
        <button class="btn btn-sm" @click="openProfile">上游画像</button>
        <button class="btn btn-primary btn-sm" @click="openAI">AI 建模</button>
        <button class="btn btn-primary btn-sm" @click="openCreate">新建模型</button>
      </div>
    </div>

    <div class="toolbar">
      <div class="row" style="gap: 6px">
        <button
          class="btn btn-sm"
          :class="usableFilter === 'usable' ? '' : 'btn-ghost'"
          @click="usableFilter = 'usable'"
        >
          可用（{{ usableCount }}）
        </button>
        <button
          class="btn btn-sm"
          :class="usableFilter === 'unusable' ? '' : 'btn-ghost'"
          @click="usableFilter = 'unusable'"
        >
          不可用（{{ models.length - usableCount }}）
        </button>
        <button
          class="btn btn-sm"
          :class="usableFilter === 'all' ? '' : 'btn-ghost'"
          @click="usableFilter = 'all'"
        >
          全部
        </button>
      </div>
      <span class="muted" style="font-size: 12px">
        不可用 = 绑定渠道都没配密钥、连通测试也没通过过；配好渠道后会自动恢复可用。
      </span>
    </div>

    <div v-if="loading" class="table-wrap">
      <table class="table">
        <tbody>
          <tr v-for="i in 4" :key="i">
            <td v-for="j in 6" :key="j"><div class="skeleton" /></td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-else-if="shownModels.length" class="table-wrap">
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
          <tr v-for="m in shownModels" :key="m.id">
            <td>
              <div class="mono" style="font-weight: 600">{{ m.name }}</div>
              <div v-if="m.remark" class="muted" style="font-size: 11.5px">{{ m.remark }}</div>
            </td>
            <td><span class="badge" :class="capBadge(m.capability)">{{ capLabel(m.capability) }}</span></td>
            <td>
              <span class="badge">{{ tierLabel(m) }}</span>
              <span v-if="m.config?.intelligence" class="muted" style="font-size: 11px; margin-left: 4px">
                {{ m.config.intelligence }} 分
              </span>
            </td>
            <td>
              <div class="dim clamp-1">{{ inputSummary(m) }}</div>
              <div v-if="inputCount(m) > 1" class="muted" style="font-size: 11.5px">
                共 {{ inputCount(m) }} 个上游，按顺序自动切换
              </div>
              <div v-if="aggregationNote(m)" class="badge brand" style="margin-top: 4px; font-weight: 400">
                {{ aggregationNote(m) }}
              </div>
            </td>
            <td>
              <span v-if="!m.enabled" class="badge">
                <span class="dot" />停用
              </span>
              <span v-else-if="m.usable === false" class="badge danger" title="绑定渠道都没配密钥且连通测试未通过">
                <span class="dot" />不可用
              </span>
              <span v-else class="badge ok">
                <span class="dot" />可用
              </span>
            </td>
            <td>
              <div class="row" style="justify-content: flex-end; gap: 4px">
                <button
                  class="btn btn-sm btn-ghost"
                  :disabled="testResults[m.name]?.state === 'running'"
                  :title="testTip(m)"
                  @click="testModel(m)"
                >
                  {{ testResults[m.name]?.state === 'running' ? '测试中…' : '测试' }}
                </button>
                <button class="btn btn-sm btn-ghost" @click="openEdit(m)">编辑</button>
                <button class="btn btn-sm btn-ghost" style="color: var(--danger)" @click="remove(m)">
                  删除
                </button>
              </div>
              <div
                v-if="testResults[m.name] && testResults[m.name].state !== 'running'"
                class="muted"
                style="font-size: 11px; text-align: right; margin-top: 2px"
                :style="{ color: testResults[m.name].state === 'fail' ? 'var(--danger)' : 'var(--ok)' }"
              >
                {{ testTip(m) }}
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-else-if="models.length" class="card empty">
      <div class="empty-icon">◇</div>
      <div class="big">当前筛选下没有模型</div>
      <div v-if="usableFilter === 'usable'">
        所有模型都不可用：给绑定渠道配上密钥（或通过连通测试）后它们会自动恢复。
      </div>
      <div v-else>换一个筛选条件试试。</div>
      <div class="empty-cta">
        <button class="btn btn-sm" @click="usableFilter = 'all'">查看全部</button>
        <RouterLink class="btn btn-sm btn-ghost" to="/providers">去渠道页配置密钥</RouterLink>
      </div>
    </div>

    <div v-else class="card empty">
      <div class="empty-icon">◇</div>
      <div class="big">还没有模型</div>
      <div>对外模型决定客户端能调用什么；上游模型名可以从「模型库」里挑。</div>
      <div class="empty-cta">
        <button class="btn btn-primary btn-sm" @click="openCreate">新建模型</button>
        <RouterLink class="btn btn-sm btn-ghost" to="/providers">先去添加渠道</RouterLink>
      </div>
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
          class="upstream-row"
        >
          <div class="row" style="align-items: center; gap: 8px; flex-wrap: nowrap">
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
          <div class="modality-row">
            <span class="hint" style="flex: none">输入模态：</span>
            <span
              v-for="mo in MODALITY_OPTIONS"
              :key="mo.value"
              class="modality-chip"
              :class="{ active: inp.input_modalities?.includes(mo.value) }"
              @click="toggleInputModality(inp, mo.value)"
            >{{ mo.label }}</span>
            <span class="hint">{{ modalityLabel(inp) }}</span>
          </div>
        </div>
        <div v-if="!form.inputs.length" class="hint">还没有上游，请点击下面的按钮添加。</div>
        <button class="btn btn-sm" @click="addInput">+ 添加上游</button>
        <div class="hint">
          下拉里带出的是模型库里该渠道探测到的模型；也可以直接手填。同一个模型在不同渠道上的上游名可以不同。
          为每个上游声明输入模态后，可把纯文本模型和多模态模型聚合成一个对外模型：纯文本请求路由到纯文本上游，带图/带音频请求自动路由到声明了对应模态的上游。
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

      <h4 style="margin: 18px 0 8px; font-size: 13px; opacity: 0.85">限速与计费（可选）</h4>
      <div class="grid cols-2" style="gap: 0 14px">
        <div class="field">
          <label>每分钟请求上限 RPM</label>
          <input v-model="form.rpm" class="input" type="number" min="0" placeholder="0 = 不限制" />
        </div>
        <div class="field">
          <label>每分钟 Token 上限 TPM</label>
          <input v-model="form.tpm" class="input" type="number" min="0" placeholder="0 = 不限制" />
        </div>
        <div class="field">
          <label>输入单价（每 1K token）</label>
          <input v-model="form.price_in" class="input" type="number" min="0" step="0.0001" placeholder="0 = 不计费" />
        </div>
        <div class="field">
          <label>输出单价（每 1K token）</label>
          <input v-model="form.price_out" class="input" type="number" min="0" step="0.0001" placeholder="0 = 不计费" />
        </div>
        <div class="field" style="margin-bottom: 0">
          <label>图片单价（每张）</label>
          <input v-model="form.price_image" class="input" type="number" min="0" step="0.0001" placeholder="0 = 不计费" />
        </div>
      </div>
      <div class="hint" style="margin-top: 6px">
        RPM / TPM 用于按对外模型维度限流（超出返回 429）；单价用于估算每次调用成本并记进调用日志。
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

    <!-- 智力测评：主模型当考官，实测档位写回模型，auto 同档位优先选高分 -->
    <Modal
      v-if="benchOpen"
      title="模型智力测评"
      wide
      :subtitle="`考官：主模型「${systemModel}」。三道固定题（数学 / 逻辑 / 指令遵循）按表现打 0-100 分，档位随分数自动标记：≥75 强力、≥45 标准、其余轻量。`"
      @close="benchOpen = false"
    >
      <div class="row" style="margin-bottom: 12px">
        <button
          class="btn btn-primary btn-sm"
          :class="{ 'is-loading': benchBusy === '__all__' }"
          :disabled="!!benchBusy || !benchModels.length"
          @click="runBenchAll"
        >
          {{ benchBusy === '__all__' ? '测评中…' : `全部测评（${benchModels.length} 个）` }}
        </button>
        <span class="muted" style="font-size: 12.5px">逐个串行执行，耗时取决于各模型的响应速度</span>
      </div>
      <div
        v-for="m in benchModels"
        :key="m.id"
        class="row preset-row"
        style="align-items: center; gap: 10px"
      >
        <div style="flex: 1; min-width: 0">
          <div class="row" style="gap: 8px">
            <span class="mono" style="font-weight: 600">{{ m.name }}</span>
            <span class="badge">{{ tierLabel(m) }}</span>
            <span v-if="m.config?.intelligence" class="badge brand">{{ m.config.intelligence }} 分</span>
            <span v-else class="badge">未测评</span>
          </div>
          <div v-if="benchErr[m.name]" class="hint err" style="margin-top: 4px">{{ benchErr[m.name] }}</div>
        </div>
        <button
          class="btn btn-sm"
          :class="{ 'is-loading': benchBusy === m.name }"
          :disabled="!!benchBusy"
          @click="runBench(m.name)"
        >
          {{ benchBusy === m.name ? '测评中…' : '测评' }}
        </button>
      </div>
      <template #footer>
        <span class="muted" style="font-size: 12.5px">
          测评会真实调用：每个模型作答 3 次 + 主模型评分 3 次，注意免费额度消耗。
        </span>
        <button class="btn" @click="benchOpen = false">关闭</button>
      </template>
    </Modal>

    <Modal v-if="aggOpen" wide title="多模态自动聚合" subtitle="扫描模型库里的视觉上游，自动生成聚合配置计划。" @close="aggOpen = false">
      <div v-if="aggLoading" class="card empty">
        <div class="big">正在扫描模型库…</div>
      </div>
      <template v-else>
        <div v-if="!aggPlan?.declares.length && !aggPlan?.creates.length" class="card empty">
          <div class="big">没有可自动聚合的内容</div>
          <div>模型库里的视觉上游都已被引用，或还没有探测到带模态信息的视觉模型。先去「渠道」页探测、或到「模型库」确认模态标注。</div>
        </div>

        <template v-else>
          <div v-if="aggPlan?.declares.length" class="field">
            <div class="field-label">补声明模态（{{ aggPlan.declares.length }}）</div>
            <div class="hint" style="margin-bottom: 8px">这些模型的上游在模型库里标注了图片输入，但模型配置没声明、名字也推断不出来，补上后带图请求才能路由过去。</div>
            <div v-for="d in aggPlan.declares" :key="d.key" class="agg-item">
              <label class="row" style="gap: 8px; cursor: pointer">
                <input v-model="aggSelDeclares[d.key]" type="checkbox" />
                <span class="mono">{{ d.model_name }}</span>
                <span class="muted">上游</span>
                <span class="mono">{{ d.provider_name }} · {{ d.upstream_name }}</span>
                <span class="badge brand">声明 {{ modalityText(d.modalities) }}</span>
              </label>
            </div>
          </div>

          <div v-if="aggPlan?.creates.length" class="field">
            <div class="field-label">新建聚合模型（{{ aggPlan.creates.length }}）</div>
            <div class="hint" style="margin-bottom: 8px">视觉上游还没被任何模型使用。新建后：纯文本请求走同渠道纯文本上游（更省更快），带图请求自动路由到视觉主上游。</div>
            <div v-for="c in aggPlan.creates" :key="c.key" class="agg-item">
              <label class="row" style="gap: 8px; cursor: pointer; align-items: flex-start">
                <input v-model="aggSelCreates[c.key]" type="checkbox" style="margin-top: 3px" />
                <span>
                  <span class="mono" style="font-weight: 600">{{ c.name }}</span>
                  <span class="badge warn" style="margin-left: 6px">视觉主上游</span>
                  <div class="muted" style="font-size: 12px; margin-top: 2px">
                    {{ c.provider_name }} · <span class="mono">{{ c.upstream_name }}</span>（{{ modalityText(c.modalities) }}）
                  </div>
                  <div v-if="c.failovers.length" class="muted" style="font-size: 12px">
                    纯文本故障转移：<span class="mono">{{ c.failovers.join(', ') }}</span>
                  </div>
                </span>
              </label>
            </div>
          </div>
        </template>
      </template>
      <template #footer>
        <span class="muted" style="font-size: 12.5px">
          已选 {{ aggSelCount }} 项 · 只改这里列出的内容，不动其它配置
        </span>
        <button class="btn" @click="aggOpen = false">取消</button>
        <button
          class="btn btn-primary"
          :disabled="aggLoading || aggApplying || !aggSelCount"
          @click="applyAggregate"
        >
          {{ aggApplying ? '应用中…' : '应用勾选项' }}
        </button>
      </template>
    </Modal>

    <Modal
      v-if="profileOpen"
      wide
      title="上游画像"
      subtitle="每次真实调用的滚动累计。连续失败 ≥3 次的上游会被路由自动降权（排到尝试队列尾部），重启不丢。"
      @close="profileOpen = false"
    >
      <div v-if="profileLoading" class="card empty"><div class="big">加载中…</div></div>
      <div v-else-if="!profileList.length" class="card empty">
        <div class="big">还没有画像数据</div>
        <div>发起一次真实调用后这里会开始积累。跑得越久画像越准，路由越懂得避开常挂的上游。</div>
      </div>
      <div v-else class="table-wrap">
        <table class="table">
          <thead>
            <tr>
              <th>渠道 / 上游</th>
              <th>状态</th>
              <th class="num">成功 / 失败</th>
              <th class="num">成功率</th>
              <th class="num">平均延迟</th>
              <th>最近错误</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="s in profileList" :key="s.provider_id + '::' + s.upstream_name">
              <td>
                <div class="mono clamp-1">{{ s.upstream_name }}</div>
                <div class="muted" style="font-size: 11.5px">{{ s.provider_name || '渠道 #' + s.provider_id }}</div>
              </td>
              <td><span class="badge" :class="healthClass(s)"><span class="dot" />{{ healthLabel(s) }}</span></td>
              <td class="num nowrap">
                <span style="color: var(--ok)">{{ s.ok_count }}</span>
                <span class="muted"> / </span>
                <span :style="s.fail_count ? 'color: var(--danger)' : ''">{{ s.fail_count }}</span>
              </td>
              <td class="num nowrap">
                <span :class="rateClass(s)">{{ rateText(s) }}</span>
              </td>
              <td class="num muted nowrap">
                {{ s.avg_latency_ms ? Math.round(s.avg_latency_ms) + ' ms' : '—' }}
              </td>
              <td class="dim clamp-1" :title="s.last_error">{{ s.last_error || '—' }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <template #footer>
        <span class="muted" style="font-size: 12.5px">共 {{ profileList.length }} 个上游 · 数据随调用实时累计</span>
        <button class="btn btn-sm" :disabled="profileLoading" @click="openProfile">刷新</button>
        <button class="btn" @click="profileOpen = false">关闭</button>
      </template>
    </Modal>

    <Modal
      v-if="aiOpen"
      wide
      title="AI 建模"
      :subtitle="`自然语言创建模型 · 由系统级 AI「${aiResult?.system_model || '未配置'}」生成草案，确认后才落库`"
      @close="aiOpen = false"
    >
      <div class="field">
        <label>描述你想要的模型</label>
        <textarea
          v-model="aiPrompt"
          class="input"
          rows="2"
          placeholder="例如：帮我把所有 NIM 的模型整合为一个 NIMout 模型"
          @keydown.enter.exact.prevent="runAI()"
        />
        <div class="row" style="gap: 6px; margin-top: 8px; flex-wrap: wrap">
          <button
            v-for="ex in aiExamples"
            :key="ex"
            class="btn btn-sm btn-ghost"
            :disabled="aiBusy"
            @click="runAI(ex)"
          >
            {{ ex }}
          </button>
        </div>
        <div class="row" style="margin-top: 10px">
          <button class="btn btn-primary btn-sm" :disabled="aiBusy || !aiPrompt.trim()" @click="runAI()">
            {{ aiBusy ? 'AI 思考中…' : '生成草案' }}
          </button>
          <span v-if="aiResult" class="muted" style="font-size: 12px">
            {{ aiResult.latency_ms }} ms · 耗费 {{ aiResult.usage?.prompt_tokens ?? '?' }}+{{ aiResult.usage?.completion_tokens ?? '?' }} tokens
          </span>
        </div>
      </div>

      <div v-if="aiError" class="hint err">{{ aiError }}</div>

      <template v-if="aiResult">
        <div class="field">
          <div class="row" style="gap: 8px; align-items: flex-start">
            <span class="badge brand" style="flex: none">AI</span>
            <span>{{ aiResult.reply }}</span>
          </div>
        </div>

        <div v-if="aiResult.warnings.length" class="field">
          <div v-for="(w, i) in aiResult.warnings" :key="i" class="hint err" style="margin-bottom: 4px">⚠ {{ w }}</div>
        </div>

        <div v-if="aiResult.models.length" class="field">
          <div class="field-label">草案（{{ aiResult.models.length }} 个模型）</div>
          <div v-for="m in aiResult.models" :key="m.name" class="agg-item">
            <div class="row" style="gap: 8px; align-items: center">
              <span class="mono" style="font-weight: 600">{{ m.name }}</span>
              <span class="badge brand">{{ capName(m.capability) }}</span>
              <span v-if="m.remark" class="muted" style="font-size: 12px">{{ m.remark }}</span>
            </div>
            <div v-for="(inp, i) in m.inputs" :key="i" class="muted" style="font-size: 12px; margin-top: 4px">
              {{ i + 1 }}. {{ providers.find((p) => p.id === inp.provider_id)?.name || '渠道 #' + inp.provider_id }}
              · <span class="mono">{{ inp.upstream_name }}</span>
              （{{ modalityShort(inp.input_modalities) }}）
            </div>
          </div>
        </div>
        <div v-else class="card empty">
          <div class="big">没有生成可用草案</div>
          <div>看看上面的 AI 说明与警告，换个更具体的描述再试。</div>
        </div>
      </template>

      <template #footer>
        <span class="muted" style="font-size: 12.5px">草案只是预览——点创建才会写入，可反复重新生成</span>
        <button class="btn" @click="aiOpen = false">关闭</button>
        <button
          v-if="aiResult?.models.length"
          class="btn btn-primary"
          :disabled="aiCreating || aiBusy"
          @click="createAIModels"
        >
          {{ aiCreating ? '创建中…' : `创建全部（${aiResult.models.length}）` }}
        </button>
      </template>
    </Modal>
  </div>
</template>
