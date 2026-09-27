<script setup lang="ts">
import { computed, nextTick, onMounted, onUpdated, ref } from 'vue'
import { api } from '../api'

// ---------- 站内 AI 智能体（全局悬浮窗） ----------
// 主模型以与用户相同的管理权限跑工具循环：查配置、建模型、改设置、删秘钥……
// 会话存 localStorage；面板可拖拽；回答支持轻量 markdown；删除类操作由「同意执行」确认条驱动。

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

const open = ref(false)
const busy = ref(false)
const input = ref('')
const msgs = ref<AgentMsg[]>([])
const panelEl = ref<HTMLElement | null>(null)
const rootEl = ref<HTMLElement | null>(null)
const taEl = ref<HTMLTextAreaElement | null>(null)
const pos = ref<{ x: number; y: number } | null>(null) // 拖拽后的左上角坐标（视口）
const dragging = ref(false)

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
const POS_KEY = 'agent.panel.pos.v1'

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
    const p = localStorage.getItem(POS_KEY)
    if (p) {
      const v = JSON.parse(p)
      if (v && typeof v.x === 'number') pos.value = v
    }
  } catch {}
}
function clearChat() {
  msgs.value = []
  try {
    localStorage.removeItem(STORAGE_KEY)
  } catch {}
}
function resetPos() {
  pos.value = null
  try {
    localStorage.removeItem(POS_KEY)
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
  if (panelEl.value) panelEl.value.scrollTop = panelEl.value.scrollHeight
}

// ---------- 拖拽（面板头部） ----------
// pos 语义 = 球的左上角（视口坐标）。拖头部时按「指针相对球的抓取偏移」移动球，面板随之。
let headDrag: { dx: number; dy: number } | null = null

function onDragStart(e: PointerEvent) {
  const b = ballPos()
  headDrag = { dx: e.clientX - b.x, dy: e.clientY - b.y }
  dragging.value = true
  // 捕获必须设在监听元素自身：捕获到父容器会把 move/up/click 全部重定向走，监听全收不到。
  ;(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId)
}
function onDragMove(e: PointerEvent) {
  if (!headDrag || !dragging.value) return
  const size = 48
  pos.value = {
    x: Math.max(8, Math.min(window.innerWidth - size - 8, e.clientX - headDrag.dx)),
    y: Math.max(8, Math.min(window.innerHeight - size - 8, e.clientY - headDrag.dy)),
  }
}
function onDragEnd(e: PointerEvent) {
  if (!dragging.value) return
  dragging.value = false
  headDrag = null
  try {
    ;(e.currentTarget as HTMLElement)?.releasePointerCapture(e.pointerId)
  } catch {}
  try {
    localStorage.setItem(POS_KEY, JSON.stringify(pos.value))
  } catch {}
}

// ballPos 计算球当前的视口左上角：拖拽过用 pos，否则按默认 right/bottom 推导。
function ballPos(): { x: number; y: number } {
  if (pos.value) return pos.value
  return { x: window.innerWidth - 22 - 48, y: window.innerHeight - 22 - 48 }
}

// panelStyle 面板独立定位（fixed）：右缘对齐球，底边贴球顶上方 10px；
// 球太靠上放不下面板时，改为从视口顶部 8px 向下展开，球保持不动。
const panelStyle = computed(() => {
  if (!open.value) return null
  const b = ballPos()
  const ph = Math.min(540, window.innerHeight - 96)
  const style: Record<string, string> = {}
  // 左缘 = 球右缘 - 面板宽，跟随球水平位置；越界收敛到视口内。
  style.left = Math.max(8, Math.min(b.x + 48 - 408, window.innerWidth - 416)) + 'px'
  if (b.y - 10 >= ph) {
    style.bottom = window.innerHeight - b.y + 10 + 'px'
  } else {
    // 球太靠上：从顶部 8px 展开，并把高度压到球顶之上，避免面板盖住球
    style.top = '8px'
    style.height = Math.max(240, b.y - 18) + 'px'
  }
  return style
})

// ---------- 悬浮球拖动 ----------
// 与面板头部拖拽同一套 pos 存储（pos = 球左上角）；位移超过阈值算拖动，松手后吞掉随之而来的 click。
let fabDrag: { startX: number; startY: number; origX: number; origY: number; active: boolean } | null = null
let fabJustDragged = false
const fabDragging = ref(false) // 给模板用：正在拖动时球放大+换手势

function onFabDown(e: PointerEvent) {
  if (open.value) return // 面板开着时球是收起按钮，不参与拖动
  const rect = rootEl.value?.getBoundingClientRect()
  if (!rect) return
  fabDrag = { startX: e.clientX, startY: e.clientY, origX: rect.left, origY: rect.top, active: false }
  // 捕获设在按钮自身（监听元素），否则后续 move/up/click 会被重定向到父容器而收不到。
  ;(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId)
}
function onFabMove(e: PointerEvent) {
  if (!fabDrag) return
  const dx = e.clientX - fabDrag.startX
  const dy = e.clientY - fabDrag.startY
  if (!fabDrag.active && Math.hypot(dx, dy) < 5) return // 5px 内视为点击
  fabDrag.active = true
  fabDragging.value = true
  const size = rootEl.value?.offsetWidth ?? 48
  const x = Math.max(8, Math.min(window.innerWidth - size - 8, fabDrag.origX + dx))
  const y = Math.max(8, Math.min(window.innerHeight - size - 8, fabDrag.origY + dy))
  pos.value = { x, y }
}
function onFabUp(e: PointerEvent) {
  if (!fabDrag) return
  const moved = fabDrag.active
  fabDrag = null
  fabDragging.value = false
  try {
    ;(e.currentTarget as HTMLElement)?.releasePointerCapture(e.pointerId)
  } catch {}
  if (moved) {
    fabJustDragged = true
    try {
      localStorage.setItem(POS_KEY, JSON.stringify(pos.value))
    } catch {}
    window.setTimeout(() => (fabJustDragged = false), 250) // 兜底：click 未触发时也能复位
  }
}
function onFabClick() {
  if (fabJustDragged) {
    fabJustDragged = false
    return
  }
  open.value = !open.value
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
  ta.style.height = Math.min(ta.scrollHeight, 140) + 'px'
}

const suggestions = [
  '把巡检间隔改成 120 秒',
  '列出所有启用的模型',
  '最近有哪些调用失败了？',
  '把智谱的视觉模型整合成一个聚合模型',
]

function runTurn(text: string) {
  const t = text.trim()
  if (!t || busy.value) return
  msgs.value.push({ role: 'user', content: t })
  const history = msgs.value
    .filter((m) => m.content && !m.content.startsWith('⚠'))
    .map((m) => ({ role: m.role, content: m.content }))
  busy.value = true
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
      if (!msgs.value[ai].content) msgs.value[ai].content = '⚠ ' + msg
      else msgs.value[ai].content += '\n⚠ ' + msg
      scrollBottom()
    },
    onDone: () => {
      busy.value = false
      saveChat()
      scrollBottom()
    },
  })
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
</script>

<template>
  <div ref="rootEl" class="ai-fab-wrap" :style="pos ? { left: pos.x + 'px', top: pos.y + 'px', right: 'auto', bottom: 'auto' } : null">
    <transition name="ai-panel">
      <div v-if="open" class="ai-panel card" :style="panelStyle">
        <div class="ai-head" @pointerdown="onDragStart" @pointermove="onDragMove" @pointerup="onDragEnd" @pointercancel="onDragEnd">
          <span class="ai-avatar ai-avatar-head">✦</span>
          <div class="ai-title">
            <strong>站内 AI 助手</strong>
            <span class="ai-model" v-if="lastModel">主模型 · {{ lastModel }}</span>
          </div>
          <div class="row">
            <span v-if="busy" class="ai-live-badge"><i class="ai-live-dot" />执行中</span>
            <button class="msg-act" title="还原位置" @click.stop="resetPos">复位</button>
            <button class="msg-act" title="清空对话" @click.stop="clearChat">清空</button>
            <button class="msg-act" title="收起" @click.stop="open = false">收起</button>
          </div>
        </div>

        <div ref="panelEl" class="ai-body">
          <div v-if="!msgs.length" class="ai-hint">
            <p class="ai-hint-title">我能直接操作这个面板，试着对我说：</p>
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
                <div class="ai-foot" v-if="m.latencyMS != null">
                  <span class="ai-lat">{{ m.latencyMS }} ms</span>
                </div>
                <div class="ai-confirm" v-if="m.role === 'assistant' && pendingConfirm(m)">
                  <button class="btn btn-danger btn-sm" @click="confirmExecute">同意并执行</button>
                  <span class="ai-confirm-tip">不可逆操作需二次确认</span>
                </div>
              </div>
            </div>
          </template>
        </div>

        <div class="ai-input">
          <textarea
            ref="taEl"
            v-model="input"
            class="input ai-textarea"
            rows="1"
            placeholder="用自然语言指挥我干活…（Enter 发送 / Shift+Enter 换行）"
            :disabled="busy"
            @input="autoGrow"
            @keydown.enter.exact.prevent="send"
            @keydown.shift.enter.prevent="() => { input += '\n'; autoGrow() }"
          />
          <button class="btn btn-primary btn-send" :disabled="busy || !input.trim()" @click="send">
            <span v-if="busy" class="spinner" />发送
          </button>
        </div>
      </div>
    </transition>

    <button
      class="ai-fab"
      :class="{ dragging: fabDragging }"
      title="站内 AI 助手（可拖动）"
      @pointerdown="onFabDown"
      @pointermove="onFabMove"
      @pointerup="onFabUp"
      @pointercancel="onFabUp"
      @click="onFabClick"
    >
      <span v-if="open">✕</span>
      <span v-else>✦</span>
      <span v-if="!open && msgs.length" class="ai-fab-badge" />
    </button>
  </div>
</template>

<style scoped>
.ai-fab-wrap {
  position: fixed;
  right: 22px;
  bottom: 22px;
  z-index: 300;
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 10px;
}
.ai-fab {
  width: 48px;
  height: 48px;
  border-radius: 50%;
  border: 1px solid var(--border-strong);
  background: linear-gradient(135deg, var(--accent), var(--accent-2));
  color: #fff;
  font-size: 19px;
  cursor: grab;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.4), var(--glow);
  transition: transform 0.15s, box-shadow 0.15s;
  position: relative;
  touch-action: none; /* 拖动期间不让浏览器接管滚动 */
  user-select: none;
}
.ai-fab:hover {
  transform: scale(1.08);
}
.ai-fab.dragging {
  cursor: grabbing;
  transform: scale(1.12);
  box-shadow: 0 12px 32px rgba(0, 0, 0, 0.5), var(--glow);
  transition: box-shadow 0.15s;
}
.ai-fab-badge {
  position: absolute;
  top: 3px;
  right: 3px;
  width: 9px;
  height: 9px;
  border-radius: 50%;
  background: var(--ok);
  border: 2px solid var(--bg);
}
.ai-panel {
  position: fixed; /* 独立于球定位：右缘对齐球，展开在球的上方，球自身不被顶动 */
  width: 408px;
  max-width: calc(100vw - 28px);
  height: 540px;
  max-height: calc(100vh - 96px);
  display: flex;
  flex-direction: column;
  padding: 0;
  overflow: hidden;
}
.ai-head {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 9px 12px;
  border-bottom: 1px solid var(--border);
  cursor: grab;
  user-select: none;
  touch-action: none; /* 头部拖拽时禁用浏览器触摸滚动 */
}
.ai-head:active {
  cursor: grabbing;
}
.ai-title {
  display: flex;
  flex-direction: column;
  line-height: 1.25;
  min-width: 0;
}
.ai-title strong {
  font-size: 13.5px;
}
.ai-model {
  font-size: 10.5px;
  color: var(--text-mute);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.ai-head .row {
  margin-left: auto;
  display: flex;
  gap: 4px;
}
.msg-act {
  background: transparent;
  border: 1px solid var(--border);
  color: var(--text-dim);
  border-radius: var(--radius-sm);
  padding: 3px 8px;
  font-size: 11.5px;
  cursor: pointer;
}
.msg-act:hover {
  border-color: var(--accent);
  color: var(--accent-text);
}
.ai-body {
  flex: 1;
  overflow-y: auto;
  padding: 14px;
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.ai-hint {
  margin: auto 0;
  text-align: center;
}
.ai-hint-title {
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
.ai-avatar-head {
  width: 28px;
  height: 28px;
  background: linear-gradient(135deg, var(--accent), var(--accent-2));
  color: #fff;
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
  animation: ai-pulse 1.1s ease-out infinite;
}
@keyframes ai-pulse {
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
.ai-live-badge {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-size: 11px;
  color: var(--accent-text);
  background: var(--accent-dim);
  border: 1px solid var(--accent);
  border-radius: 999px;
  padding: 2px 9px;
  white-space: nowrap;
}
.ai-live-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--accent);
  animation: ai-blink 1s ease-in-out infinite;
}
@keyframes ai-blink {
  0%,
  100% {
    opacity: 1;
  }
  50% {
    opacity: 0.25;
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
.ai-foot {
  margin-top: 6px;
  display: flex;
  justify-content: flex-end;
}
.ai-lat {
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
.btn-send {
  align-self: flex-end;
  display: inline-flex;
  align-items: center;
  gap: 5px;
}
.spinner {
  width: 12px;
  height: 12px;
  border: 2px solid rgba(255, 255, 255, 0.4);
  border-top-color: #fff;
  border-radius: 50%;
  animation: ai-spin 0.6s linear infinite;
}
@keyframes ai-spin {
  to {
    transform: rotate(360deg);
  }
}
.ai-input {
  display: flex;
  gap: 8px;
  padding: 10px 12px;
  border-top: 1px solid var(--border);
  align-items: flex-end;
}
.ai-textarea {
  flex: 1;
  resize: none;
  min-height: 38px;
  max-height: 140px;
  line-height: 1.5;
  font-family: var(--sans);
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
  animation: ai-bounce 1s infinite ease-in-out;
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
@keyframes ai-bounce {
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
.ai-panel-enter-active,
.ai-panel-leave-active {
  transition: opacity 0.18s, transform 0.18s;
}
.ai-panel-enter-from,
.ai-panel-leave-to {
  opacity: 0;
  transform: translateY(12px) scale(0.97);
}
</style>
