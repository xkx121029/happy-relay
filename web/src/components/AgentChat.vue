<script setup lang="ts">
import { computed, nextTick, onMounted, onUpdated, ref } from 'vue'
import { api } from '../api'

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
interface AgentMsg {
  role: 'user' | 'assistant'
  content: string
  steps?: AgentStep[]
  systemModel?: string
  latencyMS?: number
}

const busy = ref(false)
const input = ref('')
const msgs = ref<AgentMsg[]>([])
const bodyEl = ref<HTMLElement | null>(null)
const rootEl = ref<HTMLElement | null>(null)
const taEl = ref<HTMLTextAreaElement | null>(null)
let abortCtrl: AbortController | null = null

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

// 给回复里的代码块 `<pre class="md-pre">` 注入「复制」按钮（v-html 内容无法直接绑事件，故 DOM 后处理）。
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
    case 'get_settings': {
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
  const more = Math.max(0, rows.length - TOOL_MAX_ROWS)
  return { cols, rows: rows.slice(0, TOOL_MAX_ROWS), more }
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

// ---------- 轻量 markdown（先转义，防 XSS） ----------
function escapeHTML(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}
function renderMarkdown(src: string): string {
  const esc = escapeHTML(src)
  // 代码块 ```lang\n...```
  const blocks: string[] = []
  let s = esc.replace(/```(?:[a-zA-Z0-9]*)\n?([\s\S]*?)```/g, (_m, code) => {
    const i = blocks.push(`<pre class="md-pre"><code>${code.replace(/\n$/, '')}</code></pre>`) - 1
    return `<!--MD-BLOCK-${i}-->`
  })
  // 行内 `code`
  s = s.replace(/`([^`\n]+?)`/g, (_m, c) => `<code class="md-code">${c}</code>`)
  // 粗体 **x**
  s = s.replace(/\*\*([^*\n]+?)\*\*/g, '<strong>$1</strong>')
  // 段落与列表
  const lines = s.split(/\n/)
  let html = ''
  let inUl = false
  const closeUl = () => {
    if (inUl) {
      html += '</ul>'
      inUl = false
    }
  }
  for (const ln of lines) {
    const ph = ln.match(/^<!--MD-BLOCK-(\d+)-->$/)
    if (ph) {
      closeUl()
      html += blocks[+ph[1]]
      continue
    }
    const li = ln.match(/^\s*[-*]\s+(.*)$/)
    if (li) {
      if (!inUl) {
        html += '<ul class="md-ul">'
        inUl = true
      }
      html += `<li>${li[1]}</li>`
      continue
    }
    closeUl()
    if (ln.trim() === '') {
      html += '<div class="md-sp"></div>'
    } else {
      html += `<p>${ln}</p>`
    }
  }
  closeUl()
  // 还原代码块占位
  html = html.replace(/<!--MD-BLOCK-(\d+)-->/g, (_m, i) => blocks[+i])
  return html
}

// ---------- 发送 ----------
function autoGrow() {
  const ta = taEl.value
  if (!ta) return
  ta.style.height = 'auto'
  ta.style.height = Math.min(ta.scrollHeight, 160) + 'px'
}

const suggestions = [
  '把巡检间隔改成 120 秒',
  '列出所有启用的模型',
  '最近有哪些调用失败了？',
  '把智谱的视觉模型整合成一个聚合模型',
  '现在飞书整合是什么状态？',
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

defineExpose({ clearChat })
</script>

<template>
  <div ref="rootEl" class="agent-chat">
    <div ref="bodyEl" class="ac-body">
      <div v-if="!msgs.length" class="ac-hint">
        <p class="ac-hint-title">我能直接操作这个面板——查配置、建模型、改设置、删秘钥。试着对我说：</p>
        <button v-for="s in suggestions" :key="s" class="ai-chip" @click="onSuggestion(s)">{{ s }}</button>
      </div>

      <template v-for="(m, i) in msgs" :key="i">
        <div class="ai-msg" :class="m.role">
          <span class="ai-avatar" :class="m.role">{{ m.role === 'user' ? '你' : '✦' }}</span>
          <div class="ai-bubble" :class="m.role">
            <template v-if="m.steps?.length">
              <div class="ai-trace">
                <div v-for="(s, j) in m.steps" :key="j" class="ai-step" :class="{ fail: !s.ok, live: isLive(i) && j === (m.steps?.length ?? 0) - 1 }">
                  <span class="ai-step-ico">{{ s.ok ? '✓' : '✕' }}</span>
                  <div class="ai-step-body">
                    <div class="ai-step-row">
                      <span class="ai-step-name">{{ stepName(s) }}</span>
                      <button v-if="s.detail" class="ai-step-toggle" type="button" @click="toggleStep(i, j)">
                        {{ stepExpanded(i + '-' + j) ? '收起' : '详情' }}
                      </button>
                    </div>
                    <span class="ai-step-txt">{{ s.error || s.summary }}</span>
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
              <div class="ai-step-divider" />
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
          </div>
        </div>
      </template>
    </div>

    <div class="ac-input">
      <textarea
        ref="taEl"
        v-model="input"
        class="input ac-textarea"
        rows="1"
        placeholder="用自然语言指挥我干活…（Enter 发送 / Shift+Enter 换行）"
        :disabled="busy"
        @input="autoGrow"
        @keydown.enter.exact.prevent="send"
        @keydown.shift.enter.prevent="() => { input += '\n'; autoGrow() }"
      />
      <button v-if="busy" class="btn ac-stop" @click="stop">停止</button>
      <button v-else class="btn btn-primary ac-send" :disabled="!input.trim()" @click="send">发送</button>
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
  padding: 18px;
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.ac-hint {
  margin: auto 0;
  text-align: center;
  max-width: 520px;
  align-self: center;
  width: 100%;
}
.ac-hint-title {
  color: var(--text-mute);
  font-size: 12.5px;
  margin: 0 0 12px;
}
.ai-chip {
  display: block;
  width: 100%;
  margin: 7px 0;
  padding: 9px 12px;
  text-align: left;
  background: var(--bg-hover);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  color: var(--text-dim);
  font-size: 12.5px;
  cursor: pointer;
  transition: 0.12s;
}
.ai-chip:hover {
  border-color: var(--accent);
  color: var(--text);
  transform: translateX(2px);
}
.ai-msg {
  display: flex;
  gap: 8px;
  align-items: flex-start;
  max-width: 860px;
  width: 100%;
  margin: 0 auto;
}
.ai-msg.user {
  flex-direction: row-reverse;
}
.ai-avatar {
  width: 26px;
  height: 26px;
  border-radius: 50%;
  flex: none;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 12px;
  font-weight: 600;
}
.ai-avatar.assistant {
  background: var(--accent-dim);
  color: var(--accent-text);
  border: 1px solid var(--accent);
}
.ai-avatar.user {
  background: var(--bg-hover);
  color: var(--text-dim);
  border: 1px solid var(--border-strong);
}
.ai-bubble {
  max-width: 82%;
  padding: 9px 12px;
  border-radius: 12px;
  font-size: 13px;
  line-height: 1.62;
  word-break: break-word;
}
.ai-msg.user .ai-bubble {
  background: var(--accent-dim);
  border: 1px solid var(--accent);
  border-top-right-radius: 3px;
}
.ai-msg.assistant .ai-bubble {
  background: var(--bg-hover);
  border: 1px solid var(--border);
  border-top-left-radius: 3px;
}
/* 执行轨迹时间线 */
.ai-trace {
  display: flex;
  flex-direction: column;
  gap: 0;
  margin-bottom: 8px;
}
.ai-step {
  display: flex;
  gap: 7px;
  align-items: flex-start;
  padding: 3px 0;
  font-size: 11.5px;
  border-left: 2px solid var(--ok-dim);
  padding-left: 9px;
  position: relative;
}
.ai-step.fail {
  border-left-color: var(--danger-dim);
}
.ai-step-ico {
  flex: none;
  width: 15px;
  height: 15px;
  border-radius: 50%;
  font-size: 10px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #fff;
  background: var(--ok);
  margin-top: 1px;
}
.ai-step.fail .ai-step-ico {
  background: var(--danger);
}
/* 执行中实时状态：最新步骤呼吸高亮 */
.ai-step.live {
  background: var(--accent-dim);
  border-radius: 6px;
}
.ai-step.live .ai-step-ico {
  animation: ac-pulse 1.1s ease-out infinite;
}
@keyframes ac-pulse {
  0% {
    box-shadow: 0 0 0 0 rgba(56, 168, 158, 0.55);
  }
  70% {
    box-shadow: 0 0 0 7px rgba(56, 168, 158, 0);
  }
  100% {
    box-shadow: 0 0 0 0 rgba(56, 168, 158, 0);
  }
}
.ai-step-body {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
}
.ai-step-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.ai-step-name {
  color: var(--text);
  font-weight: 600;
}
.ai-step-toggle {
  flex: none;
  background: transparent;
  border: 1px solid var(--border);
  color: var(--accent-text);
  border-radius: var(--radius-sm);
  padding: 1px 8px;
  font-size: 10.5px;
  cursor: pointer;
  line-height: 1.5;
}
.ai-step-toggle:hover {
  border-color: var(--accent);
}
.ai-step-detail {
  margin: 5px 0 0;
  background: #14161a;
  border: 1px solid var(--border);
  border-radius: 7px;
  padding: 8px 10px;
  font-family: var(--mono);
  font-size: 11px;
  line-height: 1.5;
  color: #cfe3e2;
  white-space: pre-wrap;
  word-break: break-word;
  max-height: 240px;
  overflow: auto;
}
.ai-step-txt {
  color: var(--text-dim);
  font-size: 11px;
  word-break: break-word;
}
/* 工具结果富 UI 卡片：列表/设置类工具的表格化呈现 */
.toolcard {
  margin-top: 6px;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: var(--bg-elev);
  overflow: hidden;
}
.toolcard table {
  width: 100%;
  border-collapse: collapse;
  font-size: 11.5px;
}
.toolcard th {
  text-align: left;
  padding: 6px 9px;
  font-weight: 600;
  color: var(--text-mute);
  font-size: 10.5px;
  letter-spacing: 0.03em;
  border-bottom: 1px solid var(--border);
  background: var(--bg-elev-2);
  white-space: nowrap;
}
.toolcard td {
  padding: 5px 9px;
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
  font-family: var(--mono);
  font-size: 11px;
  color: var(--text);
  font-weight: 500;
}
.toolcard td.cap {
  color: var(--accent-text);
  background: var(--accent-dim);
  border-radius: 4px;
  padding: 1px 6px;
  display: inline-block;
  font-size: 10.5px;
  margin: 1px 0;
}
.toolcard td.ok {
  color: #3fb950;
  font-weight: 600;
}
.toolcard td.off {
  color: var(--text-mute);
}
.toolcard-more {
  padding: 5px 9px;
  font-size: 10.5px;
  color: var(--text-mute);
  background: var(--bg-elev-2);
  border-top: 1px solid var(--border);
}
.ai-step-divider {
  border-top: 1px dashed var(--border);
  margin: 6px 0 8px;
}
/* 轻量 markdown */
.md :deep(p) {
  margin: 0 0 6px;
}
.md :deep(p:last-child) {
  margin-bottom: 0;
}
.md :deep(.md-sp) {
  height: 6px;
}
.md :deep(strong) {
  color: var(--text);
  font-weight: 700;
}
.md :deep(.md-code) {
  font-family: var(--mono);
  font-size: 12px;
  background: var(--border);
  padding: 1px 5px;
  border-radius: 4px;
  color: var(--accent-text);
}
.md :deep(.md-pre) {
  position: relative;
  background: #14161a;
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 9px 11px;
  overflow-x: auto;
  margin: 6px 0;
}
.md :deep(.md-pre code) {
  font-family: var(--mono);
  font-size: 11.5px;
  color: #cfe3e2;
  white-space: pre;
}
.md :deep(.md-copy) {
  position: absolute;
  top: 6px;
  right: 6px;
  font-size: 10.5px;
  font-family: var(--sans);
  line-height: 1.4;
  padding: 2px 8px;
  border-radius: 5px;
  background: var(--bg-elev-2);
  color: var(--text-dim);
  border: 1px solid var(--border);
  cursor: pointer;
  opacity: 0.55;
  transition: opacity 0.12s;
}
.md :deep(.md-pre:hover .md-copy),
.md :deep(.md-copy:active) {
  opacity: 1;
}
.md :deep(.md-ul) {
  margin: 4px 0;
  padding-left: 18px;
}
.md :deep(.md-ul li) {
  margin: 2px 0;
}
.ac-foot {
  margin-top: 6px;
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  align-items: baseline;
}
.ac-model {
  font-size: 10px;
  color: var(--text-mute);
}
.ac-lat {
  font-size: 10px;
  color: var(--text-mute);
}
.ai-confirm {
  margin-top: 8px;
  display: flex;
  align-items: center;
  gap: 8px;
}
.ai-confirm-tip {
  font-size: 11px;
  color: var(--text-mute);
}
.ac-input {
  display: flex;
  gap: 8px;
  padding: 12px 18px 16px;
  border-top: 1px solid var(--border);
  align-items: flex-end;
  max-width: 896px;
  width: 100%;
  margin: 0 auto;
}
.ac-textarea {
  flex: 1;
  resize: none;
  min-height: 40px;
  max-height: 160px;
  line-height: 1.5;
  font-family: var(--sans);
}
.ac-send,
.ac-stop {
  align-self: flex-end;
  display: inline-flex;
  align-items: center;
  gap: 5px;
}
.ac-stop {
  border: 1px solid var(--border-strong);
  color: var(--text-dim);
}
.ac-stop:hover {
  border-color: var(--danger);
  color: var(--danger);
}
/* 输入指示动画 */
.typing {
  display: inline-flex;
  gap: 3px;
  margin-right: 6px;
}
.typing i {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--accent-text);
  animation: ac-bounce 1s infinite ease-in-out;
}
.typing i:nth-child(2) {
  animation-delay: 0.15s;
}
.typing i:nth-child(3) {
  animation-delay: 0.3s;
}
.ai-busy-txt {
  color: var(--text-dim);
  font-size: 12.5px;
}
@keyframes ac-bounce {
  0%,
  60%,
  100% {
    transform: translateY(0);
    opacity: 0.5;
  }
  30% {
    transform: translateY(-5px);
    opacity: 1;
  }
}
</style>
