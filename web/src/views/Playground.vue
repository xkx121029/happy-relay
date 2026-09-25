<script setup lang="ts">
import { computed, nextTick, onMounted, reactive, ref, watch } from 'vue'
import { api, streamChat, type Skill, type VideoTask } from '../api'
import { models, skills, state, toast } from '../store'
import { capLabel, fmtTime, renderMarkdown } from '../utils'

interface ChatMsg {
  role: string
  content: string
  reasoning?: string
  /** 本次回复实际命中的上游渠道 */
  provider?: string
  /** auto 模式下实际选中的模型 */
  routedModel?: string
  /** auto 模式下的难度评估 */
  difficulty?: { score: number; tier: string; reason: string }
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

onMounted(load)

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
})

watch(capModels, (list) => {
  if (!list.some((m) => m.name === selModel.value)) {
    selModel.value = list[0]?.name || ''
  }
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

// ---------- 对话 ----------

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
  if (selSkill.value) {
    // 技能模式：由后端渲染模板，这里只展示套用的技能与参数。
    const desc = Object.entries(skillParams)
      .filter(([, v]) => v)
      .map(([k, v]) => `${k}：${v}`)
      .join('；')
    msgs.value.push({ role: 'user', content: `【技能】${currentSkill.value?.name}${desc ? '（' + desc + '）' : ''}` })
  } else {
    if (!text) {
      toast('请输入内容', 'error')
      return
    }
    msgs.value.push({ role: 'user', content: text })
  }

  const body: any = { model: selModel.value, stream: true, ...skillBody() }
  if (!selSkill.value) {
    body.messages = msgs.value.map((m) => ({ role: m.role, content: m.content }))
  }
  input.value = ''
  const idx = msgs.value.push({ role: 'assistant', content: '', reasoning: '' }) - 1
  streaming.value = true
  controller = new AbortController()
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
        if (meta.model && meta.model !== selModel.value) m.routedModel = meta.model
        if (meta.difficulty) m.difficulty = meta.difficulty
      },
      onDone: () => {
        streaming.value = false
        controller = null
      },
    },
    controller.signal,
  )
  streaming.value = false
}

function clearChat() {
  stopChat()
  msgs.value = []
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

        <template v-if="capability === 'image'">
          <div class="field" style="margin-top: 12px">
            <label>尺寸</label>
            <select v-model="imageSize" class="select">
              <option v-for="s in imageSizes" :key="s" :value="s">{{ s }}</option>
            </select>
          </div>
        </template>

        <template v-if="capability === 'video'">
          <div class="field" style="margin-top: 12px">
            <label>时长（秒）</label>
            <select v-model.number="videoDuration" class="select">
              <option v-for="d in videoDurations" :key="d" :value="d">{{ d }} 秒</option>
            </select>
          </div>
        </template>
      </div>

      <!-- 右侧主区域 -->
      <div>
        <!-- 对话 -->
        <template v-if="capability === 'chat'">
          <div class="chat-wrap">
            <div ref="streamEl" class="chat-stream">
              <div v-if="!msgs.length" class="empty" style="margin: auto">
                <div class="big">开始一次对话</div>
                <div>选择模型后输入内容，回复会逐字流式输出。</div>
              </div>
              <div v-for="(m, i) in msgs" :key="i" class="msg" :class="m.role">
                <div class="avatar">{{ m.role === 'user' ? '我' : 'AI' }}</div>
                <div class="bubble">
                  <div v-if="m.reasoning" class="reasoning">{{ m.reasoning }}</div>
                  <div v-if="m.role === 'assistant'" v-html="renderMarkdown(m.content)" />
                  <div v-else style="white-space: pre-wrap">{{ m.content }}</div>
                  <span
                    v-if="streaming && i === msgs.length - 1 && m.role === 'assistant'"
                    class="typing"
                  />
                  <div v-if="m.provider || m.routedModel" class="msg-provider">
                    <span v-if="m.provider">上游渠道：{{ m.provider }}</span>
                    <span v-if="m.routedModel"> · 自动选用：{{ m.routedModel }}</span>
                    <span v-if="m.difficulty">
                      （难度 {{ m.difficulty.score }} · {{ tierLabel(m.difficulty.tier) }}：{{
                        m.difficulty.reason
                      }}）
                    </span>
                  </div>
                </div>
              </div>
            </div>
            <div class="chat-input">
              <textarea
                v-model="input"
                class="textarea"
                rows="2"
                placeholder="输入消息，Enter 发送 / Shift+Enter 换行"
                @keydown.enter.exact.prevent="sendChat"
              />
              <div class="row" style="margin-top: 10px">
                <span class="muted" style="font-size: 12px">
                  {{ selModel ? '目标模型：' + selModel : '请选择模型' }}
                </span>
                <div class="spacer" />
                <button v-if="streaming" class="btn btn-danger btn-sm" @click="stopChat">停止</button>
                <button class="btn btn-primary btn-sm" :disabled="streaming" @click="sendChat">
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
                  <a :href="src" target="_blank" rel="noreferrer">打开原图</a>
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
              <button class="btn btn-primary btn-sm" :disabled="videoBusy" @click="genVideo">
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
