<script setup lang="ts">
import { computed, nextTick, onMounted, reactive, ref, watch } from 'vue'
import { api, streamChat, type Skill, type VideoTask } from '../api'
import { models, skills, state, toast } from '../store'
import { capLabel, fmtTime, renderMarkdown } from '../utils'

interface ChatMsg {
  role: string
  content: string
  reasoning?: string
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

const imageSize = ref('1024x1024')
const imageSizes = ['1024x1024', '1536x1024', '1024x1536', '512x512']

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
  const body: any = { model: selModel.value, size: imageSize.value, ...skillBody() }
  if (prompt) body.prompt = prompt
  generating.value = true
  images.value = []
  try {
    const res = await api.playgroundImage(body)
    const items = res?.data || []
    images.value = items
      .map((it: any) => it.url || (it.b64_json ? 'data:image/png;base64,' + it.b64_json : ''))
      .filter(Boolean)
    if (!images.value.length) toast('未返回图片', 'error')
  } catch (e: any) {
    toast(e.message || '生成失败', 'error')
  } finally {
    generating.value = false
  }
}

// ---------- 视频 ----------

const videoDuration = ref(5)
const videoDurations = [5, 8, 10]

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
    duration: videoDuration.value,
    ...skillBody(),
  }
  if (prompt) body.prompt = prompt
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
            <option v-if="!capModels.length" value="">（无可用{{ capLabel(capability) }}模型）</option>
            <option v-for="m in capModels" :key="m.id" :value="m.name">{{ m.name }}</option>
          </select>
          <div v-if="capModels.length" class="hint">
            {{ capModels.find((m) => m.name === selModel)?.provider_name }}
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
            <div class="row">
              <div class="spacer" />
              <button class="btn btn-primary btn-sm" :disabled="generating" @click="genImage">
                {{ generating ? '生成中…' : '生成图片' }}
              </button>
            </div>
          </div>
          <div v-if="generating" class="card row"><span class="spinner" /> <span class="muted">正在生成…</span></div>
          <div v-else-if="images.length" class="media-grid">
            <div v-for="(src, i) in images" :key="i" class="media-card">
              <img :src="src" alt="生成结果" />
              <div class="cap">
                <span>结果 {{ i + 1 }}</span>
                <a :href="src" target="_blank" rel="noreferrer">打开原图</a>
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
              <textarea v-model="input" class="textarea" rows="2" placeholder="例如：海边日落，镜头缓慢推进" />
            </div>
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
              <span class="badge" :class="taskBadge(videoTask.status)">{{ taskLabel(videoTask.status) }}</span>
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
