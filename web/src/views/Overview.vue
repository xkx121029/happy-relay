<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { api, type Overview, type StatsReport } from '../api'
import { capLabel, fmtNumber, fmtTime, relative } from '../utils'
import { prefs, toast } from '../store'

const router = useRouter()
const data = ref<Overview | null>(null)
const report = ref<StatsReport | null>(null)
const loading = ref(true)
const failed = ref(false)
const lastUpdated = ref('')

async function load(silent = false) {
  if (!silent) loading.value = true
  try {
    const [overview, stats, provs, health] = await Promise.all([
      api.getOverview(),
      api.getStats(14),
      api.listProviders(),
      api.healthHistory({ limit: 1 }),
    ])
    data.value = overview
    report.value = stats
    buildUptime(pros(provs), health.uptime || {})
    lastUpdated.value = new Date().toLocaleTimeString('zh-CN', { hour12: false })
    failed.value = false
  } catch (e: any) {
    // 自动刷新失败时保持安静，避免反复弹出提示。
    if (!silent) {
      failed.value = true
      toast(e.message || '概览加载失败', 'error')
    }
  } finally {
    if (!silent) loading.value = false
  }
}

// ---------- 渠道健康速览 ----------

interface UptimeRow {
  id: number
  name: string
  uptime: number // -1 表示没有巡检样本
  avg: number
  lastOk: boolean
  err: string
}

const uptimeRows = ref<UptimeRow[]>([])

function pros(list: { id: number; name: string; enabled: boolean }[]) {
  return list.filter((p) => p.enabled)
}

function buildUptime(provs: { id: number; name: string }[], up: Record<string, any>) {
  uptimeRows.value = provs
    .map((p) => {
      const u = up[String(p.id)]
      return {
        id: p.id,
        name: p.name,
        uptime: u ? (u.uptime as number) : -1,
        avg: u ? Math.round(u.avg_latency_ms as number) : 0,
        lastOk: u ? (u.last_ok as boolean) : true,
        err: u?.last_error || '',
      }
    })
    // 有异常的渠道排前面，健康速览一眼看到的就是问题项。
    .sort((a, b) => {
      if (a.uptime < 0) return 1
      if (b.uptime < 0) return -1
      return a.uptime - b.uptime
    })
}

function uptimeClass(u: number): string {
  if (u < 0) return 'mute'
  if (u >= 95) return 'ok'
  if (u >= 70) return 'warn'
  return 'danger'
}

function uptimeText(u: number): string {
  return u < 0 ? '无数据' : `${u.toFixed(0)}%`
}

let refreshTimer: number | null = null

// scheduleRefresh 按设置里的间隔静默刷新，0 表示关闭。
function scheduleRefresh() {
  if (refreshTimer !== null) window.clearInterval(refreshTimer)
  refreshTimer = null
  const sec = prefs.autoRefreshSec
  if (sec > 0) {
    refreshTimer = window.setInterval(() => load(true), sec * 1000)
  }
}

onMounted(async () => {
  await load()
  scheduleRefresh()
})

onUnmounted(() => {
  if (refreshTimer !== null) window.clearInterval(refreshTimer)
})

const daily = computed(() => [...(data.value?.daily || [])].reverse())
const dailyMax = computed(() => Math.max(1, ...daily.value.map((d) => d.total)))

function barHeight(total: number): string {
  return Math.max(3, Math.round((total / dailyMax.value) * 100)) + '%'
}

// 失败段占整根柱子的比例：柱内底部叠红色，一眼看出哪天在抖。
function failPct(d: { total: number; failed: number }): string {
  if (!d.total || !d.failed) return '0%'
  return Math.min(100, Math.round((d.failed / d.total) * 100)) + '%'
}

const stats = computed(
  () =>
    data.value?.stats || {
      providers: 0,
      models: 0,
      keys: 0,
      skills: 0,
      today_calls: 0,
      total_calls: 0,
      success_rate: 100,
      avg_latency_ms: 0,
      prompt_tokens: 0,
      completion_tokens: 0,
      failed_calls: 0,
    },
)

function taskBadge(status: string) {
  if (status === 'succeeded') return 'ok'
  if (status === 'failed') return 'danger'
  return 'warn'
}

function taskLabel(status: string) {
  return { queued: '排队中', running: '生成中', succeeded: '已完成', failed: '失败' }[status] || status
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h2>运行概览</h2>
        <p>中转站的整体调用情况与最近动态。</p>
      </div>
      <div class="row">
        <span v-if="lastUpdated && !loading" class="muted nowrap" style="font-size: 12px">
          更新于 {{ lastUpdated }}
        </span>
        <button class="btn btn-sm" :disabled="loading" @click="load()">刷新</button>
      </div>
    </div>

    <!-- 加载态：用骨架屏占位，避免布局跳动 -->
    <template v-if="loading">
      <div class="grid cols-4" style="margin-bottom: 16px">
        <div v-for="i in 4" :key="i" class="stat">
          <div class="skeleton" style="width: 46px" />
          <div class="skeleton" style="width: 78px; height: 22px; margin-top: 9px" />
          <div class="skeleton" style="width: 60px; margin-top: 8px" />
        </div>
      </div>
      <div class="grid cols-2">
        <div class="card"><div class="skeleton" style="height: 130px" /></div>
        <div class="card"><div class="skeleton" style="height: 130px" /></div>
      </div>
    </template>

    <!-- 失败态：给出明确重试入口 -->
    <div v-else-if="failed" class="card empty">
      <div class="big">概览加载失败</div>
      <div style="margin-bottom: 14px">无法连接服务端，请确认服务正在运行。</div>
      <button class="btn btn-primary btn-sm" @click="load()">重试</button>
    </div>

    <template v-else>
      <div class="grid cols-4" style="margin-bottom: 16px">
        <div class="stat stat-brand">
          <div class="label">累计调用</div>
          <div class="value">{{ fmtNumber(stats.total_calls) }}</div>
          <div class="foot">今日 {{ fmtNumber(stats.today_calls) }} 次</div>
        </div>
        <div class="stat" :class="stats.success_rate >= 95 ? 'stat-ok' : 'stat-warn'">
          <div class="label">成功率</div>
          <div class="value" :style="{ color: stats.success_rate >= 95 ? 'var(--ok)' : 'var(--warn)' }">
            {{ stats.success_rate.toFixed(1) }}%
          </div>
          <div class="foot">失败 {{ fmtNumber(stats.failed_calls) }} 次</div>
        </div>
        <div class="stat">
          <div class="label">平均耗时</div>
          <div class="value">{{ Math.round(stats.avg_latency_ms) }}<span style="font-size: 13px"> ms</span></div>
          <div class="foot">Token {{ fmtNumber(stats.prompt_tokens + stats.completion_tokens) }}</div>
        </div>
        <div class="stat">
          <div class="label">资源数量</div>
          <div class="value">{{ stats.providers }}<span style="font-size: 13px"> 渠道</span></div>
          <div class="foot">{{ stats.models }} 模型 · {{ stats.keys }} 秘钥 · {{ stats.skills }} 技能</div>
        </div>
      </div>

      <div class="grid cols-2" style="margin-bottom: 16px">
        <div class="card">
          <h3 class="card-title">近 14 天调用趋势</h3>
          <div v-if="daily.length" class="bars">
            <div v-for="(d, i) in daily" :key="d.date" class="bar-col">
              <div class="bar-track">
                <div
                  class="bar"
                  :class="{ 'has-fail': d.failed > 0, today: i === daily.length - 1 }"
                  :style="{ height: barHeight(d.total) }"
                  :title="`${d.date}：共 ${d.total} 次，失败 ${d.failed} 次`"
                >
                  <i v-if="d.failed > 0" class="bar-fail" :style="{ height: failPct(d) }" />
                </div>
              </div>
              <span class="bar-label" :class="{ 'bar-label-today': i === daily.length - 1 }">
                {{ d.date.slice(5) }}
              </span>
            </div>
          </div>
          <div v-else class="empty">
            <div class="empty-icon">▤</div>
            <div class="big">暂无调用数据</div>
            <div>配置渠道与模型后，从调试台发起一次调用即可看到统计。</div>
            <div class="empty-cta">
              <RouterLink class="btn btn-sm" to="/providers">添加渠道</RouterLink>
              <RouterLink class="btn btn-sm btn-ghost" to="/playground">去调试台</RouterLink>
            </div>
          </div>
        </div>

        <div class="card">
          <div class="row" style="justify-content: space-between; margin-bottom: 14px">
            <h3 class="card-title" style="margin: 0">最近视频任务</h3>
            <button class="btn btn-sm btn-ghost" @click="router.push('/tasks')">全部</button>
          </div>
          <div v-if="data?.recent_tasks?.length" style="display: flex; flex-direction: column; gap: 13px">
            <div v-for="t in data.recent_tasks" :key="t.id">
              <div class="row" style="justify-content: space-between; gap: 8px">
                <span class="mono clamp-1" style="max-width: 220px">{{ t.model }}</span>
                <span class="badge" :class="taskBadge(t.status)">{{ taskLabel(t.status) }}</span>
              </div>
              <div class="progress" style="margin: 6px 0 4px">
                <i :style="{ width: t.progress + '%' }" />
              </div>
              <div class="row" style="justify-content: space-between; font-size: 11.5px">
                <span class="muted mono clamp-1">{{ t.id }}</span>
                <span class="muted nowrap">{{ relative(t.created_at) }}</span>
              </div>
            </div>
          </div>
          <div v-else class="empty">
            <div class="big">暂无视频任务</div>
            <div>通过 /v1/videos 或调试台提交后，这里会显示进度。</div>
          </div>
        </div>
      </div>

      <!-- 渠道健康速览：巡检样本里的可用率，异常渠道排最前 -->
      <div v-if="uptimeRows.length" class="card" style="margin-bottom: 16px">
        <div class="row" style="justify-content: space-between; margin-bottom: 12px">
          <h3 class="card-title" style="margin: 0">渠道健康速览</h3>
          <button class="btn btn-sm btn-ghost" @click="router.push('/providers')">渠道管理</button>
        </div>
        <div class="uptime-grid">
          <button
            v-for="u in uptimeRows"
            :key="u.id"
            class="uptime-item"
            :title="u.err ? `最近错误：${u.err}` : u.avg ? `平均耗时 ${u.avg} ms` : u.name"
            @click="router.push('/providers')"
          >
            <span class="uptime-dot" :class="uptimeClass(u.uptime)" />
            <span class="uptime-name clamp-1">{{ u.name }}</span>
            <span class="uptime-pct" :class="uptimeClass(u.uptime)">{{ uptimeText(u.uptime) }}</span>
            <span v-if="u.uptime >= 0 && u.avg" class="uptime-avg nowrap">{{ u.avg }} ms</span>
          </button>
        </div>
      </div>

      <div class="grid cols-2" style="margin-bottom: 16px">
        <div class="card">
          <h3 class="card-title">模型用量 · 近 14 天</h3>
          <div v-if="report?.by_model?.length" class="table-wrap" style="border: none">
            <table class="table">
              <thead>
                <tr>
                  <th>模型</th>
                  <th class="num">调用</th>
                  <th class="num">失败</th>
                  <th class="num">Token</th>
                  <th class="num">平均耗时</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="m in report.by_model" :key="m.model">
                  <td class="mono clamp-1">{{ m.model }}</td>
                  <td class="num dim">{{ m.calls }}</td>
                  <td class="num" :style="m.failed ? 'color: var(--danger)' : ''">{{ m.failed }}</td>
                  <td class="num muted">{{ fmtNumber(m.tokens) }}</td>
                  <td class="num muted nowrap">{{ Math.round(m.avg_latency_ms) }} ms</td>
                </tr>
              </tbody>
            </table>
          </div>
          <div v-else class="empty"><div class="big">近 14 天暂无数据</div></div>
        </div>

        <div class="card">
          <h3 class="card-title">渠道用量 · 近 14 天</h3>
          <div v-if="report?.by_provider?.length" class="table-wrap" style="border: none">
            <table class="table">
              <thead>
                <tr>
                  <th>渠道</th>
                  <th class="num">调用</th>
                  <th class="num">失败</th>
                  <th class="num">平均耗时</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="p in report.by_provider" :key="p.provider">
                  <td class="clamp-1">{{ p.provider }}</td>
                  <td class="num dim">{{ p.calls }}</td>
                  <td class="num" :style="p.failed ? 'color: var(--danger)' : ''">{{ p.failed }}</td>
                  <td class="num muted nowrap">{{ Math.round(p.avg_latency_ms) }} ms</td>
                </tr>
              </tbody>
            </table>
          </div>
          <div v-else class="empty">
            <div class="empty-icon">▤</div>
            <div class="big">近 14 天暂无数据</div>
          </div>
        </div>
      </div>

      <div class="card">
        <div class="row" style="justify-content: space-between; margin-bottom: 14px">
          <h3 class="card-title" style="margin: 0">最近调用</h3>
          <button class="btn btn-sm btn-ghost" @click="router.push('/logs')">查看全部</button>
        </div>
        <div v-if="data?.recent_logs?.length" class="table-wrap" style="border: none">
          <table class="table">
            <thead>
              <tr>
                <th>时间</th>
                <th>能力</th>
                <th>模型</th>
                <th>渠道</th>
                <th>状态</th>
                <th class="num">耗时</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="l in data.recent_logs" :key="l.id">
                <td class="nowrap muted">{{ fmtTime(l.created_at) }}</td>
                <td><span class="badge brand">{{ capLabel(l.capability) }}</span></td>
                <td class="mono clamp-1">{{ l.model }}</td>
                <td class="dim clamp-1">{{ l.provider_name || '—' }}</td>
                <td>
                  <span class="badge" :class="l.status >= 400 || l.error ? 'danger' : 'ok'">
                    {{ l.status >= 400 || l.error ? '失败' : '成功' }}
                  </span>
                </td>
                <td class="num muted nowrap">{{ l.latency_ms }} ms</td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-else class="empty">
          <div class="empty-icon">≡</div>
          <div class="big">还没有调用记录</div>
          <div>从调试台发一条消息，或者用秘钥从客户端调用一次，这里就会记录下来。</div>
          <div class="empty-cta">
            <RouterLink class="btn btn-sm" to="/playground">去调试台</RouterLink>
            <RouterLink class="btn btn-sm btn-ghost" to="/keys">创建访问秘钥</RouterLink>
          </div>
        </div>
      </div>
    </template>
  </div>
</template>
