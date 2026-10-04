<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { api, type UsageLog } from '../api'
import { renderMarkdown } from '../utils'

// ---------- 用户门户（多页） ----------
// 管理员用面板（/）；持秘钥用户在这里自助：概览 / 模型 / 每模型自动生成文档 / 调用记录 / 快速体验。
// 秘钥只存本机 localStorage，所有请求都带它实时认证。

const KEY_STORE = 'userportal.key'

const key = ref('')
const loginInput = ref('')
const loginErr = ref('')
const loginBusy = ref(false)
const me = ref<any>(null)
const models = ref<any[]>([])
const logs = ref<UsageLog[]>([])
const logOk = ref(0)
const logFail = ref(0)
const loading = ref(false)
const page = ref<'overview' | 'models' | 'docs' | 'logs' | 'play'>('overview')

interface ChatMsg {
  role: 'user' | 'assistant'
  content: string
}
const chatModel = ref('')
const chatInput = ref('')
const chatMsgs = ref<ChatMsg[]>([])
const chatBusy = ref(false)
const chatEl = ref<HTMLElement | null>(null)
// 文档页当前选中的模型
const docModel = ref('')

const baseURL = computed(() => location.origin + '/v1')

// ---------- 未登录主页（公开信息，无需秘钥） ----------
const pub = ref<Awaited<ReturnType<typeof api.userPublic>> | null>(null)
const copied = ref(false)
// 价值主张（静态文案：讲清"为什么用它"，不泄露任何内部信息）
const perks = [
  { title: '一个端点全覆盖', desc: '对话 / 图像 / 视频 / 向量共用同一套地址与秘钥。' },
  { title: '自动故障转移', desc: '同一模型挂多个上游，某个挂了自动换下一个继续服务。' },
  { title: '用量透明可查', desc: '随时查看自己的调用次数、Token 消耗与每条记录。' },
]
// 右侧速通卡里的三步（极简版，细节留给下方"怎么接入"）
const quickSteps = ['拿到管理员给的 sk- 秘钥', '把 base_url 指向本服务', '照常调用，代码不用改']
// 下方"怎么接入"的完整步骤
const steps = [
  { t: '拿到秘钥', d: '向管理员索取 sk- 开头的秘钥，或在面板「秘钥」页自助创建。' },
  { t: '填两个参数', d: 'Base URL 指向本服务，API Key 填你的秘钥；其余参数保持默认。' },
  { t: '照常写代码', d: '官方 SDK 直接可用；也可以用 curl / 任意兼容 OpenAI 协议的客户端。' },
]

async function loadPublic() {
  try {
    pub.value = await api.userPublic()
  } catch {
    // 后端不可达时页面照常展示，只是数字为空
  }
}
onMounted(loadPublic)

function focusLogin() {
  document.getElementById('pt-login-box')?.scrollIntoView({ behavior: 'smooth', block: 'center' })
  setTimeout(() => (document.querySelector('.pt-login .input') as HTMLInputElement)?.focus(), 380)
}

async function copyBase() {
  try {
    await navigator.clipboard.writeText(baseURL.value)
    copied.value = true
    setTimeout(() => (copied.value = false), 1600)
  } catch {
    /* 剪贴板不可用时忽略 */
  }
}

function capLabel(cap: string) {
  return (
    { chat: '文本对话', image: '图像生成', video: '视频生成', embedding: '向量检索', speech: '语音合成', transcription: '语音转写' } as Record<string, string>
  )[cap] || cap
}
// 能力图标用内联 SVG 而不是 emoji：emoji 在部分环境（无彩色字体/服务端渲染）会变成方块。
const CAP_ICONS: Record<string, string[]> = {
  chat: ['M21 12a8 8 0 0 1-8 8H7l-4 3v-5.5A8 8 0 0 1 11 4h2a8 8 0 0 1 8 8Z'],
  image: ['M3 5h18v14H3z', 'm3 16 5-5 4 4 3-3 6 6', 'M8.5 9.5a1.2 1.2 0 1 0 0-2.4 1.2 1.2 0 0 0 0 2.4Z'],
  video: ['M3 6h12v12H3z', 'm15 10 6-3v10l-6-3z'],
  embedding: ['M11 4a7 7 0 1 0 0 14 7 7 0 0 0 0-14Z', 'm20 20-3.6-3.6'],
  speech: ['M4 10v4', 'M8 6v12', 'M12 4v16', 'M16 7v10', 'M20 11v2'],
  transcription: ['M6 3h8l4 4v14H6z', 'M14 3v4h4', 'M9 12h6', 'M9 16h4'],
}
function capIcon(cap: string) {
  return CAP_ICONS[cap] || ['M12 3l2.6 6.4L21 12l-6.4 2.6L12 21l-2.6-6.4L3 12l6.4-2.6z']
}
// 每种能力一个专属高饱和色相（HSL 的 hue），卡片/图标/发光都靠它统一。
const CAP_HUES: Record<string, number> = {
  chat: 217, //  electric blue
  image: 322, // magenta
  video: 28, //  orange
  embedding: 190, // cyan
  speech: 152, // emerald
  transcription: 268, // violet
}
function capHue(cap: string) {
  return CAP_HUES[cap] ?? 217
}
const chatCapable = computed(() => models.value.filter((m) => !m.capability || m.capability === 'chat'))
const docMeta = computed(() => models.value.find((m) => m.name === docModel.value) || null)

async function loadAll(k: string) {
  loading.value = true
  try {
    const [m, ms, lg] = await Promise.all([api.userMe(k), api.userModels(k), api.userLogs(k, 100)])
    me.value = m
    models.value = ms
    logs.value = lg.logs || []
    logOk.value = lg.ok
    logFail.value = lg.fail
    if (!chatModel.value && chatCapable.value.length) chatModel.value = chatCapable.value[0].name
    if (!docModel.value && models.value.length) docModel.value = models.value[0].name
  } catch (e: any) {
    localStorage.removeItem(KEY_STORE)
    key.value = ''
    me.value = null
    loginErr.value = e.message || '秘钥校验失败'
  } finally {
    loading.value = false
  }
}

async function login() {
  const k = loginInput.value.trim()
  if (!k || loginBusy.value) return
  loginBusy.value = true
  loginErr.value = ''
  try {
    await api.userMe(k)
    key.value = k
    localStorage.setItem(KEY_STORE, k)
    await loadAll(k)
  } catch (e: any) {
    loginErr.value = e.message || '秘钥无效'
  } finally {
    loginBusy.value = false
  }
}

function logout() {
  localStorage.removeItem(KEY_STORE)
  key.value = ''
  me.value = null
  chatMsgs.value = []
  loginInput.value = ''
}

function refresh() {
  if (key.value) loadAll(key.value)
}


// ---------- 管理员可视化编辑 ----------
// 面板与门户同源部署：探测管理面板 API 可达（同源且有面板能力）即认为是管理员，
// 普通用户（只拿到秘钥、在其他域名打开）不会看到编辑入口。
const isAdmin = ref(false)
const editMode = ref(false)
const notice = ref('')
const noticeDraft = ref('')
const savingPortal = ref(false)
const portalMsg = ref('')

// 模型介绍手动覆盖（编辑态草稿）
const introEditing = ref(false)
const introDraft = ref('')

function flashPortalMsg(t: string) {
  portalMsg.value = t
  setTimeout(() => (portalMsg.value = ''), 2000)
}

async function savePortal(content: { notice?: string }) {
  if (savingPortal.value) return
  savingPortal.value = true
  try {
    await api.portalSave(content)
    flashPortalMsg('已保存 ✓')
  } catch (e: any) {
    flashPortalMsg('保存失败：' + (e.message || '未知错误'))
  } finally {
    savingPortal.value = false
  }
}

async function saveNotice() {
  notice.value = noticeDraft.value.trim()
  await savePortal({ notice: noticeDraft.value.trim() })
}

function toggleEdit() {
  editMode.value = !editMode.value
  introEditing.value = false
  if (editMode.value) {
    noticeDraft.value = notice.value
  }
}

function startIntroEdit() {
  introEditing.value = true
  introDraft.value = introMap.value[docModel.value] || docsFor(docMeta.value!).intro
}

async function saveIntro() {
  const name = docModel.value
  if (!name || !key.value || !introDraft.value.trim()) return
  introEditing.value = false
  try {
    const r = await api.userModelIntroSave(key.value, name, introDraft.value)
    introMap.value = { ...introMap.value, [name]: r.intro }
    flashPortalMsg('介绍已更新 ✓')
  } catch (e: any) {
    flashPortalMsg('保存失败：' + (e.message || '未知错误'))
  }
}

onMounted(async () => {
  const k = localStorage.getItem(KEY_STORE)
  if (k) {
    key.value = k
    await loadAll(k)
  }
  // 门户公告（管理员配置了才有）。
  try {
    const r = await api.portalLinks()
    notice.value = r.notice || ''
  } catch {
    /* 拉不到就不显示 */
  }
  // 探测面板 API：可达则认为是管理员，展示可视化编辑入口。
  try {
    const res = await fetch('/api/meta')
    isAdmin.value = res.ok
  } catch {
    isAdmin.value = false
  }
})

function pct(used: number, total: number): number {
  if (!total) return 0
  return Math.min(100, Math.round((used / total) * 100))
}
function fmtTime(ts: string): string {
  if (!ts) return '—'
  return ts.length >= 16 ? ts.slice(5, 16).replace('T', ' ') : ts
}
const capBadge = (c: string) => ({ chat: '对话', image: '绘图', video: '视频', embedding: '向量', speech: '语音' }[c] || c || '—')

// ---------- 每个模型自动生成文档 ----------
interface DocSnippet {
  title: string
  code: string
  lang: string
}
function docsFor(m: any): { intro: string; endpoint: string; snippets: DocSnippet[]; notes: string[] } {
  const curl = (path: string, body: string) =>
    `curl ${location.origin}${path} \\\n  -H "Authorization: Bearer <你的秘钥>" \\\n  -H "Content-Type: application/json" \\\n  -d '${body}'`
  const py = (code: string) =>
    `from openai import OpenAI\n\nclient = OpenAI(\n    base_url="${baseURL.value}",\n    api_key="<你的秘钥>",\n)\n\n` + code
  switch (m.capability) {
    case 'image':
      return {
        intro: `${m.name} 是绘图模型：提交文字描述，返回生成图片的 URL。`,
        endpoint: 'POST /v1/images/generations',
        snippets: [
          { title: 'cURL', lang: 'bash', code: curl('/v1/images/generations', JSON.stringify({ model: m.name, prompt: '一只在雪地里打滚的萨摩耶，电影感光线', size: '1024x1024', n: 1 }, null, 2)) },
          {
            title: 'Python（OpenAI SDK）',
            lang: 'python',
            code: py(`resp = client.images.generate(\n    model="${m.name}",\n    prompt="一只在雪地里打滚的萨摩耶，电影感光线",\n    size="1024x1024",\n    n=1,\n)\nprint(resp.data[0].url)`),
          },
        ],
        notes: ['响应 data[].url 为图片链接（或 b64_json），有效期有限，建议及时转存。', '出图耗时通常 5~30 秒，超时由服务端统一兜底。'],
      }
    case 'video':
      return {
        intro: `${m.name} 是视频模型：提交任务后轮询进度，完成后拉取视频流或链接。`,
        endpoint: 'POST /v1/videos → GET /v1/videos/{id}',
        snippets: [
          { title: '① 提交任务', lang: 'bash', code: curl('/v1/videos', JSON.stringify({ model: m.name, prompt: '一只柯基在海边奔跑，日落，慢镜头', duration: 5 }, null, 2)) },
          { title: '② 轮询任务', lang: 'bash', code: `curl ${location.origin}/v1/videos/<task_id> \\\n  -H "Authorization: Bearer <你的秘钥>"` },
          { title: '③ 播放/下载', lang: 'bash', code: `# 任务完成后直接取视频流\n${location.origin}/v1/videos/<task_id>/stream` },
        ],
        notes: ['status: pending → running → succeeded / failed；视频生成通常 1~10 分钟。', '轮询建议 10 秒一次；也可以把 stream 链接直接交给 <video> 标签播放。'],
      }
    case 'embedding':
      return {
        intro: `${m.name} 是向量模型：把文本转成语义向量，用于检索、聚类与相似度计算。`,
        endpoint: 'POST /v1/embeddings',
        snippets: [
          { title: 'cURL', lang: 'bash', code: curl('/v1/embeddings', JSON.stringify({ model: m.name, input: '今天天气怎么样？' }, null, 2)) },
          { title: 'Python（OpenAI SDK）', lang: 'python', code: py(`resp = client.embeddings.create(\n    model="${m.name}",\n    input=["今天天气怎么样？"],\n)\nprint(resp.data[0].embedding[:8], "...")`) },
        ],
        notes: ['响应 data[].embedding 为浮点数组；批量传入 list 可一次编码多条。'],
      }
    case 'speech':
      return {
        intro: `${m.name} 是语音合成模型：文字转语音，返回音频二进制。`,
        endpoint: 'POST /v1/audio/speech',
        snippets: [
          { title: 'cURL（保存为 mp3）', lang: 'bash', code: `curl ${location.origin}/v1/audio/speech \\\n  -H "Authorization: Bearer <你的秘钥>" \\\n  -H "Content-Type: application/json" \\\n  -d '{"model":"${m.name}","input":"你好，欢迎试玩","voice":"tongtong"}' \\\n  --output hello.mp3` },
        ],
        notes: ['响应体是音频文件流；voice 可选音色以模型支持为准。'],
      }
    default:
      return {
        intro: `${m.name} 是对话模型：发送消息数组，返回助手回复；支持流式输出。`,
        endpoint: 'POST /v1/chat/completions',
        snippets: [
          { title: 'cURL（流式）', lang: 'bash', code: curl('/v1/chat/completions', JSON.stringify({ model: m.name, messages: [{ role: 'user', content: '你好，介绍一下你自己' }], stream: true }, null, 2)) },
          { title: 'Python（OpenAI SDK，流式）', lang: 'python', code: py(`stream = client.chat.completions.create(\n    model="${m.name}",\n    messages=[{"role": "user", "content": "你好，介绍一下你自己"}],\n    stream=True,\n)\nfor chunk in stream:\n    delta = chunk.choices[0].delta.content or ""\n    print(delta, end="", flush=True)`) },
        ],
        notes: ['messages 按 role/user / assistant / system 顺序传，上下文由调用方维护。', '流式响应为 SSE：data: {...} 行，delta.content 逐段拼接，data: [DONE] 结束。'],
      }
  }
}

// ---------- 模型介绍（主模型撰写，markdown；''=已请求但无内容，回退内置简介） ----------
const introMap = ref<Record<string, string>>({})
const introLoading = ref('')
const docTab = ref<'openai' | 'advanced'>('openai')

async function loadIntro(name: string, refresh = false) {
  if (!name || !key.value) return
  if (!refresh && introMap.value[name] !== undefined) return
  introLoading.value = name
  try {
    const r = await api.userModelIntro(key.value, name, refresh)
    introMap.value = { ...introMap.value, [name]: r.intro || '' }
  } catch {
    introMap.value = { ...introMap.value, [name]: '' }
  } finally {
    if (introLoading.value === name) introLoading.value = ''
  }
}
function regenerateIntro() {
  if (docModel.value) loadIntro(docModel.value, true)
}
/** 从文档页一键跳到快速体验（对话模型才可跳） */
function tryItNow() {
  const m = docMeta.value
  if (!m) return
  if (!m.capability || m.capability === 'chat') {
    chatModel.value = m.name
    page.value = 'play'
  } else {
    page.value = 'play'
  }
}
watch(docModel, (n) => {
  docTab.value = 'openai'
  introEditing.value = false
  loadIntro(n)
})
// ---------- 页面切换动画（方向感知、可打断） ----------
const PAGE_ORDER: Record<string, number> = { overview: 0, models: 1, docs: 2, logs: 3, play: 4 }
const animName = ref('pt-fwd')
/** 底部标签栏（手机档） */
const tabs = [
  { id: 'overview', ico: '◉', label: '概览' },
  { id: 'models', ico: '▣', label: '模型' },
  { id: 'docs', ico: '❰❱', label: '文档' },
  { id: 'logs', ico: '≡', label: '记录' },
  { id: 'play', ico: '✦', label: '体验' },
] as const
watch(page, (n, o) => {
  // 目标页在导航序里更靠后 → 前进（左推入）；否则后退。快速连点也能从当前状态续接动画。
  animName.value = (PAGE_ORDER[n] ?? 0) >= (PAGE_ORDER[o] ?? 0) ? 'pt-fwd' : 'pt-back'
  // 切页回顶：立即生效，避免旧滚动位置穿帮
  window.scrollTo({ top: 0 })
  if (n === 'docs') loadIntro(docModel.value)
})

/** 进阶说明：统一错误格式示例 */
const advancedSnippets = computed<DocSnippet[]>(() => [
  {
    title: '统一错误格式（所有能力一致，HTTP 4xx/5xx）',
    lang: 'json',
    code: JSON.stringify(
      {
        error: {
          message: 'Incorrect API key provided.',
          type: 'invalid_request_error',
          code: 'invalid_api_key',
        },
      },
      null,
      2,
    ),
  },
])

/** 进阶说明：通用注意事项（带当前秘钥的限速/配额实况） */
const advancedNotes = computed<string[]>(() => {
  const rpm = me.value?.rpm
  const quota = me.value?.daily_quota
  return [
    '认证统一走 Authorization: Bearer <秘钥>（或 X-API-Key 头），所有 /v1 端点通用。',
    rpm ? `当前秘钥限速 ${rpm} 次/分钟；超限返回 429，请客户端做退避重试。` : '超限会返回 429，请客户端做退避重试。',
    quota && quota > 0 ? `每日配额 ${quota} 次，用完当天返回 429，次日自动恢复。` : '每日配额以秘钥设置为准，用完当天返回 429。',
    '一个对外模型背后可能挂多条上游链路：某条失败会自动切换下一条，对调用方透明。',
    '流式响应为 SSE：data: {...} 行，delta.content 逐段拼接，data: [DONE] 结束。',
  ]
})

// ---------- 快速体验 ----------
async function scrollChat() {
  await nextTick()
  if (chatEl.value) chatEl.value.scrollTop = chatEl.value.scrollHeight
}

async function sendChat() {
  const t = chatInput.value.trim()
  if (!t || chatBusy.value || !chatModel.value) return
  chatInput.value = ''
  chatMsgs.value.push({ role: 'user', content: t })
  const ai = chatMsgs.value.push({ role: 'assistant', content: '' }) - 1
  chatBusy.value = true
  scrollChat()
  try {
    const res = await fetch('/v1/chat/completions', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + key.value },
      body: JSON.stringify({ model: chatModel.value, messages: [{ role: 'user', content: t }], stream: true }),
    })
    if (!res.ok || !res.body) {
      const text = await res.text().catch(() => '')
      let msg = `HTTP ${res.status}`
      try {
        msg = JSON.parse(text)?.error?.message || msg
      } catch {}
      throw new Error(msg)
    }
    const reader = res.body.getReader()
    const dec = new TextDecoder()
    let buf = ''
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      buf += dec.decode(value, { stream: true })
      const lines = buf.split('\n')
      buf = lines.pop() || ''
      for (const ln of lines) {
        const s = ln.trim()
        if (!s.startsWith('data:')) continue
        const payload = s.slice(5).trim()
        if (payload === '[DONE]') continue
        try {
          const j = JSON.parse(payload)
          const delta = j.choices?.[0]?.delta?.content || ''
          if (delta) {
            chatMsgs.value[ai].content += delta
            scrollChat()
          }
        } catch {}
      }
    }
  } catch (e: any) {
    chatMsgs.value[ai].content += (chatMsgs.value[ai].content ? '\n' : '') + '⚠ ' + (e.message || '调用失败')
  } finally {
    chatBusy.value = false
    scrollChat()
    refresh()
  }
}

async function copyText(text: string, el: HTMLElement) {
  try {
    await navigator.clipboard.writeText(text)
    const old = el.textContent
    el.textContent = '已复制'
    setTimeout(() => (el.textContent = old), 1200)
  } catch {}
}
</script>

<template>
  <div class="portal">
    <!-- 未登录：公开主页（能看，不能用）+ 秘钥入口 -->
    <div v-if="!me" class="pt-home">
      <div class="pt-wrap">
        <!-- 顶栏 -->
        <header class="pt-top">
          <div class="pt-top-brand">
            <span class="pt-logo">H</span>
            <div class="pt-top-txt">
              <strong>{{ pub.service || '模型服务门户' }}</strong>
              <span class="muted">OpenAI 兼容 · {{ pub.total_models || 0 }} 个模型</span>
            </div>
          </div>
          <button class="btn btn-primary" @click="focusLogin">输入秘钥</button>
        </header>

        <!-- Hero：左文案 + 右速通卡 -->
        <section class="pt-home-hero">
          <div class="pt-home-hero-copy">
            <span class="pt-chip">◆ OpenAI 兼容接口</span>
            <h1>一个入口，<em>所有模型</em></h1>
            <p class="pt-home-hero-sub">
              对话、图像、视频、向量共用同一套地址与秘钥。把 SDK 的 base_url 指过来、api_key 填上，
              剩下的代码一行都不用改。
            </p>
            <ul class="pt-home-hero-points">
              <li v-for="(p, i) in perks" :key="i">
                <span class="pt-tick" :style="{ background: `hsl(${205 + i * 62} 92% 62%)` }" />
                <div>
                  <strong>{{ p.title }}</strong>
                  <span class="muted">{{ p.desc }}</span>
                </div>
              </li>
            </ul>
            <div class="pt-home-hero-cta">
              <button class="btn btn-primary btn-lg" @click="focusLogin">粘贴秘钥开始使用</button>
              <span class="muted">没有秘钥也能先逛逛</span>
            </div>
          </div>

          <!-- 速通卡：把「怎么用」浓缩成一张卡，右侧不留空 -->
          <aside class="pt-quick">
            <div class="pt-quick-head">
              <span class="muted">三步接入</span>
              <span class="pt-quick-badge">约 2 分钟</span>
            </div>
            <ol class="pt-quick-steps">
              <li v-for="(s, i) in quickSteps" :key="i">
                <span class="pt-quick-n">{{ i + 1 }}</span>
                <span class="muted">{{ s }}</span>
              </li>
            </ol>
            <div class="pt-quick-base">
              <span class="mono">{{ baseURL }}</span>
              <button class="btn btn-sm" @click="copyBase">{{ copied ? '已复制' : '复制' }}</button>
            </div>
          </aside>
        </section>

        <!-- 能力 Bento：对话占大格，其余小格 -->
        <section v-if="pub.capabilities?.length" class="pt-block">
          <h2 class="pt-h2">能做什么</h2>
          <div class="pt-bento">
            <div
              v-for="c in pub.capabilities"
              :key="c.capability"
              class="pt-tile"
              :class="{ big: c.capability === 'chat' }"
              :style="{ '--hue': capHue(c.capability) }"
            >
              <span class="pt-tile-icon">
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">
                  <path v-for="(d, i) in capIcon(c.capability)" :key="i" :d="d" />
                </svg>
              </span>
              <div class="pt-tile-body">
                <strong>{{ capLabel(c.capability) }}</strong>
                <span class="muted">{{ c.count }} 个模型</span>
              </div>
              <span v-if="c.capability === 'chat'" class="pt-tile-tag">主力能力</span>
            </div>
          </div>
        </section>

        <!-- 接入区：左步骤 + 右代码 -->
        <section class="pt-block">
          <h2 class="pt-h2">怎么接入</h2>
          <div class="pt-split">
            <ol class="pt-steps">
              <li v-for="(s, i) in steps" :key="i" :style="{ '--hue': 258 + i * 58 }">
                <span class="pt-step-n">{{ i + 1 }}</span>
                <div>
                  <strong>{{ s.t }}</strong>
                  <span class="muted">{{ s.d }}</span>
                </div>
              </li>
            </ol>
            <div class="pt-code">
              <div class="pt-code-head">
                <span class="pt-code-dots"><i /><i /><i /></span>
                <span class="mono">quickstart.py</span>
              </div>
              <pre class="mono"><code><span class="c">from</span> openai <span class="c">import</span> OpenAI

client = OpenAI(
    base_url=<span class="s">"{{ baseURL }}"</span>,
    api_key=<span class="s">"sk-你的秘钥"</span>,
)
resp = client.chat.completions.create(
    model=<span class="s">"门户里的模型名"</span>,
    messages=[{<span class="s">"role"</span>: <span class="s">"user"</span>, <span class="s">"content"</span>: <span class="s">"你好"</span>}],
)</code></pre>
            </div>
          </div>
        </section>

        <!-- 公告 -->
        <section v-if="pub.notice" class="pt-block">
          <h2 class="pt-h2">公告</h2>
          <div class="pt-home-notice" v-html="renderMarkdown(pub.notice)" />
        </section>

        <!-- 开始使用：左说明 + 右登录卡 -->
        <section class="pt-block">
          <h2 class="pt-h2">开始使用</h2>
          <div class="pt-split start">
            <div class="pt-start-copy">
              <p class="muted">秘钥只存在你自己的浏览器里，我们不做任何托管。登录后即可：</p>
              <ul>
                <li>查看自己的配额、每日消耗与到期时间</li>
                <li>阅读每个模型的自动生成说明与调用示例</li>
                <li>直接在门户里对话体验效果</li>
                <li>随时导出自己的调用记录</li>
              </ul>
            </div>
            <div class="card pt-login">
              <h3>输入秘钥</h3>
              <input
                v-model="loginInput"
                class="input code"
                placeholder="sk-…"
                :disabled="loginBusy"
                @keydown.enter="login"
              />
              <button class="btn btn-primary btn-block" :disabled="loginBusy || !loginInput.trim()" @click="login">
                <span v-if="loginBusy" class="spinner" />进入门户
              </button>
              <div v-if="loginErr" class="pt-err">{{ loginErr }}</div>
            </div>
          </div>
        </section>

        <footer class="pt-foot muted">{{ pub.service || '模型中转站' }} · 兼容 OpenAI 接口 · 未登录仅可浏览</footer>
      </div>
    </div>
    <!-- 已登录：侧边栏多页布局 -->
    <div v-else class="pt-shell">
      <aside class="pt-side" :class="{ editing: editMode }">
        <div class="pt-brand">
          <span class="pt-logo">H</span>
          <div class="pt-brand-txt">
            <strong>模型服务</strong>
            <span>{{ me.name }}</span>
          </div>
        </div>
        <nav class="pt-nav">
          <button class="pt-nav-item" :class="{ active: page === 'overview' }" @click="page = 'overview'">
            <span class="pt-nav-ico">◉</span>概览
          </button>
          <button class="pt-nav-item" :class="{ active: page === 'models' }" @click="page = 'models'">
            <span class="pt-nav-ico">▣</span>模型<em>{{ models.length }}</em>
          </button>
          <button class="pt-nav-item" :class="{ active: page === 'docs' }" @click="page = 'docs'">
            <span class="pt-nav-ico">❰❱</span>模型文档
          </button>
          <button class="pt-nav-item" :class="{ active: page === 'logs' }" @click="page = 'logs'">
            <span class="pt-nav-ico">≡</span>调用记录<em>{{ logs.length }}</em>
          </button>
          <button class="pt-nav-item" :class="{ active: page === 'play' }" @click="page = 'play'">
            <span class="pt-nav-ico">✦</span>快速体验
          </button>
        </nav>
        <div class="pt-side-foot">
          <button v-if="isAdmin" class="btn btn-sm btn-block" :class="editMode ? 'btn-primary' : 'btn-ghost'" @click="toggleEdit">
            {{ editMode ? '✓ 完成编辑' : '✏️ 编辑门户' }}
          </button>
          <button class="btn btn-ghost btn-sm btn-block" :disabled="loading" @click="refresh">
            <span v-if="loading" class="spinner" />刷新数据
          </button>
          <button class="btn btn-ghost btn-sm btn-block" @click="logout">退出登录</button>
        </div>
      </aside>

      <main class="pt-main-wrap">
        <Transition :name="animName">
          <div class="pt-page-anim" :key="page">
        <!-- 概览 -->
        <section v-if="page === 'overview'">
          <div class="pt-hero">
            <div>
              <h2>你好，{{ me.name }}</h2>
              <p>你的服务接入点：<code class="pt-code-inline">{{ baseURL }}</code>，认证方式 <code class="pt-code-inline">Authorization: Bearer &lt;秘钥&gt;</code></p>
            </div>
          </div>
          <!-- 公告：管理员在门户里可视化编辑（markdown） -->
          <div v-if="editMode || notice" class="card pt-notice">
            <h3 class="card-title">📢 公告</h3>
            <template v-if="editMode">
              <textarea v-model="noticeDraft" class="input pt-notice-input" rows="5" placeholder="支持 markdown，留空保存即清空公告"></textarea>
              <div class="pt-link-ops">
                <button class="btn btn-primary btn-sm" :disabled="savingPortal" @click="saveNotice">
                  {{ savingPortal ? '保存中…' : '保存公告' }}
                </button>
              </div>
            </template>
            <div v-else class="pt-md" v-html="renderMarkdown(notice)"></div>
          </div>
          <div class="pt-stats">
            <div class="card stat">
              <div class="label">今日调用</div>
              <div class="value">{{ me.today_calls }}</div>
              <div class="foot">
                <template v-if="me.daily_quota">每日上限 {{ me.daily_quota }} 次</template>
                <template v-else>不限次数</template>
              </div>
              <div v-if="me.daily_quota" class="progress"><i :style="{ width: pct(me.today_calls, me.daily_quota) + '%' }" /></div>
            </div>
            <div class="card stat">
              <div class="label">今日 Tokens</div>
              <div class="value">{{ me.today_tokens.toLocaleString() }}</div>
              <div class="foot">
                <template v-if="me.daily_tokens">每日上限 {{ me.daily_tokens.toLocaleString() }}</template>
                <template v-else>不限额度</template>
              </div>
              <div v-if="me.daily_tokens" class="progress"><i :style="{ width: pct(me.today_tokens, me.daily_tokens) + '%' }" /></div>
            </div>
            <div class="card stat">
              <div class="label">速率限制</div>
              <div class="value">{{ me.rpm || '∞' }}<span style="font-size: 13px"> 次/分</span></div>
              <div class="foot">最近使用：{{ fmtTime(me.last_used_at) }}</div>
            </div>
            <div class="card stat">
              <div class="label">秘钥状态</div>
              <div class="value" :style="{ color: me.expires_at ? 'var(--warn)' : 'var(--ok)' }">
                {{ me.expires_at ? '有期限' : '长期有效' }}
              </div>
              <div class="foot">{{ me.expires_at ? '到期：' + fmtTime(me.expires_at) : (me.remark || '祝使用愉快') }}</div>
            </div>
          </div>
          <div class="card">
            <h3 class="card-title">最近动态</h3>
            <div v-if="!logs.length" class="muted">还没有调用记录，去「快速体验」发第一条消息吧。</div>
            <table v-else class="table">
              <thead><tr><th>时间</th><th>模型</th><th>状态</th><th>耗时</th></tr></thead>
              <tbody>
                <tr v-for="l in logs.slice(0, 8)" :key="l.id">
                  <td class="mono">{{ fmtTime(l.created_at) }}</td>
                  <td class="mono">{{ l.model }}</td>
                  <td><span class="pt-status" :class="l.status ? 'fail' : 'ok'">{{ l.status ? '失败' : '成功' }}</span></td>
                  <td>{{ l.latency_ms }} ms</td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>

        <!-- 模型 -->
        <section v-if="page === 'models'">
          <div class="pt-page-head">
            <h2>可用模型</h2>
            <span class="muted">共 {{ models.length }} 个 · 点卡片查看自动生成的接入文档</span>
          </div>
          <div class="pt-model-grid">
            <button v-for="m in models" :key="m.name" class="card pt-model-card" @click="docModel = m.name; page = 'docs'">
              <div class="pt-model-top">
                <span class="mono">{{ m.name }}</span>
                <span class="pt-badge" :class="m.capability">{{ capBadge(m.capability) }}</span>
              </div>
              <div class="pt-model-meta">
                <span v-if="m.context">上下文 {{ (m.context / 1000).toFixed(0) }}K</span>
                <span>{{ m.upstreams }} 上游</span>
                <span v-for="x in (m.out_mod || []).slice(0, 2)" :key="x">{{ x }}</span>
              </div>
              <div class="pt-model-remark">{{ m.remark || '查看接入文档 →' }}</div>
            </button>
          </div>
        </section>

        <!-- 模型文档 -->
        <section v-if="page === 'docs'">
          <div class="pt-page-head">
            <h2>模型文档</h2>
            <span class="muted">按模型自动生成：接入点、请求示例与注意事项</span>
          </div>
          <div class="pt-docs">
            <div class="pt-doc-list card">
              <button
                v-for="m in models"
                :key="m.name"
                class="pt-doc-item"
                :class="{ active: docModel === m.name }"
                @click="docModel = m.name"
              >
                <span class="mono">{{ m.name }}</span>
                <span class="pt-badge" :class="m.capability">{{ capBadge(m.capability) }}</span>
              </button>
            </div>
            <div v-if="docMeta" class="card pt-doc-body">
              <div class="pt-doc-head">
                <h3 class="mono">{{ docMeta.name }}</h3>
                <span class="pt-badge" :class="docMeta.capability">{{ capBadge(docMeta.capability) }}</span>
                <span class="pt-doc-actions">
                  <button
                    v-if="!docMeta.capability || docMeta.capability === 'chat'"
                    class="pt-copy pt-act"
                    @click="tryItNow"
                  >⚡ 快速体验</button>
                  <button v-if="editMode" class="pt-copy pt-act" @click="startIntroEdit">✏️ 编辑介绍</button>
                  <button class="pt-copy pt-act" :disabled="introLoading === docMeta.name" @click="regenerateIntro">
                    {{ introLoading === docMeta.name ? '撰写中…' : '↻ 重写介绍' }}
                  </button>
                </span>
              </div>
              <!-- 介绍：管理员手改 / 主模型撰写的 markdown 优先；未生成/失败回退内置一句话 -->
              <template v-if="introEditing && editMode">
                <textarea v-model="introDraft" class="input pt-notice-input" rows="6" placeholder="支持 markdown"></textarea>
                <div class="pt-link-ops">
                  <button class="btn btn-primary btn-sm" :disabled="savingPortal || !introDraft.trim()" @click="saveIntro">保存介绍</button>
                  <button class="btn btn-ghost btn-sm" @click="introEditing = false">取消</button>
                </div>
              </template>
              <div v-else-if="introLoading === docModel && !introMap[docModel]" class="pt-intro-loading">
                <span class="pt-intro-spinner"></span> 主模型正在为该模型撰写介绍…
              </div>
              <div v-else-if="introMap[docModel]" class="pt-md" v-html="renderMarkdown(introMap[docModel])"></div>
              <p v-else class="pt-doc-intro">{{ docsFor(docMeta).intro }}</p>
              <div class="pt-doc-facts">
                <div><label>接入点</label><code class="mono">{{ docsFor(docMeta).endpoint }}</code></div>
                <div><label>Base URL</label><code class="mono">{{ baseURL }}</code></div>
                <div><label>认证</label><code class="mono">Authorization: Bearer sk-…</code></div>
                <div v-if="docMeta.context"><label>上下文窗口</label><code class="mono">{{ docMeta.context.toLocaleString() }} tokens</code></div>
                <div v-if="(docMeta.in_mod || []).length"><label>输入模态</label><code class="mono">{{ (docMeta.in_mod || []).join(' / ') }}</code></div>
                <div v-if="(docMeta.out_mod || []).length"><label>输出模态</label><code class="mono">{{ (docMeta.out_mod || []).join(' / ') }}</code></div>
                <div><label>上游链路</label><code class="mono">{{ docMeta.upstreams }} 个（自动主备/轮询）</code></div>
              </div>
              <div class="pt-doc-tabs">
                <button :class="{ active: docTab === 'openai' }" @click="docTab = 'openai'">OpenAI 通用接口</button>
                <button :class="{ active: docTab === 'advanced' }" @click="docTab = 'advanced'">进阶说明</button>
              </div>
              <template v-if="docTab === 'openai'">
                <div v-for="sn in docsFor(docMeta).snippets" :key="sn.title" class="pt-snippet">
                  <div class="pt-snippet-head">
                    <span>{{ sn.title }}</span>
                    <button class="pt-copy" @click="copyText(sn.code, $event)">复制</button>
                  </div>
                  <pre class="pt-snippet-code"><code>{{ sn.code }}</code></pre>
                </div>
                <ul class="pt-doc-notes">
                  <li v-for="n in docsFor(docMeta).notes" :key="n">{{ n }}</li>
                </ul>
              </template>
              <template v-else>
                <div v-for="sn in advancedSnippets" :key="sn.title" class="pt-snippet">
                  <div class="pt-snippet-head">
                    <span>{{ sn.title }}</span>
                    <button class="pt-copy" @click="copyText(sn.code, $event)">复制</button>
                  </div>
                  <pre class="pt-snippet-code"><code>{{ sn.code }}</code></pre>
                </div>
                <ul class="pt-doc-notes">
                  <li v-for="n in advancedNotes" :key="n">{{ n }}</li>
                </ul>
              </template>
            </div>
          </div>
        </section>

        <!-- 调用记录 -->
        <section v-if="page === 'logs'">
          <div class="pt-page-head">
            <h2>调用记录</h2>
            <span class="muted">成功 {{ logOk }} · 失败 {{ logFail }}（仅自己秘钥的调用，不含请求内容）</span>
          </div>
          <div class="card">
            <div v-if="!logs.length" class="muted">还没有调用记录。</div>
            <table v-else class="table">
              <thead><tr><th>时间</th><th>模型</th><th>状态</th><th>耗时</th><th>Tokens</th><th>错误</th></tr></thead>
              <tbody>
                <tr v-for="l in logs" :key="l.id">
                  <td class="mono">{{ fmtTime(l.created_at) }}</td>
                  <td class="mono">{{ l.model }}</td>
                  <td><span class="pt-status" :class="l.status ? 'fail' : 'ok'">{{ l.status ? '失败' : '成功' }}</span></td>
                  <td>{{ l.latency_ms }} ms</td>
                  <td>{{ (l.prompt_tokens + l.completion_tokens).toLocaleString() }}</td>
                  <td class="pt-err-cell">{{ l.error || '—' }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>

        <!-- 快速体验 -->
        <section v-if="page === 'play'" class="pt-play-sec">
          <div class="pt-page-head">
            <h2>快速体验</h2>
            <span class="muted">用你自己的秘钥直接对话，走完整计费与限速链路</span>
          </div>
          <div class="card pt-chatcard">
            <div class="pt-chat-select">
              <select v-model="chatModel" class="select" :disabled="chatBusy">
                <option v-for="m in chatCapable" :key="m.name" :value="m.name">{{ m.name }}</option>
              </select>
            </div>
            <div ref="chatEl" class="pt-chat">
              <div v-if="!chatMsgs.length" class="muted" style="margin: auto; text-align: center">
                输入第一条消息开始体验。
              </div>
              <div v-for="(m, i) in chatMsgs" :key="i" class="msg" :class="m.role">
                <span class="avatar">{{ m.role === 'user' ? '你' : 'AI' }}</span>
                <div class="bubble">
                  <div v-if="m.content" class="md" v-html="renderMarkdown(m.content)" />
                  <div v-else class="pt-typing"><span class="typing"><i /><i /><i /></span></div>
                </div>
              </div>
            </div>
            <div class="pt-chat-input">
              <textarea
                v-model="chatInput"
                class="input"
                rows="1"
                placeholder="输入消息…（Enter 发送）"
                :disabled="chatBusy"
                @keydown.enter.exact.prevent="sendChat"
              />
              <button class="btn btn-primary" :disabled="chatBusy || !chatInput.trim() || !chatModel" @click="sendChat">
                <span v-if="chatBusy" class="spinner" />发送
              </button>
            </div>
          </div>
        </section>
          </div>
        </Transition>
      </main>

      <!-- 手机底部标签栏（桌面档由 CSS 隐藏） -->
      <nav class="pt-tabbar">
        <button
          v-for="t in tabs"
          :key="t.id"
          class="pt-tab"
          :class="{ active: page === t.id }"
          @click="page = t.id"
        >
          <span class="pt-tab-ico">{{ t.ico }}</span>{{ t.label }}
        </button>
      </nav>
    </div>
  </div>
</template>

<style scoped>
/* ---------- 登录 ---------- */
.portal {
  min-height: 100vh;
  background: var(--bg);
  color: var(--text);
}
/* 秘钥登录卡片：主页里居中，别贴着左边 */
#pt-login-box .pt-login {
  margin: 0 auto;
}
/* ---------- 未登录主页 ----------
   样式全部引用全局设计令牌（--s* 间距 / --r* 圆角 / --e* 阴影 / --brand-grad），
   与面板其余页面同一套语言，不自造圆角与阴影。 */
.pt-home {
  min-height: 100vh;
  padding: 0 var(--s5) var(--s8);
  color: var(--text);
  background:
    radial-gradient(1100px 560px at 6% -10%, hsl(224 92% 58% / 0.22), transparent 68%),
    radial-gradient(820px 460px at 98% 0%, hsl(318 90% 60% / 0.16), transparent 68%),
    radial-gradient(900px 520px at 50% 106%, hsl(190 92% 54% / 0.14), transparent 68%),
    var(--bg);
  animation: pt-rise 0.4s cubic-bezier(0.32, 0.72, 0, 1) both;
}
.pt-wrap {
  max-width: var(--container);
  margin: 0 auto;
}

/* 顶栏 */
.pt-top {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--s4);
  height: 68px;
  border-bottom: 1px solid var(--border);
}
.pt-top-brand {
  display: flex;
  align-items: center;
  gap: var(--s3);
}
.pt-top-txt strong {
  display: block;
  font-size: 15px;
  line-height: 1.3;
}
.pt-top-txt .muted {
  font-size: 12px;
}
.pt-top .btn-primary {
  padding: 7px var(--s4);
  border-radius: var(--r-full);
  font-weight: 600;
}

/* Hero：左文案 / 右速通卡 */
.pt-home-hero {
  display: grid;
  grid-template-columns: minmax(0, 1.15fr) minmax(300px, 0.85fr);
  gap: var(--s8);
  align-items: center;
  padding: var(--s9) 0 var(--s8);
}
.pt-home-hero-copy {
  min-width: 0;
}
.pt-chip {
  display: inline-block;
  padding: 4px var(--s3);
  border-radius: var(--r-full);
  font-size: 12px;
  font-weight: 600;
  color: #fff;
  background: var(--brand-grad);
  box-shadow: var(--glow);
}
.pt-home-hero h1 {
  margin: var(--s4) 0 var(--s3);
  font-size: clamp(32px, 4.6vw, 50px);
  line-height: 1.14;
  letter-spacing: -0.8px;
  font-weight: 800;
}
.pt-home-hero h1 em {
  font-style: normal;
  background: var(--brand-grad);
  -webkit-background-clip: text;
  background-clip: text;
  color: transparent;
}
.pt-home-hero-sub {
  margin: 0;
  max-width: 52ch;
  font-size: 14.5px;
  line-height: 1.75;
  color: var(--text-dim);
}
.pt-home-hero-points {
  list-style: none;
  margin: var(--s6) 0 0;
  padding: 0;
  display: grid;
  gap: var(--s3);
}
.pt-home-hero-points li {
  display: flex;
  gap: var(--s3);
  align-items: flex-start;
}
.pt-tick {
  flex: none;
  width: 6px;
  height: 6px;
  margin-top: 7px;
  border-radius: 50%;
}
.pt-home-hero-points strong {
  display: block;
  font-size: 13.5px;
}
.pt-home-hero-points .muted {
  font-size: 12.5px;
  line-height: 1.6;
}
.pt-home-hero-cta {
  margin-top: var(--s6);
  display: flex;
  align-items: center;
  gap: var(--s4);
  flex-wrap: wrap;
}
.pt-home-hero-cta .btn-lg {
  padding: 11px var(--s6);
  font-size: 15px;
  border-radius: var(--r-full);
}
.pt-home-hero-cta .muted {
  font-size: 12.5px;
}

/* 右侧速通卡 */
.pt-quick {
  padding: var(--s5);
  border-radius: var(--r-lg);
  background: var(--bg-elev);
  border: 1px solid var(--border);
  box-shadow: var(--e2);
}
.pt-quick-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: var(--s4);
  font-size: 13px;
  font-weight: 600;
}
.pt-quick-badge {
  padding: 3px var(--s2);
  border-radius: var(--r-full);
  font-size: 11.5px;
  font-weight: 600;
  color: var(--brand-text);
  background: var(--accent-dim);
}
.pt-quick-steps {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
  gap: var(--s3);
}
.pt-quick-steps li {
  display: flex;
  align-items: center;
  gap: var(--s3);
  font-size: 13px;
}
.pt-quick-n {
  flex: none;
  width: 20px;
  height: 20px;
  border-radius: var(--r-xs);
  display: grid;
  place-items: center;
  font-size: 11.5px;
  font-weight: 700;
  color: #fff;
  background: var(--brand-grad);
}
.pt-quick-base {
  margin-top: var(--s4);
  padding: var(--s2) var(--s2) var(--s2) var(--s3);
  display: flex;
  align-items: center;
  gap: var(--s2);
  border-radius: var(--r-sm);
  background: var(--bg-elev-2);
  border: 1px solid var(--border);
}
.pt-quick-base .mono {
  flex: 1;
  min-width: 0;
  font-size: 12px;
  color: var(--brand-text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 区块节奏 */
.pt-block {
  padding: var(--s7) 0;
  border-top: 1px solid var(--border);
}
.pt-h2 {
  margin: 0 0 var(--s5);
  font-size: 18px;
  letter-spacing: 0.2px;
}

/* 能力 Bento：对话占大格 */
.pt-bento {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: var(--s4);
}
.pt-tile {
  position: relative;
  overflow: hidden;
  grid-column: span 1;
  min-height: 104px;
  padding: var(--s4);
  display: flex;
  align-items: center;
  gap: var(--s3);
  border-radius: var(--r);
  background: linear-gradient(150deg, hsl(var(--hue) 92% 58% / var(--tint-bg)), transparent 70%), var(--bg-elev);
  border: 1px solid hsl(var(--hue) 92% 60% / var(--tint-border));
  transition: transform 0.18s cubic-bezier(0.32, 0.72, 0, 1), box-shadow 0.18s ease, border-color 0.18s ease;
}
.pt-tile.big {
  grid-column: span 2;
  min-height: 136px;
}
.pt-tile:hover {
  transform: translateY(-2px);
  border-color: hsl(var(--hue) 92% 62% / 0.7);
  box-shadow: var(--e2);
}
.pt-tile-icon {
  flex: none;
  width: 40px;
  height: 40px;
  border-radius: var(--r-sm);
  display: grid;
  place-items: center;
  color: #fff;
  background: linear-gradient(140deg, hsl(var(--hue) 92% 60%), hsl(calc(var(--hue) + 26) 90% 56%));
  box-shadow: var(--e1);
}
.pt-tile.big .pt-tile-icon {
  width: 48px;
  height: 48px;
  border-radius: var(--r);
}
.pt-tile-icon svg {
  width: 20px;
  height: 20px;
}
.pt-tile.big .pt-tile-icon svg {
  width: 24px;
  height: 24px;
}
.pt-tile-body {
  min-width: 0;
}
.pt-tile-body strong {
  display: block;
  font-size: 14.5px;
}
.pt-tile-body .muted {
  font-size: 12.5px;
  color: hsl(var(--hue) 70% 72%);
}
.pt-tile-tag {
  position: absolute;
  top: var(--s3);
  right: var(--s3);
  padding: 2px var(--s2);
  border-radius: var(--r-full);
  font-size: 11px;
  font-weight: 600;
  color: #fff;
  background: hsl(var(--hue) 92% 58% / 0.9);
}

/* 接入区：左步骤 + 右代码 */
.pt-split {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1.05fr);
  gap: var(--s6);
  align-items: start;
}
.pt-steps {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
  gap: var(--s4);
}
.pt-steps li {
  display: flex;
  gap: var(--s3);
  padding: var(--s4);
  border-radius: var(--r);
  background: linear-gradient(150deg, hsl(var(--hue) 92% 58% / calc(var(--tint-bg) * 0.8)), transparent 72%), var(--bg-elev);
  border: 1px solid hsl(var(--hue) 92% 58% / calc(var(--tint-border) * 0.8));
}
.pt-step-n {
  flex: none;
  width: 24px;
  height: 24px;
  border-radius: var(--r-xs);
  display: grid;
  place-items: center;
  font-size: 12px;
  font-weight: 700;
  color: #fff;
  background: hsl(var(--hue) 90% 58%);
}
.pt-steps strong {
  display: block;
  font-size: 14px;
}
.pt-steps .muted {
  font-size: 12.5px;
  line-height: 1.6;
}

/* 代码卡 */
.pt-code {
  border-radius: var(--r);
  border: 1px solid var(--border);
  background: var(--bg-elev);
  overflow: hidden;
  box-shadow: var(--e1);
}
.pt-code-head {
  display: flex;
  align-items: center;
  gap: var(--s3);
  padding: var(--s2) var(--s3);
  border-bottom: 1px solid var(--border);
  background: var(--bg-elev-2);
  font-size: 12px;
  color: var(--text-mute);
}
.pt-code-dots {
  display: inline-flex;
  gap: 5px;
}
.pt-code-dots i {
  width: 9px;
  height: 9px;
  border-radius: 50%;
  background: var(--border-strong);
}
.pt-code-dots i:nth-child(1) { background: #e0645c; }
.pt-code-dots i:nth-child(2) { background: #d9a545; }
.pt-code-dots i:nth-child(3) { background: #4cc38a; }
.pt-code pre {
  margin: 0;
  padding: var(--s4) var(--s5);
  font-size: 12.5px;
  line-height: 1.8;
  /* 窄屏必须允许横向滚动代码，否则长行会把整页撑出横向滚动条 */
  overflow-x: auto;
  max-width: 100%;
}
.pt-code .c {
  color: #c792ea;
  font-weight: 600;
}
.pt-code .s {
  color: #7ee787;
}

/* 公告 */
.pt-home-notice {
  padding: var(--s4) var(--s5);
  border-radius: var(--r);
  background: var(--bg-elev);
  border: 1px solid var(--border);
  font-size: 13.5px;
  line-height: 1.75;
  color: var(--text-dim);
}

/* 开始使用 */
.pt-split.start {
  grid-template-columns: minmax(0, 1fr) minmax(300px, 380px);
}
.pt-start-copy p {
  margin: 0 0 var(--s3);
  font-size: 13.5px;
}
.pt-start-copy ul {
  margin: 0;
  padding-left: 1.1em;
  display: grid;
  gap: var(--s2);
  font-size: 13px;
  line-height: 1.7;
  color: var(--text-dim);
}
.pt-login {
  padding: var(--s5);
  border-radius: var(--r);
}
.pt-login h3 {
  margin: 0 0 var(--s3);
  font-size: 15px;
}
.pt-login .input {
  margin-bottom: var(--s3);
}
.pt-login .btn-primary {
  border-radius: var(--r-sm);
  padding: 9px var(--s4);
}
.pt-err {
  margin-top: var(--s2);
  font-size: 12.5px;
  color: var(--danger);
}

.pt-foot {
  padding: var(--s6) 0 var(--s4);
  border-top: 1px solid var(--border);
  text-align: center;
  font-size: 12px;
}

/* 窄屏：全部单列，速通卡移到文案下方 */
@media (max-width: 900px) {
  .pt-home-hero,
  .pt-split,
  .pt-split.start {
    grid-template-columns: 1fr;
    gap: var(--s6);
  }
  .pt-bento {
    grid-template-columns: repeat(2, 1fr);
  }
  .pt-tile.big {
    grid-column: span 2;
  }
  /* 栅格列在窄屏是 1fr min-content 的最小化陷阱，显式 minmax(0,1fr) 才能真正收窄 */
  .pt-split > * {
    min-width: 0;
  }
}
@media (max-width: 640px) {
  .pt-home {
    padding: 0 var(--s4) var(--s7);
  }
  .pt-top {
    height: 60px;
  }
  .pt-home-hero {
    padding: var(--s7) 0 var(--s6);
  }
  .pt-home-hero-cta .btn-lg {
    width: 100%;
  }
  .pt-block {
    padding: var(--s6) 0;
  }
}
.pt-logo {
  width: 28px;
  height: 28px;
  border-radius: 8px;
  display: grid;
  place-items: center;
  font-weight: 700;
  color: #fff;
  background: var(--brand-grad);
  flex: none;
}
.pt-logo-lg {
  width: 48px;
  height: 48px;
  border-radius: 14px;
  font-size: 23px;
  margin: 0 auto 14px;
}

/* ---------- 壳 ---------- */
.pt-shell {
  display: grid;
  grid-template-columns: 208px minmax(0, 1fr);
  min-height: 100vh;
}
.pt-side {
  border-right: 1px solid var(--border);
  background: var(--bg-elev);
  padding: 14px 10px;
  display: flex;
  flex-direction: column;
  gap: 14px;
  position: sticky;
  top: 0;
  height: 100vh;
  box-sizing: border-box;
  /* 内容超高时侧栏内部滚动，foot 始终可达 */
  overflow-y: auto;
}
.pt-brand {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 2px 6px;
}
.pt-brand-txt {
  display: flex;
  flex-direction: column;
  line-height: 1.3;
  min-width: 0;
}
.pt-brand-txt strong {
  font-size: 13.5px;
}
.pt-brand-txt span {
  font-size: 11px;
  color: var(--text-mute);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.pt-nav {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
/* 侧边栏分组标题 */
.pt-nav-group {
  font-size: 10.5px;
  letter-spacing: 0.12em;
  color: var(--text-mute);
  padding: 12px 12px 4px;
}
/* 侧边栏条目：文本截断 */
.pt-nav-item {
  display: flex;
  align-items: center;
  gap: 9px;
  padding: 9px 12px;
  border: none;
  background: transparent;
  color: var(--text-dim);
  font-size: 13px;
  border-radius: var(--radius-sm);
  cursor: pointer;
  text-align: left;
  transition: 0.12s;
  width: 100%;
}
.pt-nav-item:hover {
  background: var(--bg-hover);
  color: var(--text);
}
.pt-nav-item.active {
  background: var(--accent-dim);
  color: var(--accent-text);
  font-weight: 600;
}
.pt-nav-ico {
  font-size: 12px;
  width: 16px;
  text-align: center;
  flex: none;
}
.pt-nav-item em {
  margin-left: auto;
  font-style: normal;
  font-size: 11px;
  color: var(--text-mute);
  background: var(--bg-elev-2);
  border-radius: 999px;
  padding: 0 7px;
}
.pt-side-foot {
  margin-top: auto;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.pt-main-wrap {
  padding: 20px 24px 40px;
  min-width: 0;
}

/* ---------- 概览 ---------- */
.pt-hero {
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 20px 22px;
  margin-bottom: 14px;
  background:
    radial-gradient(600px 200px at 0% 0%, var(--accent-dim), transparent),
    linear-gradient(135deg, var(--bg-elev), var(--bg-elev-2));
}
.pt-hero h2 {
  margin: 0 0 6px;
  font-size: 19px;
}
.pt-hero p {
  margin: 0;
  font-size: 12.5px;
  color: var(--text-dim);
}
.pt-code-inline {
  font-family: var(--mono);
  font-size: 11.5px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 4px;
  padding: 1px 6px;
}
.pt-stats {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 12px;
  margin-bottom: 14px;
}
.pt-stats .progress {
  margin-top: 8px;
}
.pt-page-head {
  display: flex;
  align-items: baseline;
  gap: 10px;
  margin-bottom: 12px;
  flex-wrap: wrap;
}
.pt-page-head h2 {
  margin: 0;
  font-size: 17px;
}
.pt-page-head .muted {
  font-size: 12px;
}
.pt-status {
  font-size: 11.5px;
  padding: 1px 8px;
  border-radius: 999px;
  white-space: nowrap;
}
.pt-status.ok {
  color: #3fb950;
  background: rgba(46, 160, 67, 0.14);
}
.pt-status.fail {
  color: #f85149;
  background: rgba(218, 54, 51, 0.14);
}

/* ---------- 模型卡 ---------- */
.pt-model-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(250px, 1fr));
  gap: 12px;
}
.pt-model-card {
  text-align: left;
  cursor: pointer;
  transition: 0.15s;
  padding: 14px 15px;
}
.pt-model-card:hover {
  border-color: var(--accent);
  transform: translateY(-2px);
  box-shadow: var(--shadow);
}
.pt-model-top {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  font-size: 13px;
  font-weight: 600;
}
.pt-badge {
  font-size: 10.5px;
  padding: 1px 8px;
  border-radius: 999px;
  background: var(--bg-elev-2);
  color: var(--text-dim);
  flex: none;
}
.pt-badge.chat {
  background: var(--accent-dim);
  color: var(--accent-text);
}
.pt-badge.image {
  background: rgba(146, 90, 235, 0.15);
  color: #a78bfa;
}
.pt-badge.video {
  background: rgba(18, 181, 164, 0.15);
  color: var(--accent-2);
}
.pt-model-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 6px 10px;
  margin-top: 8px;
  font-size: 11px;
  color: var(--text-mute);
}
.pt-model-remark {
  margin-top: 8px;
  font-size: 11.5px;
  color: var(--text-dim);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* ---------- 文档 ---------- */
.pt-docs {
  display: grid;
  grid-template-columns: 230px minmax(0, 1fr);
  gap: 14px;
  align-items: start;
}
.pt-doc-list {
  padding: 6px;
  display: flex;
  flex-direction: column;
  gap: 2px;
  max-height: 72vh;
  overflow-y: auto;
  position: sticky;
  top: 14px;
}
.pt-doc-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 8px 10px;
  border: none;
  background: transparent;
  color: var(--text-dim);
  font-size: 12px;
  border-radius: var(--radius-sm);
  cursor: pointer;
  text-align: left;
  width: 100%;
}
.pt-doc-item:hover {
  background: var(--bg-hover);
  color: var(--text);
}
.pt-doc-item.active {
  background: var(--accent-dim);
  color: var(--accent-text);
}
.pt-doc-item .mono {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.pt-doc-body {
  padding: 18px 20px;
}
.pt-doc-head {
  display: flex;
  align-items: center;
  gap: 10px;
}
.pt-doc-head h3 {
  margin: 0;
  font-size: 16px;
}
.pt-doc-intro {
  color: var(--text-dim);
  font-size: 13px;
  margin: 8px 0 14px;
}
/* 头部操作按钮组 */
.pt-doc-actions {
  margin-left: auto;
  display: inline-flex;
  gap: 8px;
}
.pt-act {
  border: 1px solid var(--border);
  background: transparent;
  color: var(--text-dim);
  cursor: pointer;
}
.pt-act:hover {
  color: var(--text);
  border-color: var(--accent);
}
.pt-act:disabled {
  opacity: 0.55;
  cursor: default;
}
/* 介绍加载态 */
.pt-intro-loading {
  display: flex;
  align-items: center;
  gap: 8px;
  color: var(--text-dim);
  font-size: 13px;
  margin: 8px 0 14px;
}
.pt-intro-spinner {
  width: 13px;
  height: 13px;
  border: 2px solid var(--border);
  border-top-color: var(--accent);
  border-radius: 50%;
  animation: pt-spin 0.8s linear infinite;
}
@keyframes pt-spin {
  to {
    transform: rotate(360deg);
  }
}
/* 主模型撰写的 markdown 介绍 */
.pt-md {
  color: var(--text-dim);
  font-size: 13px;
  line-height: 1.75;
  margin: 8px 0 14px;
  padding: 10px 14px;
  border-left: 3px solid var(--accent);
  background: color-mix(in srgb, var(--accent) 6%, transparent);
  border-radius: 0 8px 8px 0;
}
.pt-md :deep(p) {
  margin: 0 0 6px;
}
.pt-md :deep(p:last-child) {
  margin-bottom: 0;
}
.pt-md :deep(strong) {
  color: var(--text);
}
.pt-md :deep(code) {
  font-family: var(--mono);
  font-size: 12px;
  background: var(--border);
  padding: 1px 5px;
  border-radius: 4px;
  color: var(--accent-text);
}
.pt-md :deep(ul) {
  margin: 6px 0;
  padding-left: 18px;
}
/* 文档页签 */
.pt-doc-tabs {
  display: flex;
  gap: 6px;
  border-bottom: 1px solid var(--border);
  margin: 4px 0 14px;
}
.pt-doc-tabs button {
  border: none;
  background: none;
  color: var(--text-dim);
  font-size: 13px;
  padding: 7px 12px;
  cursor: pointer;
  border-bottom: 2px solid transparent;
  margin-bottom: -1px;
}
.pt-doc-tabs button:hover {
  color: var(--text);
}
.pt-doc-tabs button.active {
  color: var(--accent-text);
  border-bottom-color: var(--accent-text);
}
.pt-doc-facts {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px 14px;
  margin-bottom: 16px;
}
.pt-doc-facts > div {
  display: flex;
  gap: 8px;
  align-items: baseline;
  min-width: 0;
}
.pt-doc-facts label {
  flex: none;
  width: 74px;
  font-size: 11.5px;
  color: var(--text-mute);
}
.pt-doc-facts code {
  font-family: var(--mono);
  font-size: 11.5px;
  background: var(--bg-elev-2);
  border: 1px solid var(--border);
  border-radius: 4px;
  padding: 2px 7px;
  overflow-wrap: anywhere;
}
.pt-snippet {
  margin-bottom: 14px;
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  overflow: hidden;
}
.pt-snippet-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 6px 10px;
  background: var(--bg-elev-2);
  font-size: 11.5px;
  color: var(--text-dim);
}
.pt-copy {
  border: 1px solid var(--border);
  background: var(--bg-elev);
  color: var(--text-dim);
  font-size: 10.5px;
  border-radius: 4px;
  padding: 1px 8px;
  cursor: pointer;
}
.pt-copy:hover {
  color: var(--accent-text);
  border-color: var(--accent);
}
.pt-snippet-code {
  margin: 0;
  background: #14161a;
  padding: 11px 13px;
  overflow-x: auto;
}
.pt-snippet-code code {
  font-family: var(--mono);
  font-size: 11.5px;
  line-height: 1.6;
  color: #cfe3e2;
  white-space: pre;
}
.pt-doc-notes {
  margin: 0;
  padding-left: 18px;
  color: var(--text-dim);
  font-size: 12.5px;
  line-height: 1.8;
}

/* ---------- 体验 ---------- */
.pt-play-sec {
  height: calc(100vh - 40px);
  display: flex;
  flex-direction: column;
}
.pt-play-sec .pt-page-head {
  flex: none;
}
.pt-chatcard {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  padding: 0;
  overflow: hidden;
}
.pt-chat-select {
  padding: 10px 14px;
  border-bottom: 1px solid var(--border);
}
.pt-chat {
  flex: 1;
  overflow-y: auto;
  padding: 12px 14px;
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.pt-chat .msg {
  display: flex;
  gap: 8px;
  max-width: 100%;
}
.pt-chat .msg.user {
  flex-direction: row-reverse;
}
.pt-chat .avatar {
  width: 24px;
  height: 24px;
  border-radius: 50%;
  flex: none;
  display: grid;
  place-items: center;
  font-size: 11px;
  font-weight: 600;
  background: var(--bg-elev-2);
  color: var(--text-dim);
}
.pt-chat .msg.assistant .avatar {
  background: var(--accent-dim);
  color: var(--accent-text);
}
.pt-chat .bubble {
  background: var(--bg-elev-2);
  border: 1px solid var(--border);
  border-radius: 10px;
  padding: 8px 11px;
  font-size: 13px;
  max-width: 85%;
  overflow-wrap: break-word;
}
.pt-chat .msg.user .bubble {
  background: var(--accent-dim);
  border-color: var(--accent);
}
.pt-chat .md :deep(p) {
  margin: 0 0 5px;
}
.pt-chat .md :deep(p:last-child) {
  margin-bottom: 0;
}
.pt-chat .md :deep(pre.md-pre) {
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 7px 9px;
  overflow-x: auto;
  margin: 5px 0;
}
.pt-chat .md :deep(code) {
  font-family: var(--mono);
  font-size: 11.5px;
}
.pt-chat .md :deep(.md-table) {
  border-collapse: collapse;
  font-size: 12px;
  margin: 5px 0;
}
.pt-chat .md :deep(.md-table th),
.pt-chat .md :deep(.md-table td) {
  border: 1px solid var(--border);
  padding: 3px 7px;
}
.pt-typing {
  min-height: 18px;
}
.pt-chat-input {
  display: flex;
  gap: 8px;
  padding: 10px 14px 14px;
  border-top: 1px solid var(--border);
  align-items: flex-end;
}
.pt-chat-input textarea {
  flex: 1;
  resize: none;
  min-height: 38px;
  max-height: 120px;
}
.pt-err-cell {
  color: var(--danger);
  font-size: 11.5px;
  max-width: 220px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* ---------- 页面切换动画：transform+opacity 走合成器，快速连点可续接 ---------- */
.pt-main-wrap {
  position: relative;
}
.pt-page-anim {
  min-width: 0;
}
.pt-fwd-enter-active,
.pt-fwd-leave-active,
.pt-back-enter-active,
.pt-back-leave-active {
  transition:
    transform 0.3s cubic-bezier(0.32, 0.72, 0, 1),
    opacity 0.3s cubic-bezier(0.32, 0.72, 0, 1);
}
/* 离场页绝对定位叠底，进出同帧进行，切换不跳变 */
.pt-fwd-leave-active,
.pt-back-leave-active {
  position: absolute;
  top: 0;
  left: 0;
  width: 100%;
  pointer-events: none;
}
.pt-fwd-enter-from {
  transform: translateX(36px);
  opacity: 0;
}
.pt-fwd-leave-to {
  transform: translateX(-24px);
  opacity: 0;
}
.pt-back-enter-from {
  transform: translateX(-36px);
  opacity: 0;
}
.pt-back-leave-to {
  transform: translateX(24px);
  opacity: 0;
}
/* 进场卡片错落浮现 */
.pt-fwd-enter-active > section > *,
.pt-back-enter-active > section > *,
.pt-fwd-enter-active > section > .card,
.pt-back-enter-active > section > .card {
  animation: pt-rise 0.36s cubic-bezier(0.32, 0.72, 0, 1) both;
}
.pt-fwd-enter-active > section > :nth-child(2),
.pt-back-enter-active > section > :nth-child(2) {
  animation-delay: 0.04s;
}
.pt-fwd-enter-active > section > :nth-child(3),
.pt-back-enter-active > section > :nth-child(3) {
  animation-delay: 0.08s;
}
.pt-fwd-enter-active > section > :nth-child(4),
.pt-back-enter-active > section > :nth-child(4) {
  animation-delay: 0.12s;
}
@keyframes pt-rise {
  from {
    opacity: 0;
    transform: translateY(12px);
  }
}

/* ---------- 按压反馈：软体感缩放 ---------- */
.pt-nav-item:active,
.pt-model-card:active,
.pt-tab:active,
.pt-doc-item:active,
.pt-side-foot .btn:active {
  transform: scale(0.96);
  transition-duration: 0.08s;
}

/* ---------- 手机底部标签栏（桌面隐藏） ---------- */
.pt-tabbar {
  display: none;
}

@media (prefers-reduced-motion: reduce) {
  .pt-fwd-enter-active,
  .pt-fwd-leave-active,
  .pt-back-enter-active,
  .pt-back-leave-active,
  .pt-fwd-enter-active > section > *,
  .pt-back-enter-active > section > * {
    transition: none;
    animation: none;
  }
}

@media (max-width: 900px) {
  .pt-shell {
    grid-template-columns: minmax(0, 1fr);
  }
  .pt-side {
    position: static;
    height: auto;
    flex-direction: row;
    align-items: center;
    overflow-x: auto;
  }
  .pt-nav {
    flex-direction: row;
  }
  .pt-nav-item em {
    display: none;
  }
  .pt-side-foot {
    margin-top: 0;
    margin-left: auto;
    flex-direction: row;
  }
  /* 统计卡 2 列比 1 列更适合中屏 */
  .pt-stats {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .pt-doc-facts {
    grid-template-columns: minmax(0, 1fr);
  }
  /* 文档页：列表+正文改单列，模型列表变横滑条 */
  .pt-docs {
    grid-template-columns: minmax(0, 1fr);
  }
  .pt-doc-list {
    flex-direction: row;
    overflow-x: auto;
    overflow-y: hidden;
    max-height: none;
  }
  .pt-doc-item {
    flex: none;
  }
  /* 长表/代码块横向滑动，不撑破卡片（Chrome 对 display:block 表格不认 width:100%，要 max-width 压住） */
  table.table {
    display: block;
    width: auto;
    max-width: 100%;
    overflow-x: auto;
  }
}

/* ---------- 手机档（≤600px）：底部标签栏 + 软件级导航 ---------- */
@media (max-width: 600px) {
  .pt-main-wrap {
    padding: 14px 12px calc(84px + env(safe-area-inset-bottom));
  }
  /* 顶栏轻量化：品牌 + 外链 chips，主导航下沉到标签栏 */
  .pt-side {
    flex-wrap: wrap;
    gap: 8px;
    padding: 10px 12px;
    overflow: visible;
  }
  /* 侧栏导航隐藏；编辑模式时唤回（外链增删改在里面） */
  .pt-nav {
    display: none;
  }
  .pt-side.editing .pt-nav {
    display: flex;
    order: 3;
    flex-basis: 100%;
    overflow-x: auto;
    gap: 4px;
    padding-bottom: 2px;
  }
  .pt-nav-item {
    white-space: nowrap;
    padding: 8px 10px;
  }
  .pt-nav-group {
    display: none;
  }
  /* 外链 chips 横滑一行 */
  .pt-side-foot {
    order: 4;
    flex-basis: 100%;
    margin-left: 0;
    justify-content: flex-end;
    gap: 4px;
  }
  .pt-side-foot .btn {
    padding: 6px 9px;
    font-size: 12px;
  }
  /* 底部标签栏 */
  .pt-tabbar {
    position: fixed;
    left: 0;
    right: 0;
    bottom: 0;
    z-index: 100;
    display: flex;
    gap: 2px;
    padding: 6px 6px calc(6px + env(safe-area-inset-bottom));
    background: color-mix(in srgb, var(--bg-elev) 88%, transparent);
    backdrop-filter: blur(14px);
    -webkit-backdrop-filter: blur(14px);
    border-top: 1px solid var(--border);
  }
  .pt-tab {
    flex: 1;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 2px;
    padding: 5px 0 4px;
    border: none;
    background: transparent;
    color: var(--text-mute);
    font-size: 10.5px;
    border-radius: 10px;
    cursor: pointer;
    transition: color 0.15s, transform 0.15s;
  }
  .pt-tab:active {
    transform: scale(0.92);
  }
  .pt-tab.active {
    color: var(--accent-text);
    font-weight: 600;
  }
  .pt-tab-ico {
    font-size: 16px;
    line-height: 1.2;
    transition: transform 0.25s cubic-bezier(0.34, 1.56, 0.64, 1);
  }
  .pt-tab.active .pt-tab-ico {
    transform: translateY(-1px) scale(1.14);
  }
  .pt-hero {
    padding: 15px 14px;
  }
  .pt-hero h2 {
    font-size: 17px;
  }
  /* 长接入点 URL 可断行，不横向撑破 */
  .pt-code-inline {
    overflow-wrap: anywhere;
  }
  .pt-page-head {
    flex-wrap: wrap;
    gap: 4px;
  }
  .pt-doc-head {
    flex-wrap: wrap;
  }
  .pt-doc-actions {
    margin-left: 0;
    flex-wrap: wrap;
  }
  /* 编辑态：外链输入与公告输入 16px 防 iOS 聚焦缩放 */
  .pt-notice-input {
    font-size: 16px;
  }
  .pt-link-ops {
    flex-wrap: wrap;
  }
}

/* ---------- 管理员可视化编辑 ---------- */
.pt-toast {
  position: fixed;
  top: 16px;
  left: 50%;
  transform: translateX(-50%);
  z-index: 200;
  padding: 8px 18px;
  border-radius: 999px;
  border: 1px solid var(--border);
  background: var(--bg-elev, var(--bg-soft, #fff));
  color: var(--text);
  font-size: 13px;
  box-shadow: var(--shadow, 0 8px 24px rgba(0, 0, 0, 0.12));
  animation: pt-toast-in 0.18s ease-out;
}
@keyframes pt-toast-in {
  from {
    opacity: 0;
    transform: translate(-50%, -6px);
  }
}
.pt-notice {
  margin-bottom: 14px;
}
.pt-notice-input {
  width: 100%;
  resize: vertical;
  font-family: inherit;
}
.pt-link-ops {
  display: flex;
  gap: 8px;
  margin-top: 8px;
}
.pt-nav .pt-link-edit .input {
  font-size: 13px;
}
</style>
