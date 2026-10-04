<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import Modal from '../components/Modal.vue'
import { api, type UsageLog } from '../api'
import { confirmDialog, state, toast } from '../store'
import { capLabel, fmtTime, fmtNumber, prettyJSON, renderMarkdown } from '../utils'

const list = ref<UsageLog[]>([])
const loading = ref(true)
const loadingMore = ref(false)
const capFilter = ref('')
const keyword = ref('')
const detail = ref<UsageLog | null>(null)
// 是否还有更早的日志：最近一次查询返回满页就假定还有，返回空页则到底。
const PAGE = 200
const hasMore = ref(false)
// 自动刷新：开启后每 10 秒拉一次第一页（会重置分页位置）。
const autoRefresh = ref(false)
let autoTimer: number | null = null

// 详情弹窗里的输入/输出切换：默认先看输入，没有请求体时直接看输出。
const detailTab = ref<'req' | 'res'>('req')
watch(detail, (v) => {
  if (v) detailTab.value = v.request_body ? 'req' : 'res'
})

// 保存策略从 meta 的 runtime 快照里读，页面上不提供修改入口（去设置页改）。
const runtime = computed(() => state.meta?.runtime?.values)
const retentionLabel = computed(() => {
  const r = runtime.value
  if (!r) return ''
  return r.log_retention_days > 0 ? `${r.log_retention_days} 天` : '不限时'
})

async function copyBody() {
  const text = detailTab.value === 'req' ? detail.value?.request_body : detail.value?.response_preview
  if (!text) return
  try {
    await navigator.clipboard.writeText(text)
    toast('已复制到剪贴板')
  } catch {
    toast('复制失败：浏览器未授权剪贴板', 'error')
  }
}

const capOptions = computed(
  () =>
    state.meta?.capabilities || [
      { value: 'chat', label: '文本对话' },
      { value: 'image', label: '图片生成' },
      { value: 'video', label: '视频生成' },
      { value: 'embedding', label: '文本向量' },
    ],
)

async function load() {
  loading.value = true
  try {
    if (!state.meta) state.meta = await api.getMeta()
    const rows = await api.listLogs({ limit: PAGE, capability: capFilter.value, q: keyword.value.trim() })
    list.value = rows
    hasMore.value = rows.length >= PAGE
  } catch (e: any) {
    toast(e.message || '加载失败', 'error')
  } finally {
    loading.value = false
  }
}

// loadMore 游标分页：以已加载最后一条的 id 为游标继续往更早翻。
async function loadMore() {
  if (loadingMore.value || !list.value.length) return
  loadingMore.value = true
  try {
    const before = list.value[list.value.length - 1].id
    const rows = await api.listLogs({
      limit: PAGE,
      before,
      capability: capFilter.value,
      q: keyword.value.trim(),
    })
    list.value.push(...rows)
    hasMore.value = rows.length >= PAGE
    if (!rows.length) toast('没有更早的日志了')
  } catch (e: any) {
    toast(e.message || '加载失败', 'error')
  } finally {
    loadingMore.value = false
  }
}

function setAutoRefresh(on: boolean) {
  autoRefresh.value = on
  if (autoTimer !== null) {
    clearInterval(autoTimer)
    autoTimer = null
  }
  if (on) autoTimer = window.setInterval(load, 10_000)
}

onUnmounted(() => setAutoRefresh(false))

// 导出当前已加载的日志为 CSV（带 BOM，Excel 打开不乱码）。正文太长不入表，只导结构化字段。
function exportCSV() {
  if (!list.value.length) return
  const esc = (v: unknown) => {
    const s = String(v ?? '')
    return /[",\n]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s
  }
  const head = ['时间', '能力', '模型', '渠道', '秘钥', '状态', '流式', '耗时ms', '输入Token', '输出Token', '花费', '错误']
  const lines = [head.map(esc).join(',')]
  for (const l of list.value) {
    lines.push(
      [
        new Date(l.created_at).toLocaleString('zh-CN', { hour12: false }),
        capLabel(l.capability),
        l.model,
        l.provider_name || '',
        l.api_key_name || '',
        ok(l) ? '成功' : '失败',
        l.stream ? '是' : '否',
        l.latency_ms,
        l.prompt_tokens,
        l.completion_tokens,
        l.cost ?? '',
        l.error || '',
      ]
        .map(esc)
        .join(','),
    )
  }
  const blob = new Blob(['\uFEFF' + lines.join('\r\n')], { type: 'text/csv;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `relay-logs-${new Date().toISOString().slice(0, 10)}.csv`
  a.click()
  URL.revokeObjectURL(url)
  toast(`已导出 ${list.value.length} 条日志`)
}

onMounted(load)

async function clearAll() {
  const ok = await confirmDialog({
    title: '清空调用日志',
    message: '确定清空所有调用日志？该操作不可恢复。',
    confirmText: '清空',
  })
  if (!ok) return
  try {
    await api.clearLogs()
    toast('日志已清空')
    await load()
  } catch (e: any) {
    toast(e.message || '清空失败', 'error')
  }
}

function ok(l: UsageLog) {
  return l.status < 400 && !l.error
}

// ---------- AI 诊断（主模型撰写，按日志 id 服务端缓存） ----------
const diagText = ref('')
const diagBusy = ref(false)
const diagCached = ref(false)

async function diagnose(refresh = false) {
  if (!detail.value || diagBusy.value) return
  diagBusy.value = true
  try {
    const r = await api.diagnoseLog(detail.value.id, refresh)
    diagText.value = r.diagnosis
    diagCached.value = r.cached
  } catch (e: any) {
    toast(e.message || '诊断失败', 'error')
  } finally {
    diagBusy.value = false
  }
}
// 打开新日志详情时重置诊断区；失败调用自动触发诊断。
watch(detail, (d) => {
  diagText.value = ''
  diagCached.value = false
  if (d && !ok(d)) diagnose()
})

// openDetail 列表数据只带 600 字符正文预览（列表性能优化），
// 打开详情时按 id 取一次全量替换；失败保持列表版本，不打断查看。
const LIST_PREVIEW_LEN = 600
async function openDetail(l: UsageLog) {
  detail.value = l
  const truncated =
    (l.request_body && l.request_body.length >= LIST_PREVIEW_LEN) ||
    (l.response_preview && l.response_preview.length >= LIST_PREVIEW_LEN)
  if (!truncated) return
  try {
    const full = await api.getLog(l.id)
    if (detail.value?.id === l.id) detail.value = full
  } catch {
    /* 静默：预览够看 */
  }
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h2>调用日志</h2>
        <p>记录每次调用的完整输入输出、模型、渠道、耗时与 token 用量。</p>
      </div>
      <div class="row">
        <button class="btn btn-sm" :class="autoRefresh ? '' : 'btn-ghost'" @click="setAutoRefresh(!autoRefresh)">
          {{ autoRefresh ? '自动刷新中 · 10s' : '自动刷新' }}
        </button>
        <button class="btn btn-sm" :disabled="!list.length" @click="exportCSV">导出 CSV</button>
        <button class="btn btn-sm" :disabled="loading" @click="load">刷新</button>
        <button class="btn btn-sm btn-danger" @click="clearAll">清空日志</button>
      </div>
    </div>

    <div v-if="runtime" class="card log-policy">
      <span class="muted" style="font-size: 12.5px">保存策略</span>
      <span class="badge" :class="runtime.log_body ? 'ok' : 'danger'">
        {{ runtime.log_body ? '记录完整输入输出' : '隐私模式：不记录正文' }}
      </span>
      <span class="badge">保留 {{ retentionLabel }}</span>
      <span class="badge">条数上限 {{ fmtNumber(runtime.max_log_rows) }}</span>
      <span class="badge">单条体积上限 {{ runtime.log_body_limit_kb }} KB</span>
      <RouterLink class="btn btn-sm btn-ghost" to="/settings" style="margin-left: auto">去调整</RouterLink>
    </div>

    <div class="toolbar">
      <select v-model="capFilter" class="select" style="width: 150px" @change="load">
        <option value="">全部能力</option>
        <option v-for="c in capOptions" :key="c.value" :value="c.value">{{ c.label }}</option>
      </select>
      <input
        v-model="keyword"
        class="input"
        style="width: 250px"
        placeholder="搜索模型 / 渠道 / 秘钥 / 错误"
        @keydown.enter="load"
      />
      <button class="btn btn-sm" @click="load">查询</button>
      <span v-if="list.length" class="muted" style="font-size: 12px">共 {{ list.length }} 条</span>
    </div>

    <div v-if="loading" class="table-wrap">
      <table class="table">
        <tbody>
          <tr v-for="i in 6" :key="i">
            <td v-for="j in 9" :key="j"><div class="skeleton" /></td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-else-if="list.length" class="table-wrap">
      <table class="table">
        <thead>
          <tr>
            <th>时间</th>
            <th>能力</th>
            <th>模型</th>
            <th>渠道</th>
            <th>秘钥</th>
            <th>状态</th>
            <th class="num">耗时</th>
            <th class="num">Token</th>
            <th style="text-align: right">详情</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="l in list" :key="l.id">
            <td class="nowrap muted">{{ fmtTime(l.created_at) }}</td>
            <td><span class="badge brand">{{ capLabel(l.capability) }}</span></td>
            <td class="mono clamp-1">{{ l.model }}</td>
            <td class="dim clamp-1">{{ l.provider_name || '—' }}</td>
            <td class="dim clamp-1">{{ l.api_key_name || '—' }}</td>
            <td>
              <span class="badge" :class="ok(l) ? 'ok' : 'danger'">{{ ok(l) ? '成功' : '失败' }}</span>
              <span v-if="l.stream" class="badge" style="margin-left: 4px">流式</span>
            </td>
            <td class="num muted nowrap">{{ l.latency_ms }} ms</td>
            <td class="num muted nowrap">{{ fmtNumber(l.prompt_tokens + l.completion_tokens) }}</td>
            <td>
              <div class="row" style="justify-content: flex-end">
                <button class="btn btn-sm btn-ghost" @click="openDetail(l)">查看</button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
      <div
        v-if="hasMore || list.length >= PAGE"
        class="row"
        style="justify-content: center; padding: 12px; border-top: 1px solid var(--border)"
      >
        <button class="btn btn-sm" :disabled="loadingMore" @click="loadMore">
          {{ loadingMore ? '加载中…' : '加载更早日志' }}
        </button>
        <span class="muted" style="font-size: 12px; margin-left: 8px">已显示 {{ list.length }} 条</span>
      </div>
    </div>

    <div v-else class="card empty">
      <div class="empty-icon">≡</div>
      <div class="big">
        {{ keyword || capFilter ? '没有匹配的日志' : '暂无日志' }}
      </div>
      <div v-if="keyword || capFilter">换个关键词或清空筛选条件再试。</div>
      <div v-else>发起一次调用后这里会记录明细。</div>
      <div v-if="keyword || capFilter" class="empty-cta">
        <button
          class="btn btn-sm"
          @click="
            () => {
              keyword = ''
              capFilter = ''
            }
          "
        >
          清空筛选条件
        </button>
      </div>
      <div v-else class="empty-cta">
        <RouterLink class="btn btn-sm" to="/playground">去调试台跑一次</RouterLink>
      </div>
    </div>

    <Modal v-if="detail" wide title="调用详情" :subtitle="fmtTime(detail.created_at)" @close="detail = null">
      <div class="meta-list" style="margin-bottom: 16px">
        <dt>能力 / 模型</dt>
        <dd>{{ capLabel(detail.capability) }} · <span class="mono">{{ detail.model }}</span></dd>
        <dt>渠道 / 秘钥</dt>
        <dd>{{ detail.provider_name || '—' }} · {{ detail.api_key_name || '—' }}</dd>
        <dt>状态 / 耗时</dt>
        <dd>{{ ok(detail) ? '成功' : '失败' }} · {{ detail.latency_ms }} ms{{ detail.stream ? ' · 流式' : '' }}</dd>
        <dt>Token</dt>
        <dd>
          输入 {{ detail.prompt_tokens }} / 输出 {{ detail.completion_tokens }}
          <template v-if="detail.cost"> · 约 ¥{{ detail.cost.toFixed(4) }}</template>
        </dd>
      </div>

      <div v-if="detail.error" class="field">
        <div class="field-label">错误信息</div>
        <pre class="code-block" style="color: var(--danger)">{{ detail.error }}</pre>
      </div>

      <!-- AI 诊断：失败自动触发，也可手动重跑 -->
      <div class="field">
        <div class="row" style="justify-content: space-between; margin-bottom: 8px">
          <div class="field-label" style="margin: 0">
            🤖 AI 诊断
            <span v-if="diagCached && diagText" class="muted" style="font-size: 11.5px; margin-left: 6px">缓存结果</span>
          </div>
          <button class="btn btn-sm btn-ghost" :disabled="diagBusy" @click="diagnose(true)">
            {{ diagBusy ? '主模型分析中…' : diagText ? '↻ 重新诊断' : '🤖 AI 诊断' }}
          </button>
        </div>
        <div v-if="diagBusy && !diagText" class="muted" style="font-size: 13px; padding: 10px 0">
          <span class="diag-spinner" /> 主模型正在分析这条调用…
        </div>
        <div v-else-if="diagText" class="md diag-md" v-html="renderMarkdown(diagText)"></div>
        <div v-else class="muted" style="font-size: 13px">让主模型基于这条日志的模型/渠道/状态码/错误片段给出根因与修复建议。</div>
      </div>

      <div v-if="detail.request_body || detail.response_preview" class="field">
        <div class="row" style="justify-content: space-between; margin-bottom: 8px">
          <div class="row" style="gap: 6px">
            <button
              v-if="detail.request_body"
              class="btn btn-sm"
              :class="detailTab === 'req' ? '' : 'btn-ghost'"
              @click="detailTab = 'req'"
            >
              输入 · 请求体
            </button>
            <button
              v-if="detail.response_preview"
              class="btn btn-sm"
              :class="detailTab === 'res' ? '' : 'btn-ghost'"
              @click="detailTab = 'res'"
            >
              输出 · 模型回复
            </button>
          </div>
          <button class="btn btn-sm btn-ghost" @click="copyBody">复制</button>
        </div>
        <pre v-if="detailTab === 'req'" class="code-block log-body">{{ prettyJSON(detail.request_body || '') }}</pre>
        <pre v-else class="code-block log-body">{{ detail.response_preview }}</pre>
      </div>
    </Modal>
  </div>
</template>
