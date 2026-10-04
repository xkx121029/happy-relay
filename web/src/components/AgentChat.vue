<script setup lang="ts">
import { computed, nextTick, onMounted, onUpdated, ref } from 'vue'
import { api } from '../api'
import { renderMarkdown } from '../utils'

// ---------- 站内 Agent 聊天核心（悬浮球与独立 Agent 页共用） ----------
// 主模型以与用户相同的管理权限跑工具循环：查配置、建模型、改设置、删秘钥……
// 会话存 localStorage（两处入口共享同一份对话）；回答支持轻量 markdown；
// 删除类操作由「同意执行」确认条驱动；支持中途停止。

interface AgentStep {
  action: string
  ok: boolean
  summary?: string
  error?: string
  detail?: string // 完整原始结果（后端未截断），可展开查看
}
// 交互引导卡：后端 reply 附带，前端渲染成秘钥输入框/选项卡
interface AgentAsk {
  kind: 'secret' | 'choice'
  label?: string
  options?: string[]
  multi?: boolean
  placeholder?: string
}
interface AgentMsg {
  role: 'user' | 'assistant'
  content: string
  steps?: AgentStep[]
  systemModel?: string
  latencyMS?: number
  ask?: AgentAsk
  askDone?: boolean // 引导卡已被用户响应，之后只读展示
}

const busy = ref(false)
const input = ref('')
const msgs = ref<AgentMsg[]>([])
const bodyEl = ref<HTMLElement | null>(null)
const rootEl = ref<HTMLElement | null>(null)
const taEl = ref<HTMLTextAreaElement | null>(null)
let abortCtrl: AbortController | null = null

// ---------- 引导卡交互状态（key=消息下标） ----------
const askInputs = ref<Record<number, string>>({})
const askSel = ref<Record<number, string[]>>({})

// 只有最后一条助手消息的引导卡可交互；历史卡只读展示流程足迹
const lastAskIdx = computed(() => {
  for (let i = msgs.value.length - 1; i >= 0; i--) {
    if (msgs.value[i].role === 'assistant') return i
  }
  return -1
})

function toggleChoice(i: number, opt: string) {
  const cur = askSel.value[i] || []
  askSel.value[i] = cur.includes(opt) ? cur.filter((x) => x !== opt) : [...cur, opt]
}

function submitSecret(i: number) {
  const v = (askInputs.value[i] || '').trim()
  if (!v || busy.value) return
  msgs.value[i].askDone = true
  runTurn('秘钥：' + v)
}

function submitChoice(i: number, opt?: string) {
  const m = msgs.value[i]
  if (!m.ask || busy.value) return
  const sel = opt ? [opt] : (askSel.value[i] || [])
  if (!sel.length) return
  m.askDone = true
  runTurn(m.ask.multi && !opt ? '我选择的模型：' + sel.join('、') : '我选择：' + sel[0])
}

// 工具步骤展开态：key = `${消息下标}-${步骤下标}`
const stepExp = ref<Record<string, boolean>>({})
function stepExpanded(key: string): boolean {
  return !!stepExp.value[key]
}
function toggleStep(mi: number, si: number) {
  const k = mi + '-' + si
  stepExp.value[k] = !stepExp.value[k]
}

const STORAGE_KEY = 'agent.chat.v1'

function saveChat() {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(msgs.value.slice(-40)))
  } catch {}
}
function restoreChat() {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (raw) {
      const arr = JSON.parse(raw)
      if (Array.isArray(arr) && arr.length) msgs.value = arr
    }
  } catch {}
}
function clearChat() {
  stop()
  msgs.value = []
  try {
    localStorage.removeItem(STORAGE_KEY)
  } catch {}
}

onMounted(restoreChat)

// ---------- 轻量 markdown：与调试台共用 utils.renderMarkdown（转义在前，防 XSS） ----------
// 给回复里的代码块注入「复制」按钮（v-html 内容无法直接绑事件，故 DOM 后处理）。
function decorateCodeBlocks() {
  const root = rootEl.value
  if (!root) return
  root.querySelectorAll<HTMLElement>('.md-pre:not([data-copy])').forEach((pre) => {
    pre.setAttribute('data-copy', '1')
    const btn = document.createElement('button')
    btn.className = 'md-copy'
    btn.type = 'button'
    btn.textContent = '复制'
    btn.addEventListener('click', (e) => {
      e.stopPropagation()
      const code = pre.querySelector('code')?.textContent || ''
      const done = () => {
        btn.textContent = '已复制'
        window.setTimeout(() => (btn.textContent = '复制'), 1200)
      }
      const fail = () => {
        btn.textContent = '复制失败'
        window.setTimeout(() => (btn.textContent = '复制'), 1200)
      }
      if (navigator.clipboard?.writeText) {
        navigator.clipboard.writeText(code).then(done).catch(fail)
      } else {
        const ta = document.createElement('textarea')
        ta.value = code
        document.body.appendChild(ta)
        ta.select()
        try {
          document.execCommand('copy')
          done()
        } catch {
          fail()
        }
        document.body.removeChild(ta)
      }
    })
    pre.appendChild(btn)
  })
}
// 每次渲染（含流式增量）后装饰尚未处理的代码块；幂等（已注入的带 data-copy 跳过）。
onUpdated(decorateCodeBlocks)

async function scrollBottom() {
  await nextTick()
  if (bodyEl.value) bodyEl.value.scrollTop = bodyEl.value.scrollHeight
}

// ---------- 工具名 → 中文标签 ----------
const actionLabel: Record<string, string> = {
  list_models: '查看模型',
  save_model: '保存模型',
  delete_model: '删除模型',
  list_providers: '查看渠道',
  save_provider: '保存渠道',
  list_catalog: '查模型库',
  get_settings: '读取设置',
  save_settings: '修改设置',
  list_keys: '查看秘钥',
  save_key: '保存秘钥',
  delete_key: '删除秘钥',
  list_skills: '查看技能',
  save_skill: '保存技能',
  delete_skill: '删除技能',
  auto_aggregate: '多模态聚合',
  list_logs: '查看日志',
  upstream_stats: '上游画像',
  clear_logs: '清空日志',
  list_tasks: '查看任务',
  delete_task: '删除任务',
  test_provider: '探测渠道',
  probe_upstream: '探测上游',
  failover: '自动恢复',
  reset_provider: '复位熔断',
  list_health: '巡检记录',
  self_check: '配置自省',
  test_notify: '测试通知',
  feishu_status: '飞书状态',
  export_backup: '导出备份',
}

function stepName(s: AgentStep): string {
  return actionLabel[s.action] || s.action
}

// ---------- 工具结果富 UI：列表/设置类工具直接渲染成卡片，而不是裸 JSON ----------
interface ToolCell {
  t: string
  cls?: string // ok=启用绿 off=停用灰 cap=类型徽标
}
interface ToolView {
  cols: string[]
  rows: ToolCell[][]
  more: number // 未展示的剩余条数
}
const TOOL_MAX_ROWS = 12
const toolViewCache = new WeakMap<object, ToolView | null>()

// tt 取步骤对应富视图（WeakMap 缓存，避免流式重渲时反复 JSON.parse）。
function tt(s: AgentStep): ToolView | null {
  if (toolViewCache.has(s)) return toolViewCache.get(s) as ToolView | null
  const v = buildToolView(s)
  toolViewCache.set(s, v)
  return v
}

function jarr(s: AgentStep): any[] | null {
  if (!s.detail) return null
  try {
    const d = JSON.parse(s.detail)
    return Array.isArray(d) ? d : null
  } catch {
    return null
  }
}
function c(t: any, cls?: string): ToolCell {
  return { t: String(t ?? ''), cls }
}
const capLabel = (v: any) =>
  ({ chat: '对话', image: '绘图', video: '视频', embedding: '向量', speech: '语音', transcribe: '转写' }[String(v)] || String(v || '—'))
const statusCell = (on: any) => c(on ? '启用' : '停用', on ? 'ok' : 'off')
const trunc = (v: any, n: number) => {
  const t = String(v ?? '')
  return t.length > n ? t.slice(0, n) + '…' : t
}
const SENSITIVE = /(secret|pass|token|key|cipher)/i

function buildToolView(s: AgentStep): ToolView | null {
  const arr = jarr(s)
  let cols: string[]
  let rows: ToolCell[][]
  switch (s.action) {
    case 'list_models':
      if (!arr) return null
      cols = ['模型', '类型', '上游', '状态']
      rows = arr.map((m) => [
        c(m.name, 'name'),
        c(capLabel(m.capability), 'cap'),
        c((m.inputs?.length ?? 0) + ' 个'),
        statusCell(m.enabled),
      ])
      break
    case 'list_providers':
      if (!arr) return null
      cols = ['渠道', '平台', '密钥数', '状态']
      rows = arr.map((p) => [c(p.name, 'name'), c(p.kind, 'cap'), c(p.api_key_count ?? 0), statusCell(p.enabled)])
      break
    case 'list_keys':
      if (!arr) return null
      cols = ['秘钥', '可用模型', '状态']
      rows = arr.map((k) => [
        c(k.name, 'name'),
        c(Array.isArray(k.models) && k.models.length ? trunc(k.models.join('、'), 36) : '全部模型'),
        statusCell(k.enabled),
      ])
      break
    case 'list_skills':
      if (!arr) return null
      cols = ['技能', '类型', '说明', '状态']
      rows = arr.map((k) => [c(k.name, 'name'), c(capLabel(k.capability), 'cap'), c(trunc(k.description, 40)), statusCell(k.enabled)])
      break
    case 'get_settings':
    case 'feishu_status': {
      if (!s.detail) return null
      let obj: Record<string, string> | null = null
      try {
        obj = JSON.parse(s.detail)
      } catch {}
      if (!obj || typeof obj !== 'object' || Array.isArray(obj)) return null
      cols = ['配置项', '值']
      rows = Object.keys(obj)
        .sort()
        .map((k) => [
          c(k, 'name'),
          SENSITIVE.test(k) && obj[k] ? c('••••••（已配置）', 'off') : c(trunc(obj[k], 60) || '—'),
        ])
      break
    }
    default:
      return null
  }
  const cap = s.action === 'get_settings' ? 20 : TOOL_MAX_ROWS
  const more = Math.max(0, rows.length - cap)
  return { cols, rows: rows.slice(0, cap), more }
}

// summaryShort：富卡片已展示结构化结果时，把 summary 里的裸 JSON 尾巴剪掉。
function summaryShort(s: AgentStep): string {
  if (!tt(s) || !s.summary) return s.summary
  const idx = s.summary.search(/：\s*[\[{]/)
  if (idx >= 0) return s.summary.slice(0, idx)
  if (/^[\[{]/.test(s.summary.trim())) return '读取成功，结果如下'
  return s.summary
}

// 该消息是否正处于「流式执行中」：是最后一条 + busy + 尚无最终回复
function isLive(i: number): boolean {
  return busy.value && i === msgs.value.length - 1 && !msgs.value[i].content
}
// 执行中状态条文案
function liveStatus(m: AgentMsg): string {
  const n = m.steps?.length ?? 0
  return n > 0 ? `已完成 ${n} 步 · 正在思考下一步…` : '思考 / 执行中…'
}
// 该轮是否需要用户确认删除类操作（后端拒绝执行 + 要求 confirm，或 AI 主动询问是否同意）
function pendingConfirm(m: AgentMsg): boolean {
  if (m.steps?.some((s) => !s.ok && /confirm|征得同意|不可逆/.test(s.error || ''))) return true
  return /您是否|是否同意|征得.*同意|确认执行|不可逆操作|请确认/.test(m.content)
}

// ---------- 发送 ----------
function autoGrow() {
  const ta = taEl.value
  if (!ta) return
  ta.style.height = 'auto'
  ta.style.height = Math.min(ta.scrollHeight, 160) + 'px'
}

const suggestions = [
  '我要接入一个新的模型服务：https://api.siliconflow.cn/v1',
  '把巡检间隔改成 120 秒',
  '列出所有启用的模型',
  '最近有哪些调用失败了？',
  '把智谱的视觉模型整合成一个聚合模型',
]

// 欢迎屏能力卡：点击直接开跑
const WELCOME_CARDS = [
  { icon: '⚙', title: '改配置', desc: '巡检间隔 / 流式速率 / 主题', q: '把巡检间隔改成 120 秒' },
  { icon: '▣', title: '管模型', desc: '查列表 / 建聚合 / 上下架', q: '列出所有启用的模型' },
  { icon: '◔', title: '排故障', desc: '失败调用 / 熔断 / 自检解读', q: '最近有哪些调用失败了？' },
  { icon: '⌘', title: '管秘钥', desc: '分发 / 限速 / 过期管理', q: '看看现在有哪些秘钥' },
]

function stop() {
  if (abortCtrl) {
    abortCtrl.abort()
    abortCtrl = null
  }
}

function runTurn(text: string) {
  const t = text.trim()
  if (!t || busy.value) return
  msgs.value.push({ role: 'user', content: t })
  const history = msgs.value
    .filter((m) => m.content && !m.content.startsWith('⚠'))
    .map((m) => ({ role: m.role, content: m.content }))
  busy.value = true
  abortCtrl = new AbortController()
  // 先放一个空的助手占位气泡，流式事件会逐步往里塞步骤与最终回复。
  const ai = msgs.value.push({ role: 'assistant', content: '', steps: [] }) - 1
  scrollBottom()
  api.agentChatStream(history, {
    onStep: (step) => {
      msgs.value[ai].steps!.push(step)
      scrollBottom()
    },
    onReply: (r) => {
      msgs.value[ai].content = r.reply
      msgs.value[ai].systemModel = r.system_model
      msgs.value[ai].latencyMS = r.latency_ms
      msgs.value[ai].ask = r.ask
      scrollBottom()
    },
    onError: (msg) => {
      if (msgs.value[ai] && !msgs.value[ai].content) msgs.value[ai].content = '⚠ ' + msg
      else if (msgs.value[ai]) msgs.value[ai].content += '\n⚠ ' + msg
      scrollBottom()
    },
    onDone: () => {
      busy.value = false
      abortCtrl = null
      saveChat()
      scrollBottom()
    },
  }, abortCtrl.signal)
}

function send() {
  const t = input.value
  input.value = ''
  nextTick(autoGrow)
  runTurn(t)
}
function onSuggestion(s: string) {
  input.value = s
  nextTick(() => {
    autoGrow()
    taEl.value?.focus()
  })
}
function confirmExecute() {
  runTurn('我同意执行上述不可逆操作，请继续完成它（带 confirm:true）。')
}

const lastModel = computed(() => {
  for (let i = msgs.value.length - 1; i >= 0; i--) {
    if (msgs.value[i].systemModel) return msgs.value[i].systemModel
  }
  return ''
})

defineExpose({ clearChat, ask: (t: string) => runTurn(t) })
</script>

<template>
  <div ref="rootEl" class="agent-chat">
    <div ref="bodyEl" class="ac-body">
      <!-- 欢迎屏：能力卡片 -->
      <div v-if="!msgs.length" class="ac-welcome">
        <span class="ac-hero-logo">✦</span>
        <h3 class="ac-hero-title">指挥我干活</h3>
        <p class="ac-hero-sub">我以你的管理权限直接操作面板——查配置、建模型、改设置、删秘钥，工具执行过程全程可见。</p>
        <div class="ac-cards">
          <button v-for="c in WELCOME_CARDS" :key="c.title" class="ac-card" @click="runTurn(c.q)">
            <span class="ac-card-ico">{{ c.icon }}</span>
            <span class="ac-card-title">{{ c.title }}</span>
            <span class="ac-card-desc">{{ c.desc }}</span>
          </button>
        </div>
        <div class="ac-chips">
          <button v-for="s in suggestions" :key="s" class="ai-chip" @click="onSuggestion(s)">{{ s }}</button>
        </div>
      </div>

      <template v-for="(m, i) in msgs" :key="i">
        <div class="ai-msg" :class="m.role">
          <span class="ai-avatar" :class="m.role">{{ m.role === 'user' ? '你' : '✦' }}</span>
          <div class="ai-bubble" :class="m.role">
            <!-- 工具执行轨迹：时间线 -->
            <template v-if="m.steps?.length">
              <div class="ai-trace">
                <div
                  v-for="(s, j) in m.steps"
                  :key="j"
                  class="ai-step"
                  :class="{ fail: !s.ok, live: isLive(i) && j === (m.steps?.length ?? 0) - 1 }"
                >
                  <span class="ai-step-dot">
                    {{ s.ok ? '✓' : '✕' }}
                  </span>
                  <div class="ai-step-body">
                    <div class="ai-step-row">
                      <span class="ai-step-name">{{ stepName(s) }}</span>
                      <button v-if="s.detail" class="ai-step-toggle" type="button" @click="toggleStep(i, j)">
                        {{ stepExpanded(i + '-' + j) ? '收起' : '详情' }}
                      </button>
                    </div>
                    <span class="ai-step-txt">{{ s.error || summaryShort(s) }}</span>
                    <!-- 工具结果富 UI：列表/设置类渲染成卡片 -->
                    <div v-if="tt(s)" class="toolcard">
                      <table>
                        <thead>
                          <tr><th v-for="col in tt(s)!.cols" :key="col">{{ col }}</th></tr>
                        </thead>
                        <tbody>
                          <tr v-for="(r, ri) in tt(s)!.rows" :key="ri">
                            <td v-for="(cell, ci) in r" :key="ci" :class="cell.cls">{{ cell.t }}</td>
                          </tr>
                        </tbody>
                      </table>
                      <div v-if="tt(s)!.more > 0" class="toolcard-more">还有 {{ tt(s)!.more }} 条未展开，点「详情」看全部</div>
                    </div>
                    <pre v-if="s.detail && stepExpanded(i + '-' + j)" class="ai-step-detail">{{ s.detail }}</pre>
                  </div>
                </div>
              </div>
            </template>
            <div v-if="!m.content && !m.steps?.length" class="ai-thinking">
              <span class="typing"><i /><i /><i /></span>
              <span class="ai-busy-txt">思考 / 执行中…</span>
            </div>
            <div v-else-if="!m.content && isLive(i)" class="ai-thinking">
              <span class="typing"><i /><i /><i /></span>
              <span class="ai-busy-txt">{{ liveStatus(m) }}</span>
            </div>
            <div class="md" v-html="renderMarkdown(m.content)" v-else />
            <div class="ac-foot" v-if="m.latencyMS != null || m.systemModel">
              <span class="ac-model" v-if="m.systemModel">{{ m.systemModel }}</span>
              <span class="ac-lat" v-if="m.latencyMS != null">{{ m.latencyMS }} ms</span>
            </div>
            <div class="ai-confirm" v-if="m.role === 'assistant' && pendingConfirm(m)">
              <button class="btn btn-danger btn-sm" @click="confirmExecute">同意并执行</button>
              <span class="ai-confirm-tip">不可逆操作需二次确认</span>
            </div>
            <!-- 交互引导卡：秘钥输入 / 模型选项勾选（一体化接入流程） -->
            <div
              class="ai-ask"
              v-if="m.role === 'assistant' && m.ask"
              :class="{ done: m.askDone, stale: i !== lastAskIdx && !m.askDone }"
            >
              <div class="ai-ask-label" v-if="m.ask.label">{{ m.ask.label }}</div>
              <template v-if="m.ask.kind === 'secret'">
                <div v-if="!m.askDone && i === lastAskIdx" class="ai-ask-secret">
                  <input
                    class="input"
                    type="password"
                    autocomplete="off"
                    v-model="askInputs[i]"
                    :placeholder="m.ask.placeholder || 'sk-…'"
                    @keydown.enter.prevent="submitSecret(i)"
                  />
                  <button class="btn btn-primary btn-sm" :disabled="!askInputs[i]?.trim() || busy" @click="submitSecret(i)">提交</button>
                </div>
                <div v-else class="ai-ask-muted">秘钥已提交 ✓</div>
              </template>
              <template v-else-if="m.ask.kind === 'choice'">
                <div class="ai-ask-choices">
                  <button
                    v-for="o in m.ask.options || []"
                    :key="o"
                    class="ai-choice"
                    :class="{ sel: askSel[i]?.includes(o) }"
                    :disabled="m.askDone || (i !== lastAskIdx) || busy"
                    @click="m.ask!.multi ? toggleChoice(i, o) : submitChoice(i, o)"
                  >{{ o }}</button>
                </div>
                <div class="ai-ask-ops" v-if="m.ask.multi && !m.askDone && i === lastAskIdx">
                  <button class="btn btn-primary btn-sm" :disabled="!askSel[i]?.length || busy" @click="submitChoice(i)">
                    接入选中的 {{ askSel[i]?.length || 0 }} 个
                  </button>
                  <span class="ai-ask-tip">可多选，点模型名勾选</span>
                </div>
                <div v-if="m.askDone" class="ai-ask-muted">已提交选择 ✓</div>
              </template>
            </div>
          </div>
        </div>
      </template>
    </div>

    <!-- 输入区：悬浮卡 -->
    <div class="ac-input-wrap">
      <div class="ac-input">
        <textarea
          ref="taEl"
          v-model="input"
          class="ac-textarea"
          rows="1"
          placeholder="用自然语言指挥我干活…"
          @input="autoGrow"
          @keydown.enter.exact.prevent="send"
          @keydown.shift.enter.prevent="() => { input += '\n'; autoGrow() }"
        />
        <button v-if="busy" class="ac-round stop" title="停止" @click="stop">
          <span class="ac-stop-ico" />
        </button>
        <button v-else class="ac-round send" :disabled="!input.trim()" title="发送 (Enter)" @click="send">
          ↑
        </button>
      </div>
      <div class="ac-input-hint">
        <span>Enter 发送 · Shift+Enter 换行</span>
        <span v-if="busy" class="ac-hint-busy">执行中，可点 ■ 停止</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.agent-chat {
  display: flex;
  flex-direction: column;
  min-height: 0;
  height: 100%;
}
.ac-body {
  flex: 1;
  overflow-y: auto;
  padding: 20px 22px 12px;
  display: flex;
  flex-direction: column;
  gap: 18px;
}

/* ---------- 欢迎屏 ---------- */
.ac-welcome {
  margin: auto 0;
  max-width: 560px;
  align-self: center;
  width: 100%;
  text-align: center;
  padding: 24px 0;
}
.ac-hero-logo {
  width: 56px;
  height: 56px;
  margin: 0 auto 14px;
  display: grid;
  place-items: center;
  font-size: 26px;
  color: #fff;
  border-radius: 16px;
  background: linear-gradient(135deg, var(--accent), var(--accent-2));
  box-shadow: var(--glow);
}
.ac-hero-title {
  margin: 0 0 6px;
  font-size: 19px;
}
.ac-hero-sub {
  margin: 0 auto 20px;
  max-width: 420px;
  font-size: 12.5px;
  line-height: 1.7;
  color: var(--text-mute);
}
.ac-cards {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
  margin-bottom: 16px;
}
.ac-card {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 3px;
  padding: 13px 14px;
  text-align: left;
  background: var(--bg-hover);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  cursor: pointer;
  transition: 0.15s;
}
.ac-card:hover {
  border-color: var(--accent);
  background: var(--accent-dim);
  transform: translateY(-1px);
}
.ac-card-ico {
  font-size: 15px;
  color: var(--accent-text);
}
.ac-card-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--text);
}
.ac-card-desc {
  font-size: 11.5px;
  color: var(--text-mute);
}
.ac-chips {
  display: flex;
  flex-wrap: wrap;
  gap: 7px;
  justify-content: center;
}
.ai-chip {
  padding: 6px 12px;
  font-size: 12px;
  background: transparent;
  border: 1px solid var(--border);
  border-radius: 999px;
  color: var(--text-dim);
  cursor: pointer;
  transition: 0.12s;
}
.ai-chip:hover {
  border-color: var(--accent);
  color: var(--accent-text);
}

/* ---------- 消息 ---------- */
.ai-msg {
  display: flex;
  gap: 10px;
  max-width: 92%;
}
.ai-msg.user {
  align-self: flex-end;
  flex-direction: row-reverse;
}
/* AI 对话框横向占满整个聊天区：表格、代码块、时间线都用整行宽度 */
.ai-msg.assistant {
  align-self: stretch;
  max-width: 100%;
  width: 100%;
}
.ai-avatar {
  width: 30px;
  height: 30px;
  flex: none;
  border-radius: 9px;
  display: grid;
  place-items: center;
  font-size: 12px;
  margin-top: 2px;
}
.ai-avatar.user {
  background: var(--border);
  color: var(--text-dim);
  font-size: 11px;
}
.ai-avatar.assistant {
  color: #fff;
  background: linear-gradient(135deg, var(--accent), var(--accent-2));
  box-shadow: var(--glow);
}
.ai-bubble {
  min-width: 0;
  padding: 10px 14px;
  border-radius: 14px;
  font-size: 13.5px;
  line-height: 1.7;
}
.ai-bubble.user {
  background: var(--accent);
  color: #fff;
  border-bottom-right-radius: 5px;
}
.ai-bubble.user .md {
  color: #fff;
}
.ai-bubble.assistant {
  flex: 1;
  min-width: 0;
  width: 100%;
  box-sizing: border-box;
  background: var(--bg-hover);
  border: 1px solid var(--border);
  border-top-left-radius: 5px;
}
/* 用户气泡里的 markdown 配色压成白 */
.ai-bubble.user :deep(code) {
  background: rgba(255, 255, 255, 0.16);
  color: #fff;
}
.ai-bubble.user :deep(a) {
  color: #fff;
}

/* ---------- 工具轨迹：时间线 ---------- */
.ai-trace {
  position: relative;
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding-bottom: 2px;
  margin-bottom: 10px;
}
.ai-trace::before {
  content: '';
  position: absolute;
  left: 8px;
  top: 12px;
  bottom: 10px;
  width: 1px;
  background: var(--border);
}
.ai-step {
  position: relative;
  display: flex;
  gap: 10px;
  align-items: flex-start;
}
.ai-step-dot {
  width: 17px;
  height: 17px;
  flex: none;
  margin-top: 2px;
  border-radius: 50%;
  display: grid;
  place-items: center;
  font-size: 10px;
  z-index: 1;
  background: var(--bg-elev);
  border: 1px solid var(--ok);
  color: var(--ok);
}
.ai-step.fail .ai-step-dot {
  border-color: var(--danger);
  color: var(--danger);
}
.ai-step.live .ai-step-dot {
  border-color: var(--accent);
  color: var(--accent);
  animation: ac-pulse 1.1s ease-out infinite;
}
@keyframes ac-pulse {
  0% {
    box-shadow: 0 0 0 0 var(--accent-dim);
  }
  100% {
    box-shadow: 0 0 0 6px transparent;
  }
}
.ai-step-body {
  flex: 1;
  min-width: 0;
}
.ai-step-row {
  display: flex;
  align-items: baseline;
  gap: 8px;
}
.ai-step-name {
  font-size: 12.5px;
  font-weight: 600;
  color: var(--text);
}
.ai-step.fail .ai-step-name {
  color: var(--danger);
}
.ai-step-toggle {
  border: none;
  background: none;
  font-size: 11px;
  color: var(--accent-text);
  cursor: pointer;
  padding: 0;
}
.ai-step-txt {
  display: block;
  font-size: 12px;
  color: var(--text-mute);
  margin-top: 1px;
  word-break: break-all;
}
.ai-step-detail {
  margin: 6px 0 0;
  padding: 8px 10px;
  max-height: 260px;
  overflow: auto;
  font-family: var(--mono);
  font-size: 11px;
  line-height: 1.6;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 8px;
  white-space: pre-wrap;
  word-break: break-all;
}

/* 工具结果富卡 */
.toolcard {
  margin-top: 6px;
  border: 1px solid var(--border);
  border-radius: 8px;
  overflow: hidden;
  background: var(--bg);
}
.toolcard table {
  width: 100%;
  border-collapse: collapse;
  font-size: 11.5px;
}
.toolcard th {
  text-align: left;
  padding: 6px 10px;
  font-weight: 500;
  color: var(--text-mute);
  border-bottom: 1px solid var(--border);
  background: var(--bg-hover);
}
.toolcard td {
  padding: 5px 10px;
  border-bottom: 1px solid var(--border);
  color: var(--text-dim);
  max-width: 220px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.toolcard tr:last-child td {
  border-bottom: none;
}
.toolcard td.name {
  color: var(--text);
  font-family: var(--mono);
  font-size: 11px;
}
.toolcard td.ok {
  color: var(--ok);
}
.toolcard td.off {
  color: var(--text-mute);
}
.toolcard td.cap {
  color: var(--accent-text);
}
.toolcard-more {
  padding: 5px 10px;
  font-size: 11px;
  color: var(--text-mute);
}

/* 思考中 */
.ai-thinking {
  display: flex;
  align-items: center;
  gap: 8px;
  color: var(--text-mute);
  font-size: 12.5px;
  padding: 2px 0;
}
.typing {
  display: inline-flex;
  gap: 3px;
}
.typing i {
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: var(--accent);
  animation: ac-bounce 1s infinite ease-in-out;
}
.typing i:nth-child(2) {
  animation-delay: 0.15s;
}
.typing i:nth-child(3) {
  animation-delay: 0.3s;
}
@keyframes ac-bounce {
  0%,
  100% {
    opacity: 0.35;
    transform: translateY(0);
  }
  50% {
    opacity: 1;
    transform: translateY(-3px);
  }
}

/* 回复脚注 */
.ac-foot {
  display: flex;
  gap: 8px;
  margin-top: 8px;
  font-size: 10.5px;
  color: var(--text-mute);
}
.ac-model {
  background: var(--border);
  padding: 1px 7px;
  border-radius: 999px;
  font-family: var(--mono);
}
.ac-lat {
  font-family: var(--mono);
}

/* 确认条 */
.ai-confirm {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 10px;
  padding: 9px 12px;
  border: 1px solid var(--danger);
  border-radius: 10px;
  background: color-mix(in srgb, var(--danger) 8%, transparent);
}
/* ---------- 交互引导卡（一体化接入流程） ---------- */
.ai-ask {
  margin-top: 10px;
  padding: 11px 12px;
  border: 1px solid var(--accent);
  border-radius: 10px;
  background: color-mix(in srgb, var(--accent) 7%, transparent);
  animation: ac-ask-in 0.25s cubic-bezier(0.32, 0.72, 0, 1) both;
}
@keyframes ac-ask-in {
  from {
    opacity: 0;
    transform: translateY(6px);
  }
}
.ai-ask.done {
  border-style: dashed;
  border-color: var(--border);
  background: transparent;
  animation: none;
}
.ai-ask.stale {
  opacity: 0.55;
}
.ai-ask-label {
  font-size: 12.5px;
  font-weight: 600;
  color: var(--text);
  margin-bottom: 8px;
}
.ai-ask-secret {
  display: flex;
  gap: 8px;
}
.ai-ask-secret .input {
  flex: 1;
  min-width: 0;
}
.ai-ask-choices {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  max-height: 190px;
  overflow-y: auto;
}
.ai-choice {
  padding: 5px 11px;
  border: 1px solid var(--border);
  border-radius: 999px;
  background: var(--bg-elev);
  color: var(--text-dim);
  font-size: 12px;
  font-family: var(--mono);
  cursor: pointer;
  transition: transform 0.12s, border-color 0.12s, color 0.12s, background 0.12s;
}
.ai-choice:hover {
  border-color: var(--accent);
  color: var(--text);
}
.ai-choice:active {
  transform: scale(0.94);
}
.ai-choice.sel {
  border-color: var(--accent);
  color: var(--accent-text);
  background: var(--accent-dim);
  font-weight: 600;
}
.ai-ask-ops {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 9px;
}
.ai-ask-tip {
  font-size: 11.5px;
  color: var(--text-mute);
}
.ai-ask-muted {
  font-size: 12px;
  color: var(--text-mute);
}
.ai-confirm-tip {
  font-size: 11.5px;
  color: var(--text-mute);
}

/* ---------- 输入区 ---------- */
.ac-input-wrap {
  padding: 10px 14px 12px;
}
.ac-input {
  display: flex;
  align-items: flex-end;
  gap: 8px;
  padding: 8px 8px 8px 14px;
  border: 1px solid var(--border);
  border-radius: 16px;
  background: var(--bg-elev);
  transition: border-color 0.15s;
}
.ac-input:focus-within {
  border-color: var(--accent);
  box-shadow: 0 0 0 3px var(--accent-dim);
}
.ac-textarea {
  flex: 1;
  border: none;
  background: transparent;
  color: var(--text);
  font-size: 13.5px;
  line-height: 1.6;
  resize: none;
  outline: none;
  max-height: 160px;
  padding: 6px 0;
  font-family: inherit;
}
.ac-textarea::placeholder {
  color: var(--text-mute);
}
.ac-round {
  width: 34px;
  height: 34px;
  flex: none;
  border-radius: 50%;
  border: none;
  display: grid;
  place-items: center;
  font-size: 16px;
  cursor: pointer;
  transition: 0.15s;
}
.ac-round.send {
  background: linear-gradient(135deg, var(--accent), var(--accent-2));
  color: #fff;
  box-shadow: var(--glow);
}
.ac-round.send:disabled {
  opacity: 0.35;
  cursor: default;
  box-shadow: none;
}
.ac-round.stop {
  background: var(--danger-dim);
  border: 1px solid var(--danger);
}
.ac-stop-ico {
  width: 11px;
  height: 11px;
  border-radius: 2.5px;
  background: var(--danger);
}
.ac-input-hint {
  display: flex;
  justify-content: space-between;
  padding: 6px 6px 0;
  font-size: 10.5px;
  color: var(--text-mute);
}
.ac-hint-busy {
  color: var(--accent-text);
}

/* ---------- 窄屏适配 ---------- */
@media (max-width: 768px) {
  .ac-body {
    padding: 14px 12px 8px;
    gap: 14px;
  }
  /* 消息占满整行，把宽度让给正文 */
  .ai-msg {
    max-width: 100%;
  }
  .ai-msg.user {
    max-width: 100%;
  }
  .ai-bubble {
    padding: 9px 12px;
    font-size: 14px;
  }
  /* 工具表格超宽时横向滑动，不撑破气泡 */
  .toolcard {
    overflow-x: auto;
    -webkit-overflow-scrolling: touch;
  }
  .toolcard table {
    width: max-content;
    min-width: 100%;
  }
  /* 欢迎卡单列 */
  .ac-cards {
    grid-template-columns: minmax(0, 1fr);
  }
  .ac-hero-logo {
    width: 48px;
    height: 48px;
    font-size: 22px;
  }
  .ac-input-wrap {
    padding: 8px 10px 10px;
  }
  .ac-input {
    border-radius: 14px;
  }
  /* 触摸目标加大 */
  .ac-round {
    width: 40px;
    height: 40px;
  }
  .ai-avatar {
    display: none;
  }
  .ai-msg {
    gap: 0;
  }
}
</style>
