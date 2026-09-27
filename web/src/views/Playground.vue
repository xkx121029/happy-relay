<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { api, streamChat, type Skill, type VideoTask } from '../api'
import { models, providers, skills, state, toast } from '../store'
import { capLabel, fmtTime, renderMarkdown } from '../utils'

interface ChatMsg {
  role: string
  content: string
  reasoning?: string
  /** 用户消息附带的图片（data URI），仅用于本地展示，回放时不重发。 */
  images?: string[]
  /** 本次回复实际命中的上游渠道 */
  provider?: string
  /** auto 模式下实际选中的模型 */
  routedModel?: string
  /** 聚合模型实际命中的上游模型名（后端 meta.upstream） */
  upstream?: string
  /** auto 模式下的难度评估 */
  difficulty?: { score: number; tier: string; reason: string }
  /** 本轮回复总耗时（毫秒），前端计时 */
  elapsedMS?: number
}

const loading = ref(true)
const capability = ref('chat')
const selModel = ref('')
const selSkill = ref('')
const skillParams = reactive<Record<string, string>>({})
const input = ref('')

const msgs = ref<ChatMsg[]>([])
const streaming = ref(false)
let controller: AbortController | null = null
const streamEl = ref<HTMLElement | null>(null)

// 待发送的图片附件（data URI）。发送后清空；只挂在当前这条用户消息上。
const attachments = ref<{ id: number; data: string; name: string; size: number }[]>([])
let attachSeq = 0
const MAX_ATTACH = 4
const MAX_ATTACH_BYTES = 4 * 1024 * 1024

const generating = ref(false)
const images = ref<string[]>([])
const imageProvider = ref('')
const videoTask = ref<VideoTask | null>(null)
const videoBusy = ref(false)
let videoTimer: number | null = null

async function load() {
  loading.value = true
  try {
    if (!state.meta) state.meta = await api.getMeta()
    const [ms, sks] = await Promise.all([api.listModels(), api.listSkills()])
    models.value = ms
    skills.value = sks
  } catch (e: any) {
    toast(e.message || '加载失败', 'error')
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  restoreChat()
  restoreParams()
  load()
})

const capTabs = computed(
  () =>
    state.meta?.capabilities || [
      { value: 'chat', label: '文本对话' },
      { value: 'image', label: '图片生成' },
      { value: 'video', label: '视频生成' },
    ],
)

const capModels = computed(() =>
  models.value.filter((m) => m.enabled && m.capability === capability.value),
)

const capSkills = computed(() =>
  skills.value.filter((s) => s.enabled && s.capability === capability.value),
)

const selModelInfo = computed(() => capModels.value.find((m) => m.name === selModel.value))

// ---------- 指定上游（仅调试台） ----------

// pinnedUpstream 为空 = 按模型配置自动调度；否则钉住某一条上游。
const pinnedUpstream = ref('')

// upstreamOptions 列出当前模型绑定的上游，key 形如 "渠道ID::上游模型名"。
const upstreamOptions = computed(() => {
  const list = selModelInfo.value?.inputs || []
  return list.map((inp) => {
    const p = providers.value.find((x) => x.id === inp.provider_id)
    return {
      key: `${inp.provider_id}::${inp.upstream_name}`,
      label: `${p?.name || '渠道 #' + inp.provider_id} · ${inp.upstream_name}`,
      provider_id: inp.provider_id,
      upstream_name: inp.upstream_name,
    }
  })
})

// pinBody 把指定的上游写进请求体，后端仅在调试台入口读取这两个字段。
function pinBody(body: Record<string, any>) {
  if (!pinnedUpstream.value) return body
  const [pid, name] = pinnedUpstream.value.split('::')
  body.provider_id = Number(pid)
  if (name) body.upstream_name = name
  return body
}

// ---------- 对话采样参数（system prompt / temperature / top_p / max_tokens） ----------
// 后端会把请求体里的未知字段原样透传给上游，这里只需在前端拼进 body 即可。
// 参数随 localStorage 持久化，刷新后不丢。

const systemPrompt = ref('')
const temperature = ref('')
const topP = ref('')
const maxTokens = ref('')

const PARAMS_STORAGE_KEY = 'playground.params.v1'

function saveParams() {
  try {
    localStorage.setItem(
      PARAMS_STORAGE_KEY,
      JSON.stringify({ s: systemPrompt.value, t: temperature.value, p: topP.value, m: maxTokens.value }),
    )
  } catch {}
}

function restoreParams() {
  try {
    const raw = localStorage.getItem(PARAMS_STORAGE_KEY)
    if (!raw) return
    const o = JSON.parse(raw)
    systemPrompt.value = o.s || ''
    temperature.value = o.t ?? ''
    topP.value = o.p ?? ''
    maxTokens.value = o.m ?? ''
  } catch {}
}

watch([systemPrompt, temperature, topP, maxTokens], saveParams)

// applyChatParams 把采样参数合并进请求体；system prompt 插到消息列表最前。
// 留空的参数不下发，交给上游用默认值。
function applyChatParams(body: any) {
  const sys = systemPrompt.value.trim()
  if (sys && Array.isArray(body.messages)) {
    body.messages = [{ role: 'system', content: sys }, ...body.messages]
  }
  if (temperature.value !== '') body.temperature = Number(temperature.value)
  if (topP.value !== '') body.top_p = Number(topP.value)
  if (maxTokens.value !== '') body.max_tokens = Number(maxTokens.value)
}

// ---------- 导出对话 Markdown ----------

function exportChat() {
  if (!msgs.value.length) return
  const lines: string[] = [
    '# 调试台对话记录',
    '',
    `- 模型：${selModel.value || '—'}`,
    `- 导出时间：${new Date().toLocaleString('zh-CN', { hour12: false })}`,
  ]
  if (systemPrompt.value.trim()) lines.push(`- System Prompt：${systemPrompt.value.trim()}`)
  lines.push('', '---', '')
  for (const m of msgs.value) {
    lines.push(m.role === 'user' ? '## 用户' : '## 助手')
    const meta: string[] = []
    if (m.routedModel) meta.push(`实际模型 ${m.routedModel}`)
    if (m.provider) meta.push(`渠道 ${m.provider}`)
    if (m.elapsedMS) meta.push(`耗时 ${m.elapsedMS} ms`)
    if (meta.length) lines.push(`> ${meta.join(' · ')}`, '')
    lines.push(m.content || '', '')
  }
  const blob = new Blob([lines.join('\n')], { type: 'text/markdown;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `playground-${new Date().toISOString().slice(0, 10)}.md`
  a.click()
  URL.revokeObjectURL(url)
  toast('对话已导出为 Markdown')
}

// ---------- 图片结果下载 ----------

function imgDownloadName(index: number, src: string): string {
  let ext = '.png'
  if (src.startsWith('data:image/webp')) ext = '.webp'
  else if (src.startsWith('data:image/jpeg')) ext = '.jpg'
  else if (src.startsWith('data:image/gif')) ext = '.gif'
  return `image-${index + 1}${ext}`
}

// ---------- 语音合成 ----------

const speechText = ref('')
const speechVoice = ref('')
const speechFormat = ref('mp3')
const speechBusy = ref(false)
const speechUrl = ref('')

const speechFormats = ['mp3', 'wav', 'opus', 'aac', 'flac', 'pcm']

function revokeSpeechUrl() {
  if (speechUrl.value) {
    URL.revokeObjectURL(speechUrl.value)
    speechUrl.value = ''
  }
}

async function genSpeech() {
  if (!selModel.value) {
    toast('请先选择语音合成模型', 'error')
    return
  }
  const text = input.value.trim()
  if (!selSkill.value && !text) {
    toast('请输入要合成的文本', 'error')
    return
  }
  const body: any = { model: selModel.value, ...skillBody() }
  if (text) body.input = text
  if (speechVoice.value.trim()) body.voice = speechVoice.value.trim()
  if (speechFormat.value) body.response_format = speechFormat.value
  pinBody(body)
  speechBusy.value = true
  revokeSpeechUrl()
  try {
    const blob = await api.playgroundSpeech(body)
    speechUrl.value = URL.createObjectURL(blob)
    toast('合成完成', 'success')
  } catch (e: any) {
    toast(e.message || '合成失败', 'error')
  } finally {
    speechBusy.value = false
  }
}

// ---------- 语音识别 ----------

const transcribeFile = ref<File | null>(null)
const transcribeLanguage = ref('')
const transcribeBusy = ref(false)
const transcribeText = ref('')
const transcribeRaw = ref('')

function pickTranscribeFile(e: Event) {
  const el = e.target as HTMLInputElement
  const f = el.files?.[0]
  if (!f) return
  if (f.size > 64 * 1024 * 1024) {
    toast('音频文件不能超过 64MB', 'error')
    el.value = ''
    return
  }
  transcribeFile.value = f
}

async function runTranscribe() {
  if (!selModel.value) {
    toast('请先选择语音识别模型', 'error')
    return
  }
  if (!transcribeFile.value) {
    toast('请先选择要识别的音频文件', 'error')
    return
  }
  const form = new FormData()
  form.append('file', transcribeFile.value)
  form.append('model', selModel.value)
  if (transcribeLanguage.value.trim()) form.append('language', transcribeLanguage.value.trim())
  // 指定上游时一并带上，后端只在调试台入口读取这两个字段。
  if (pinnedUpstream.value) {
    const [pid, name] = pinnedUpstream.value.split('::')
    form.append('provider_id', pid)
    if (name) form.append('upstream_name', name)
  }
  transcribeBusy.value = true
  transcribeText.value = ''
  transcribeRaw.value = ''
  try {
    const res = await api.playgroundTranscribe(form)
    transcribeText.value = res?.text || ''
    transcribeRaw.value = JSON.stringify(res, null, 2)
    if (!transcribeText.value) toast('上游未返回 text 字段，请看原始响应', 'error')
  } catch (e: any) {
    toast(e.message || '识别失败', 'error')
  } finally {
    transcribeBusy.value = false
  }
}

onUnmounted(revokeSpeechUrl)

// auto 是服务端内置的自动模型：按任务难度挑选合适的文本模型。
const modelOptions = computed(() => {
  const opts = capModels.value.map((m) => ({ name: m.name, label: m.name }))
  if (capability.value === 'chat' && capModels.value.length) {
    opts.unshift({ name: 'auto', label: 'auto · 自动挑选' })
  }
  return opts
})

function tierLabel(tier: string) {
  return { light: '轻量', standard: '标准', pro: '强力' }[tier] || tier
}

const currentSkill = computed<Skill | undefined>(() => skills.value.find((s) => s.slug === selSkill.value))

watch(capability, () => {
  selModel.value = capModels.value[0]?.name || ''
  selSkill.value = ''
  pinnedUpstream.value = ''
})

watch(capModels, (list) => {
  if (!list.some((m) => m.name === selModel.value)) {
    selModel.value = list[0]?.name || ''
  }
})

// 换模型后原有的上游选择必然失效，重置回自动。
watch(selModel, () => {
  pinnedUpstream.value = ''
})

watch(selSkill, () => {
  Object.keys(skillParams).forEach((k) => delete skillParams[k])
  const sk = currentSkill.value
  if (!sk) return
  for (const p of sk.params || []) {
    skillParams[p.key] = p.default || ''
  }
})

function onSkillChip(slug: string) {
  selSkill.value = selSkill.value === slug ? '' : slug
}

function skillBody(): Record<string, any> {
  if (!selSkill.value) return {}
  return { skill: selSkill.value, skill_params: { ...skillParams } }
}

async function scrollBottom() {
  await nextTick()
  if (streamEl.value) streamEl.value.scrollTop = streamEl.value.scrollHeight
}

// ---------- 对话会话持久化 ----------
// 刷新页面后自动恢复上次对话，避免调试到一半白屏重来。
// 图片附件只保留小于 100KB 的 data URI（localStorage 容量有限），大图丢弃。

const CHAT_STORAGE_KEY = 'playground.chat.v1'

function saveChat() {
  try {
    const slim = msgs.value.slice(-80).map((m) => ({
      role: m.role,
      content: m.content,
      reasoning: m.reasoning,
      provider: m.provider,
      upstream: m.upstream,
      routedModel: m.routedModel,
      difficulty: m.difficulty,
      elapsedMS: m.elapsedMS,
      images: m.images?.filter((s) => s.length < 100_000),
    }))
    localStorage.setItem(CHAT_STORAGE_KEY, JSON.stringify(slim))
  } catch {
    // 存不下（超容量/隐私模式）就算了，持久化是尽力而为。
  }
}

function restoreChat() {
  try {
    const raw = localStorage.getItem(CHAT_STORAGE_KEY)
    if (!raw) return
    const arr = JSON.parse(raw)
    if (Array.isArray(arr) && arr.length) msgs.value = arr
  } catch {
    // 坏数据直接忽略。
  }
}

async function copyMsg(m: ChatMsg) {
  try {
    await navigator.clipboard.writeText(m.content)
    toast('已复制到剪贴板')
  } catch {
    toast('复制失败：浏览器未授权剪贴板', 'error')
  }
}

// ---------- 对话 ----------

// 读取图片附件：转成 data URI 直接内嵌发送，后端会解码成二进制转发给上游。
// 限制 4MB / 4 张，避免单次请求体过大把上游或代理打挂。
async function readAttachment(file: File): Promise<string | null> {
  if (!file.type.startsWith('image/')) {
    toast(`「${file.name}」不是图片，已跳过`, 'error')
    return null
  }
  if (file.size > MAX_ATTACH_BYTES) {
    toast(`「${file.name}」超过 4MB，建议先压缩或改用 URL`, 'error')
    return null
  }
  return new Promise((resolve) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result || ''))
    reader.onerror = () => resolve(null)
    reader.readAsDataURL(file)
  })
}

async function addAttachments(files: FileList | File[]) {
  const list = Array.from(files)
  for (const f of list) {
    if (attachments.value.length >= MAX_ATTACH) {
      toast(`最多附带 ${MAX_ATTACH} 张图片`, 'error')
      break
    }
    const data = await readAttachment(f)
    if (data) {
      attachments.value.push({ id: ++attachSeq, data, name: f.name, size: f.size })
    }
  }
}

function onPickFiles(e: Event) {
  const el = e.target as HTMLInputElement
  if (el.files?.length) addAttachments(el.files)
  // 清空 input，否则连续选同一张图不触发 change。
  el.value = ''
}

// 直接粘贴截图（Ctrl+V）是最顺手的路径，很多人截图后就想直接发。
function onPaste(e: ClipboardEvent) {
  const files = Array.from(e.clipboardData?.files || []).filter((f) => f.type.startsWith('image/'))
  if (!files.length) return
  e.preventDefault()
  addAttachments(files)
}

function removeAttachment(id: number) {
  attachments.value = attachments.value.filter((a) => a.id !== id)
}

// 把当前输入框内容按「文本 + 图片」组装成 OpenAI 多模态 content。
// 只有图片没有文字时补一个占位文本，因为部分上游要求 content 非空。
function buildUserContent(text: string, imgs: string[]): any {
  if (!imgs.length) return text
  const parts: any[] = []
  if (text) parts.push({ type: 'text', text })
  else parts.push({ type: 'text', text: '请描述这张图片。' })
  for (const url of imgs) parts.push({ type: 'image_url', image_url: { url } })
  return parts
}

function stopChat() {
  controller?.abort()
  streaming.value = false
  controller = null
}

async function sendChat() {
  if (!selModel.value) {
    toast('请先选择模型', 'error')
    return
  }
  const text = input.value.trim()
  const imgs = attachments.value.map((a) => a.data)
  if (selSkill.value) {
    // 技能模式：由后端渲染模板，这里只展示套用的技能与参数。
    const desc = Object.entries(skillParams)
      .filter(([, v]) => v)
      .map(([k, v]) => `${k}：${v}`)
      .join('；')
    msgs.value.push({ role: 'user', content: `【技能】${currentSkill.value?.name}${desc ? '（' + desc + '）' : ''}` })
  } else {
    if (!text && !imgs.length) {
      toast('请输入内容或添加图片', 'error')
      return
    }
    msgs.value.push({ role: 'user', content: text, images: imgs })
  }
  // 附件已挂到上面那条消息里，立刻清空输入区，避免误以为没发出去。
  attachments.value = []
  input.value = ''
  resetInputHeight()
  saveChat()
  await streamTurn()
}

// streamTurn 基于当前 msgs 发起一轮对话：追加 assistant 占位并流式填充。
// sendChat 与「重新生成」共用，后者在调用前已把尾部 assistant 消息摘掉。
async function streamTurn() {
  const body: any = { model: selModel.value, stream: true, ...skillBody() }
  if (!selSkill.value) {
    // 历史消息里的老图片不再重发，只发当前这条的图，控制请求体大小。
    const last = msgs.value.length - 1
    const imgs = msgs.value[last]?.role === 'user' ? msgs.value[last].images || [] : []
    body.messages = msgs.value.map((m, i) => ({
      role: m.role,
      content: i === last ? buildUserContent(m.content, imgs) : m.content,
    }))
  }
  pinBody(body)
  applyChatParams(body)
  const idx = msgs.value.push({ role: 'assistant', content: '', reasoning: '' }) - 1
  streaming.value = true
  controller = new AbortController()
  const startedAt = performance.now()
  await scrollBottom()

  await streamChat(
    body,
    {
      onDelta: (content, reasoning) => {
        const m = msgs.value[idx]
        if (!m) return
        m.content += content
        if (reasoning) m.reasoning = (m.reasoning || '') + reasoning
        scrollBottom()
      },
      onError: (message) => {
        const m = msgs.value[idx]
        if (m) m.content += (m.content ? '\n\n' : '') + '⚠ ' + message
      },
      onMeta: (meta) => {
        const m = msgs.value[idx]
        if (!m) return
        if (meta.provider) m.provider = meta.provider
        if (meta.upstream) m.upstream = meta.upstream
        if (meta.model && meta.model !== selModel.value) m.routedModel = meta.model
        if (meta.difficulty) m.difficulty = meta.difficulty
      },
      onDone: () => {
        streaming.value = false
        controller = null
        const m = msgs.value[idx]
        if (m) m.elapsedMS = Math.round(performance.now() - startedAt)
        saveChat()
      },
    },
    controller.signal,
  )
  streaming.value = false
  saveChat()
}

// regenerate 摘掉尾部 assistant 回复后原样重发最后一轮，
// 换模型/换上游后点它即可立刻对比效果。
async function regenerate() {
  if (streaming.value || !selModel.value) return
  while (msgs.value.length && msgs.value[msgs.value.length - 1].role === 'assistant') {
    msgs.value.pop()
  }
  const last = msgs.value[msgs.value.length - 1]
  if (!last || last.role !== 'user') return
  await streamTurn()
}

function clearChat() {
  stopChat()
  msgs.value = []
  try {
    localStorage.removeItem(CHAT_STORAGE_KEY)
  } catch {}
}

// ---------- 输入框自适应高度 ----------

const chatInputEl = ref<HTMLTextAreaElement | null>(null)

function autoGrow(e: Event) {
  const el = e.target as HTMLTextAreaElement
  el.style.height = 'auto'
  el.style.height = Math.min(el.scrollHeight, 220) + 'px'
}

function resetInputHeight() {
  if (chatInputEl.value) chatInputEl.value.style.height = ''
}

// ---------- 图片 ----------

// 图片参数：尺寸档位 + 宽高比（Agnes 等新模型用 1K/2K + ratio 表达），
// 也可以切到「自定义」直接填 1024x1024 这类精确尺寸。
const imageSizeMode = ref('2K')
const imageSizeCustom = ref('1024x1024')
const imageRatio = ref('1:1')
const imageFormat = ref('') // 空表示不传，交给上游默认
const imageRefs = ref<string[]>([])

const sizePresets = ['1K', '2K', '3K', '4K']
const ratios = ['1:1', '3:4', '4:3', '16:9', '9:16', '2:3', '3:2', '21:9']

function activeSize(): string {
  return imageSizeMode.value === 'custom' ? imageSizeCustom.value.trim() : imageSizeMode.value
}

function addImageRef() {
  if (imageRefs.value.length >= 8) {
    toast('最多 8 张参考图', 'error')
    return
  }
  imageRefs.value.push('')
}

function updateRef(index: number, e: Event) {
  imageRefs.value[index] = (e.target as HTMLInputElement).value
}

// pickImageRef 把本地图片读成 data URI，便于直接传给支持 data URI 的上游。
async function pickImageRef(index: number, e: Event) {
  const el = e.target as HTMLInputElement
  const file = el.files?.[0]
  if (!file) return
  if (file.size > 4 * 1024 * 1024) {
    toast('参考图不能超过 4MB，建议改用图片 URL', 'error')
    el.value = ''
    return
  }
  const reader = new FileReader()
  reader.onload = () => {
    imageRefs.value[index] = String(reader.result || '')
  }
  reader.readAsDataURL(file)
  el.value = ''
}

async function genImage() {
  if (!selModel.value) {
    toast('请先选择图片模型', 'error')
    return
  }
  const prompt = input.value.trim()
  if (!selSkill.value && !prompt) {
    toast('请输入图片提示词', 'error')
    return
  }
  const body: any = { model: selModel.value, ...skillBody() }
  const size = activeSize()
  if (size) body.size = size
  // 档位式尺寸才带宽高比；自定义精确尺寸时由尺寸本身决定比例。
  if (imageSizeMode.value !== 'custom' && imageRatio.value) body.ratio = imageRatio.value
  if (imageFormat.value) body.response_format = imageFormat.value
  const refs = imageRefs.value.map((s) => s.trim()).filter(Boolean)
  if (refs.length) body.image = refs
  if (prompt) body.prompt = prompt
  pinBody(body)
  generating.value = true
  images.value = []
  try {
    const res = await api.playgroundImage(body)
    const items = res?.data || []
    images.value = items
      .map((it: any) => it.url || (it.b64_json ? 'data:image/png;base64,' + it.b64_json : ''))
      .filter(Boolean)
    imageProvider.value = res?.provider || ''
    if (!images.value.length) toast('未返回图片', 'error')
  } catch (e: any) {
    toast(e.message || '生成失败', 'error')
  } finally {
    generating.value = false
  }
}

// ---------- 视频 ----------

// 视频参数：Agnes 等上游用 mode 区分文生视频 / 首尾帧 / 参考素材三种生成方式。
const videoMode = ref('text')
const videoModes = [
  { value: 'text', label: '文生视频' },
  { value: 'keyframe', label: '首尾帧' },
  { value: 'reference', label: '参考素材' },
]
const videoDuration = ref('5')
const videoDurations = ['4', '5', '6', '8', '10', '12']
const videoRatio = ref('16:9')
const videoRatios = ['16:9', '9:16', '1:1', '4:3', '3:4', '21:9']
const videoFirstFrame = ref('')
const videoLastFrame = ref('')
const videoRefImages = ref<string[]>([])
const videoRefAudios = ref<string[]>([])

function videoRefList(kind: 'images' | 'audios') {
  return kind === 'images' ? videoRefImages : videoRefAudios
}

function addVideoRef(kind: 'images' | 'audios') {
  const list = videoRefList(kind)
  const limit = kind === 'images' ? 8 : 4
  if (list.value.length >= limit) {
    toast(`最多 ${limit} 个${kind === 'images' ? '参考图' : '参考音频'}`, 'error')
    return
  }
  list.value.push('')
}

function updateVideoRef(kind: 'images' | 'audios', index: number, e: Event) {
  videoRefList(kind).value[index] = (e.target as HTMLInputElement).value
}

function removeVideoRef(kind: 'images' | 'audios', index: number) {
  videoRefList(kind).value.splice(index, 1)
}

// pickVideoImage 把本地图片读成 data URI；音频体积大，建议直接填 URL。
async function pickVideoImage(index: number, e: Event) {
  const el = e.target as HTMLInputElement
  const file = el.files?.[0]
  if (!file) return
  if (file.size > 4 * 1024 * 1024) {
    toast('参考图不能超过 4MB，建议改用图片 URL', 'error')
    el.value = ''
    return
  }
  const reader = new FileReader()
  reader.onload = () => {
    videoRefImages.value[index] = String(reader.result || '')
  }
  reader.readAsDataURL(file)
  el.value = ''
}

function stopPolling() {
  if (videoTimer !== null) {
    clearInterval(videoTimer)
    videoTimer = null
  }
}

async function refreshTask(id: string) {
  try {
    const list = await api.listTasks(50)
    const found = list.find((t) => t.id === id)
    if (found) {
      videoTask.value = found
      if (found.status === 'succeeded' || found.status === 'failed') {
        stopPolling()
        videoBusy.value = false
        toast(found.status === 'succeeded' ? '视频生成完成' : '视频生成失败', found.status === 'succeeded' ? 'success' : 'error')
      }
    }
  } catch {
    /* 轮询失败忽略，下个周期重试 */
  }
}

async function genVideo() {
  if (!selModel.value) {
    toast('请先选择视频模型', 'error')
    return
  }
  const prompt = input.value.trim()
  if (!selSkill.value && !prompt) {
    toast('请输入视频提示词', 'error')
    return
  }
  const body: any = {
    model: selModel.value,
    mode: videoMode.value,
    duration: Number(videoDuration.value) || 0,
    ...skillBody(),
  }
  if (prompt) body.prompt = prompt
  if (videoRatio.value) body.aspect_ratio = videoRatio.value
  if (videoMode.value === 'keyframe') {
    if (videoFirstFrame.value.trim()) body.first_frame = videoFirstFrame.value.trim()
    if (videoLastFrame.value.trim()) body.last_frame = videoLastFrame.value.trim()
  }
  if (videoMode.value === 'reference') {
    const refs = videoRefImages.value.map((s) => s.trim()).filter(Boolean)
    if (refs.length) body.images = refs
    const audios = videoRefAudios.value.map((s) => s.trim()).filter(Boolean)
    if (audios.length) body.audios = audios
  }
  pinBody(body)
  videoBusy.value = true
  videoTask.value = null
  stopPolling()
  try {
    const task = await api.playgroundVideo(body)
    videoTask.value = task
    if (task.status === 'succeeded' || task.status === 'failed') {
      videoBusy.value = false
    } else {
      videoTimer = window.setInterval(() => refreshTask(task.id), 3000)
    }
  } catch (e: any) {
    toast(e.message || '提交失败', 'error')
    videoBusy.value = false
  }
}

function taskLabel(status: string) {
  return { queued: '排队中', running: '生成中', succeeded: '已完成', failed: '失败' }[status] || status
}

function taskBadge(status: string) {
  if (status === 'succeeded') return 'ok'
  if (status === 'failed') return 'danger'
  return 'warn'
}

async function run() {
  if (capability.value === 'chat') await sendChat()
  else if (capability.value === 'image') await genImage()
  else await genVideo()
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h2>调试台</h2>
        <p>不用建秘钥就能直接在网页里试模型，支持流式对话、出图与出视频。</p>
      </div>
      <div class="row">
        <button v-if="capability === 'chat' && msgs.length" class="btn btn-sm" @click="exportChat">
          导出 Markdown
        </button>
        <button v-if="capability === 'chat' && msgs.length" class="btn btn-sm" @click="clearChat">清空对话</button>
        <button class="btn btn-sm" :disabled="loading" @click="load">刷新</button>
      </div>
    </div>

    <div class="tabs">
      <div
        v-for="c in capTabs"
        :key="c.value"
        class="tab"
        :class="{ active: capability === c.value }"
        @click="capability = c.value"
      >
        {{ c.label }}
      </div>
    </div>

    <div class="grid" style="grid-template-columns: 300px 1fr; gap: 16px; align-items: start">
      <!-- 左侧设置 -->
      <div class="card">
        <div class="field">
          <label>模型</label>
          <select v-model="selModel" class="select">
            <option v-if="!modelOptions.length" value="">（无可用{{ capLabel(capability) }}模型）</option>
            <option v-for="m in modelOptions" :key="m.name" :value="m.name">{{ m.label }}</option>
          </select>
          <div v-if="selModel === 'auto'" class="hint">按任务难度自动挑选已启用的文本模型。</div>
          <div v-else-if="modelOptions.length" class="hint">
            {{ selModelInfo?.provider_name || '—' }}
            <span v-if="(selModelInfo?.inputs?.length || 0) > 1">
              · 共 {{ selModelInfo?.inputs?.length }} 个上游，失败时自动切换
            </span>
          </div>
        </div>

        <div v-if="upstreamOptions.length" class="field">
          <label>指定上游（可选）</label>
          <select v-model="pinnedUpstream" class="select">
            <option value="">自动（按模型配置顺序，失败自动切换）</option>
            <option v-for="u in upstreamOptions" :key="u.key" :value="u.key">{{ u.label }}</option>
          </select>
          <div class="hint">
            {{
              pinnedUpstream
                ? '只走这一个上游，失败不再切换，拿到的就是它的真实报错。'
                : '指定后可单独验证某个渠道 / 上游模型，便于定位问题。'
            }}
          </div>
        </div>

        <!-- 对话采样参数：后端原样透传，留空不下发 -->
        <details v-if="capability === 'chat'" class="pg-params">
          <summary>对话参数</summary>
          <div class="field" style="margin-top: 10px">
            <label>System Prompt</label>
            <textarea
              v-model="systemPrompt"
              class="textarea"
              rows="3"
              placeholder="设定助手的角色与行为，留空则不发送"
            />
          </div>
          <div class="grid cols-2" style="gap: 0 10px">
            <div class="field">
              <label>Temperature</label>
              <input
                v-model="temperature"
                class="input"
                type="number"
                min="0"
                max="2"
                step="0.1"
                placeholder="默认"
              />
            </div>
            <div class="field">
              <label>Top P</label>
              <input v-model="topP" class="input" type="number" min="0" max="1" step="0.05" placeholder="默认" />
            </div>
          </div>
          <div class="field" style="margin-bottom: 4px">
            <label>Max Tokens</label>
            <input v-model="maxTokens" class="input" type="number" min="1" placeholder="默认（不限）" />
          </div>
          <div class="hint">参数会随本次对话持久保存；temperature 控制发散度，max_tokens 限制回复长度。</div>
        </details>

        <div class="field" style="margin-bottom: 8px">
          <label>技能</label>
          <div v-if="capSkills.length" class="chips">
            <span
              v-for="s in capSkills"
              :key="s.id"
              class="chip"
              :class="{ active: selSkill === s.slug }"
              :title="s.description"
              @click="onSkillChip(s.slug)"
            >
              {{ s.name }}
            </span>
          </div>
          <div v-else class="hint">该能力下暂无可用技能。</div>
        </div>

        <template v-if="currentSkill">
          <div
            v-for="p in currentSkill.params || []"
            :key="p.key"
            class="field"
            style="margin-top: 12px; margin-bottom: 10px"
          >
            <label>{{ p.label || p.key }}</label>
            <textarea
              v-if="p.type === 'textarea'"
              v-model="skillParams[p.key]"
              class="textarea"
              rows="3"
              :placeholder="p.placeholder"
            />
            <select v-else-if="p.type === 'select'" v-model="skillParams[p.key]" class="select">
              <option value="">（未选择）</option>
              <option v-for="o in p.options || []" :key="o" :value="o">{{ o }}</option>
            </select>
            <input
              v-else-if="p.type === 'number'"
              v-model="skillParams[p.key]"
              type="number"
              class="input"
              :placeholder="p.placeholder"
            />
            <input v-else v-model="skillParams[p.key]" class="input" :placeholder="p.placeholder" />
          </div>
          <div class="hint">已选中技能「{{ currentSkill.name }}」，发送时会套用该模板。</div>
        </template>

      </div>

      <!-- 右侧主区域 -->
      <div>
        <!-- 语音合成 -->
        <template v-if="capability === 'speech'">
          <div class="card" style="margin-bottom: 16px">
            <div class="field" style="margin-bottom: 10px">
              <label>要合成的文本</label>
              <textarea
                v-model="input"
                class="textarea"
                rows="3"
                placeholder="例如：你好，欢迎使用模型中转站。"
              />
            </div>

            <div class="grid cols-2" style="gap: 0 14px">
              <div class="field">
                <label>音色（可选，留空用上游默认）</label>
                <input v-model="speechVoice" class="input code" placeholder="alloy / zh-CN-XiaoxiaoNeural" />
              </div>
              <div class="field">
                <label>音频格式</label>
                <select v-model="speechFormat" class="select">
                  <option v-for="f in speechFormats" :key="f" :value="f">{{ f }}</option>
                </select>
              </div>
            </div>
            <div class="hint" style="margin-bottom: 12px">
              各家音色名不同；固定用一个音色时，可以写在模型的「默认参数」里，例如
              <code>{"voice": "alloy"}</code>。
            </div>

            <div class="row">
              <div class="spacer" />
              <button class="btn btn-primary btn-sm" :disabled="speechBusy" @click="genSpeech">
                {{ speechBusy ? '合成中…' : '合成语音' }}
              </button>
            </div>
          </div>

          <div v-if="speechUrl" class="card">
            <label>试听</label>
            <audio :src="speechUrl" controls style="width: 100%; margin-top: 10px" />
            <div class="row" style="margin-top: 12px">
              <div class="spacer" />
              <a class="btn btn-sm" :href="speechUrl" :download="'speech.' + speechFormat">下载音频</a>
            </div>
          </div>
        </template>

        <!-- 语音识别 -->
        <template v-else-if="capability === 'transcription'">
          <div class="card" style="margin-bottom: 16px">
            <div class="field">
              <label>音频文件</label>
              <input type="file" accept="audio/*" class="input" @change="pickTranscribeFile" />
              <div class="hint">
                {{
                  transcribeFile
                    ? `已选择：${transcribeFile.name}（${(transcribeFile.size / 1024).toFixed(0)} KB）`
                    : '支持 mp3 / wav / m4a 等常见格式，单文件不超过 64MB。'
                }}
              </div>
            </div>
            <div class="field" style="max-width: 280px">
              <label>语言（可选）</label>
              <input v-model="transcribeLanguage" class="input code" placeholder="zh / en，留空自动识别" />
            </div>
            <div class="row">
              <div class="spacer" />
              <button class="btn btn-primary btn-sm" :disabled="transcribeBusy" @click="runTranscribe">
                {{ transcribeBusy ? '识别中…' : '开始识别' }}
              </button>
            </div>
          </div>

          <div v-if="transcribeText || transcribeRaw" class="card">
            <label>识别结果</label>
            <div
              style="
                margin-top: 10px;
                padding: 13px 14px;
                background: var(--bg-elev-2);
                border-radius: 8px;
                white-space: pre-wrap;
                line-height: 1.75;
              "
            >
              {{ transcribeText || '（上游未返回 text 字段）' }}
            </div>
            <details style="margin-top: 12px">
              <summary class="muted" style="cursor: pointer; font-size: 12.5px">上游原始响应</summary>
              <pre
                class="mono"
                style="
                  margin-top: 10px;
                  padding: 12px;
                  background: var(--bg-elev-2);
                  border-radius: 8px;
                  overflow: auto;
                  max-height: 320px;
                  font-size: 12px;
                  line-height: 1.6;
                "
              >{{ transcribeRaw }}</pre>
            </details>
          </div>
        </template>

        <!-- 对话 -->
        <template v-else-if="capability === 'chat'">
          <div class="chat-wrap">
            <div ref="streamEl" class="chat-stream">
              <div v-if="!msgs.length" class="empty" style="margin: auto">
                <div class="empty-icon">◇</div>
                <div class="big">开始一次对话</div>
                <div>选择模型后输入内容，回复会逐字流式输出。</div>
                <div class="empty-tips">
                  <span>Enter 发送 · Shift+Enter 换行</span>
                  <span v-if="capSkills.length">也可以先点左侧技能套用模板</span>
                </div>
                <div v-if="!modelOptions.length" class="empty-cta">
                  <RouterLink class="btn btn-sm" to="/models">去「模型」页启用一个对话模型</RouterLink>
                </div>
              </div>
              <div v-for="(m, i) in msgs" :key="i" class="msg" :class="m.role">
                <div class="avatar">{{ m.role === 'user' ? '我' : 'AI' }}</div>
                <div class="bubble">
                  <div v-if="m.reasoning" class="reasoning">{{ m.reasoning }}</div>
                  <div v-if="m.images?.length" class="msg-images">
                    <img v-for="(src, k) in m.images" :key="k" :src="src" alt="附图" />
                  </div>
                  <div v-if="m.role === 'assistant'" v-html="renderMarkdown(m.content)" />
                  <div v-else style="white-space: pre-wrap">{{ m.content }}</div>
                  <span
                    v-if="streaming && i === msgs.length - 1 && m.role === 'assistant'"
                    class="typing"
                  />
                  <div v-if="m.provider || m.routedModel || m.upstream || m.elapsedMS" class="msg-provider">
                    <span v-if="m.provider">上游渠道：{{ m.provider }}</span>
                    <span v-if="m.upstream"> · 命中上游：{{ m.upstream }}</span>
                    <span v-if="m.routedModel"> · 自动选用：{{ m.routedModel }}</span>
                    <span v-if="m.difficulty">
                      （难度 {{ m.difficulty.score }} · {{ tierLabel(m.difficulty.tier) }}：{{
                        m.difficulty.reason
                      }}）
                    </span>
                    <span v-if="m.elapsedMS"> · {{ m.elapsedMS }} ms</span>
                  </div>
                  <div v-if="m.content && !(streaming && i === msgs.length - 1)" class="msg-actions">
                    <button class="msg-act" @click="copyMsg(m)">复制</button>
                    <button
                      v-if="m.role === 'assistant' && i === msgs.length - 1"
                      class="msg-act"
                      @click="regenerate"
                    >
                      重新生成
                    </button>
                  </div>
                </div>
              </div>
            </div>
            <div class="chat-input">
              <!-- 待发送的图片：发送前可以逐个摘掉 -->
              <div v-if="attachments.length" class="attach-strip">
                <div v-for="a in attachments" :key="a.id" class="attach-item">
                  <img :src="a.data" :alt="a.name" :title="a.name" />
                  <button
                    type="button"
                    class="attach-x"
                    aria-label="移除图片"
                    @click="removeAttachment(a.id)"
                  >
                    ×
                  </button>
                </div>
                <span class="muted" style="font-size: 11.5px; align-self: center">
                  已附 {{ attachments.length }} 张，发送后清空
                </span>
              </div>

              <textarea
                ref="chatInputEl"
                v-model="input"
                class="textarea chat-input-textarea"
                rows="2"
                placeholder="输入消息，Enter 发送 / Shift+Enter 换行；可直接 Ctrl+V 粘贴截图"
                @keydown.enter.exact.prevent="sendChat"
                @paste="onPaste"
                @input="autoGrow"
                @dragover.prevent
                @drop.prevent="(e) => e.dataTransfer?.files && addAttachments(e.dataTransfer.files)"
              />
              <div class="row" style="margin-top: 10px">
                <label class="btn btn-sm btn-ghost" style="cursor: pointer" title="添加图片（最多 4 张，单张 ≤ 4MB）">
                  + 图片
                  <input
                    type="file"
                    accept="image/*"
                    multiple
                    style="display: none"
                    @change="onPickFiles"
                  />
                </label>
                <span class="muted" style="font-size: 12px">
                  {{ selModel ? '目标模型：' + selModel : '请选择模型' }}
                </span>
                <div class="spacer" />
                <button v-if="streaming" class="btn btn-danger btn-sm" @click="stopChat">停止</button>
                <button
                  class="btn btn-primary btn-sm"
                  :class="{ 'is-loading': streaming }"
                  :disabled="streaming || !modelOptions.length"
                  @click="sendChat"
                >
                  {{ streaming ? '生成中…' : '发送' }}
                </button>
              </div>
            </div>
          </div>
        </template>

        <!-- 图片 -->
        <template v-else-if="capability === 'image'">
          <div class="card" style="margin-bottom: 16px">
            <div class="field" style="margin-bottom: 10px">
              <label>图片提示词</label>
              <textarea v-model="input" class="textarea" rows="2" placeholder="例如：一只戴墨镜的柴犬" />
            </div>

            <div class="grid cols-3" style="gap: 0 14px">
              <div class="field">
                <label>尺寸</label>
                <select v-model="imageSizeMode" class="select">
                  <option v-for="s in sizePresets" :key="s" :value="s">{{ s }}（档位）</option>
                  <option value="custom">自定义</option>
                </select>
              </div>
              <div class="field">
                <label>宽高比</label>
                <select v-model="imageRatio" class="select" :disabled="imageSizeMode === 'custom'">
                  <option v-for="r in ratios" :key="r" :value="r">{{ r }}</option>
                </select>
              </div>
              <div class="field">
                <label>返回格式</label>
                <select v-model="imageFormat" class="select">
                  <option value="">上游默认</option>
                  <option value="url">url</option>
                  <option value="b64_json">b64_json</option>
                </select>
              </div>
            </div>

            <div v-if="imageSizeMode === 'custom'" class="field">
              <label>精确尺寸</label>
              <input v-model="imageSizeCustom" class="input code" placeholder="1024x1024" />
            </div>

            <div class="field">
              <label>参考图（图生图 / 多图合成，可留空）</label>
              <div v-for="(_, i) in imageRefs" :key="'ref' + i" class="list-row">
                <input
                  :value="imageRefs[i]"
                  class="input code"
                  placeholder="https://example.com/input.png"
                  @input="updateRef(i, $event)"
                />
                <label class="btn btn-sm btn-ghost" style="cursor: pointer; white-space: nowrap">
                  上传
                  <input type="file" accept="image/*" style="display: none" @change="pickImageRef(i, $event)" />
                </label>
                <button class="btn btn-sm btn-ghost" @click="imageRefs.splice(i, 1)">移除</button>
              </div>
              <button class="btn btn-sm" @click="addImageRef">+ 添加参考图</button>
              <div class="hint">
                填公共可访问的图片 URL 最省事；本地图片会转成 data URI 一并提交，单张建议不超过 4MB。
              </div>
            </div>

            <div class="row">
              <div class="spacer" />
              <button class="btn btn-primary btn-sm" :disabled="generating" @click="genImage">
                {{ generating ? '生成中…' : '生成图片' }}
              </button>
            </div>
          </div>
          <div v-if="generating" class="card row"><span class="spinner" /> <span class="muted">正在生成…</span></div>
          <div v-else-if="images.length">
            <div v-if="imageProvider" class="row" style="margin-bottom: 10px">
              <span class="badge brand">上游渠道：{{ imageProvider }}</span>
            </div>
            <div class="media-grid">
              <div v-for="(src, i) in images" :key="i" class="media-card">
                <img :src="src" alt="生成结果" />
                <div class="cap">
                  <span>结果 {{ i + 1 }}</span>
                  <span>
                    <a :href="src" target="_blank" rel="noreferrer">打开原图</a>
                    <a :href="src" :download="imgDownloadName(i, src)" style="margin-left: 8px">下载</a>
                  </span>
                </div>
              </div>
            </div>
          </div>
          <div v-else class="card empty">
            <div class="big">还没有生成结果</div>
            <div>填写提示词后点击「生成图片」。</div>
          </div>
        </template>

        <!-- 视频 -->
        <template v-else>
          <div class="card" style="margin-bottom: 16px">
            <div class="field" style="margin-bottom: 10px">
              <label>视频提示词</label>
              <textarea
                v-model="input"
                class="textarea"
                rows="2"
                placeholder="例如：雨后的未来城市街道，霓虹灯倒映在地面，电影级运镜"
              />
            </div>

            <div class="grid cols-3" style="gap: 0 14px">
              <div class="field">
                <label>生成方式</label>
                <select v-model="videoMode" class="select">
                  <option v-for="m in videoModes" :key="m.value" :value="m.value">{{ m.label }}</option>
                </select>
              </div>
              <div class="field">
                <label>时长</label>
                <select v-model="videoDuration" class="select">
                  <option v-for="d in videoDurations" :key="d" :value="d">{{ d }} 秒</option>
                </select>
              </div>
              <div class="field">
                <label>画幅</label>
                <select v-model="videoRatio" class="select">
                  <option v-for="r in videoRatios" :key="r" :value="r">{{ r }}</option>
                </select>
              </div>
            </div>

            <template v-if="videoMode === 'keyframe'">
              <div class="grid cols-2" style="gap: 0 14px">
                <div class="field">
                  <label>首帧图 URL</label>
                  <input
                    v-model="videoFirstFrame"
                    class="input code"
                    placeholder="https://example.com/first.png"
                  />
                </div>
                <div class="field">
                  <label>尾帧图 URL（可留空）</label>
                  <input
                    v-model="videoLastFrame"
                    class="input code"
                    placeholder="https://example.com/last.png"
                  />
                </div>
              </div>
              <div class="hint" style="margin-bottom: 12px">首帧与尾帧至少填写一个。</div>
            </template>

            <template v-if="videoMode === 'reference'">
              <div class="field">
                <label>参考图（Agnes Video 2.5 Flash 最多 5 张）</label>
                <div v-for="(_, i) in videoRefImages" :key="'vimg' + i" class="list-row">
                  <input
                    :value="videoRefImages[i]"
                    class="input code"
                    placeholder="https://example.com/ref.png"
                    @input="updateVideoRef('images', i, $event)"
                  />
                  <label class="btn btn-sm btn-ghost" style="cursor: pointer; white-space: nowrap">
                    上传
                    <input
                      type="file"
                      accept="image/*"
                      style="display: none"
                      @change="pickVideoImage(i, $event)"
                    />
                  </label>
                  <button class="btn btn-sm btn-ghost" @click="removeVideoRef('images', i)">移除</button>
                </div>
                <button class="btn btn-sm" @click="addVideoRef('images')">+ 添加参考图</button>
              </div>
              <div class="field">
                <label>参考音频（Agnes Video 2.5 Flash 最多 3 段）</label>
                <div v-for="(_, i) in videoRefAudios" :key="'vaud' + i" class="list-row">
                  <input
                    :value="videoRefAudios[i]"
                    class="input code"
                    placeholder="https://example.com/ref.mp3"
                    @input="updateVideoRef('audios', i, $event)"
                  />
                  <button class="btn btn-sm btn-ghost" @click="removeVideoRef('audios', i)">移除</button>
                </div>
                <button class="btn btn-sm" @click="addVideoRef('audios')">+ 添加参考音频</button>
              </div>
              <div class="hint" style="margin-bottom: 12px">
                提示词里可用 &lt;Picture 1&gt;、&lt;Audio 1&gt; 指代对应素材；参考音频仅部分上游支持。
              </div>
            </template>

            <div class="row">
              <div class="spacer" />
              <button
                class="btn btn-primary btn-sm"
                :class="{ 'is-loading': videoBusy }"
                :disabled="videoBusy || !modelOptions.length"
                @click="genVideo"
              >
                {{ videoBusy ? '处理中…' : '创建视频任务' }}
              </button>
            </div>
          </div>

          <div v-if="videoTask" class="card">
            <div class="row" style="justify-content: space-between">
              <span class="mono dim">{{ videoTask.id }}</span>
              <span class="row" style="gap: 6px">
                <span v-if="videoTask.provider" class="badge brand">上游 {{ videoTask.provider }}</span>
                <span class="badge" :class="taskBadge(videoTask.status)">{{ taskLabel(videoTask.status) }}</span>
              </span>
            </div>
            <div class="progress" style="margin: 12px 0">
              <i :style="{ width: (videoTask.progress || 0) + '%' }" />
            </div>
            <div class="muted" style="font-size: 12.5px">
              创建于 {{ fmtTime(videoTask.created_at) }} · 更新于 {{ fmtTime(videoTask.updated_at) }}
            </div>
            <div v-if="videoTask.error" class="badge danger" style="margin-top: 10px">
              {{ videoTask.error.message }}
            </div>
            <video
              v-if="videoTask.video_url"
              :src="videoTask.video_url"
              controls
              style="width: 100%; margin-top: 14px; border-radius: 10px; background: #000"
            />
            <div v-else-if="videoTask.status !== 'failed'" class="muted" style="margin-top: 12px">
              视频生成中，页面会自动刷新状态……
            </div>
          </div>
          <div v-else class="card empty">
            <div class="big">还没有视频任务</div>
            <div>填写提示词后点击「创建视频任务」，生成期间会自动轮询状态。</div>
          </div>
        </template>
      </div>
    </div>
  </div>
</template>
