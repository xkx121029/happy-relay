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

async function load(silent = false) {
  if (!silent) loading.value = true
  try {
    const [overview, stats] = await Promise.all([api.getOverview(), api.getStats(14)])
    data.value = overview
    report.value = stats
  } catch (e: any) {
    // 自动刷新失败时保持安静，避免反复弹出提示。
    if (!silent) toast(e.message || '概览加载失败', 'error')
  } finally {
    if (!silent) loading.value = false
  }
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
      <button class="btn btn-sm" :disabled="loading" @click="load">刷新</button>
    </div>

    <div class="grid cols-4" style="margin-bottom: 16px">
      <div class="stat">
        <div class="label">累计调用</div>
        <div class="value">{{ fmtNumber(stats.total_calls) }}</div>
        <div class="foot">今日 {{ fmtNumber(stats.today_calls) }} 次</div>
      </div>
      <div class="stat">
        <div class="label">成功率</div>
        <div class="value" :style="{ color: stats.success_rate >= 95 ? 'var(--ok)' : 'var(--warn)' }">
          {{ stats.success_rate.toFixed(1) }}%
        </div>
        <div class="foot">失败 {{ fmtNumber(stats.failed_calls) }} 次</div>
      </div>
      <div class="stat">
        <div class="label">平均耗时</div>
        <div class="value">{{ Math.round(stats.avg_latency_ms) }}<span style="font-size: 14px"> ms</span></div>
        <div class="foot">Token 用量 {{ fmtNumber(stats.prompt_tokens + stats.completion_tokens) }}</div>
      </div>
      <div class="stat">
        <div class="label">资源数量</div>
        <div class="value">{{ stats.providers }}<span style="font-size: 14px"> 渠道</span></div>
        <div class="foot">{{ stats.models }} 模型 · {{ stats.keys }} 秘钥 · {{ stats.skills }} 技能</div>
      </div>
    </div>

    <div class="grid cols-2" style="margin-bottom: 16px">
      <div class="card">
        <h3 class="card-title">近 14 天调用趋势</h3>
        <div v-if="daily.length" class="bars">
          <div v-for="d in daily" :key="d.date" class="bar-col">
            <div class="bar-track">
              <div
                class="bar"
                :class="{ 'has-fail': d.failed > 0 }"
                :style="{ height: barHeight(d.total) }"
                :title="`${d.date}：共 ${d.total} 次，失败 ${d.failed} 次`"
              />
            </div>
            <span class="bar-label">{{ d.date.slice(5) }}</span>
          </div>
        </div>
        <div v-else class="empty">
          <div class="big">暂无调用数据</div>
          <div>配置渠道与模型后，从调试台发起一次调用即可看到统计。</div>
        </div>
      </div>

      <div class="card">
        <h3 class="card-title">最近视频任务</h3>
        <div v-if="data?.recent_tasks?.length" style="display: flex; flex-direction: column; gap: 12px">
          <div v-for="t in data.recent_tasks" :key="t.id">
            <div class="row" style="justify-content: space-between">
              <span class="mono clamp-1" style="max-width: 220px">{{ t.model }}</span>
              <span class="badge" :class="taskBadge(t.status)">{{ taskLabel(t.status) }}</span>
            </div>
            <div class="progress" style="margin: 7px 0 4px">
              <i :style="{ width: t.progress + '%' }" />
            </div>
            <div class="row" style="justify-content: space-between; font-size: 12px">
              <span class="muted mono">{{ t.id }}</span>
              <span class="muted">{{ relative(t.created_at) }}</span>
            </div>
          </div>
        </div>
        <div v-else class="empty">
          <div class="big">暂无视频任务</div>
        </div>
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
                <th>调用</th>
                <th>失败</th>
                <th>Token</th>
                <th>平均耗时</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="m in report.by_model" :key="m.model">
                <td class="mono clamp-1">{{ m.model }}</td>
                <td class="dim">{{ m.calls }}</td>
                <td :style="m.failed ? 'color: var(--danger)' : ''">{{ m.failed }}</td>
                <td class="muted">{{ fmtNumber(m.tokens) }}</td>
                <td class="muted nowrap">{{ Math.round(m.avg_latency_ms) }} ms</td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-else class="empty"><div class="big">暂无数据</div></div>
      </div>

      <div class="card">
        <h3 class="card-title">渠道用量 · 近 14 天</h3>
        <div v-if="report?.by_provider?.length" class="table-wrap" style="border: none">
          <table class="table">
            <thead>
              <tr>
                <th>渠道</th>
                <th>调用</th>
                <th>失败</th>
                <th>平均耗时</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="p in report.by_provider" :key="p.provider">
                <td class="clamp-1">{{ p.provider }}</td>
                <td class="dim">{{ p.calls }}</td>
                <td :style="p.failed ? 'color: var(--danger)' : ''">{{ p.failed }}</td>
                <td class="muted nowrap">{{ Math.round(p.avg_latency_ms) }} ms</td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-else class="empty"><div class="big">暂无数据</div></div>
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
              <th>耗时</th>
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
              <td class="muted nowrap">{{ l.latency_ms }} ms</td>
            </tr>
          </tbody>
        </table>
      </div>
      <div v-else class="empty">
        <div class="big">还没有调用记录</div>
      </div>
    </div>
  </div>
</template>
