<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import type { RuntimeOptions } from '../api'
import { api } from '../api'
import { applyPrefs, models, panelTitle, state, toast } from '../store'

const loading = ref(true)
const saving = ref(false)
const backupBusy = ref(false)

// 表单统一用字符串保存，便于直接映射到 settings 表的键值；
// 运行参数留空表示沿用环境变量提供的默认值。
const form = reactive({
  panel_title: '',
  system_model: '',
  default_page: '/',
  theme: 'dark',
  compact: false,
  confirm_danger: true,
  auto_refresh_sec: '0',
  max_log_rows: '',
  log_body: true,
  stream_smooth: true,
  stream_rate: '',
  heartbeat_sec: '',
  video_poll_sec: '',
})

const chatModels = computed(() => models.value.filter((m) => m.enabled && m.capability === 'chat'))
const defaults = computed(() => state.meta?.runtime?.defaults)
const effective = computed(() => state.meta?.runtime?.values)

const pageOptions = [
  { value: '/', label: '概览' },
  { value: '/playground', label: '调试台' },
  { value: '/tasks', label: '任务' },
  { value: '/providers', label: '渠道' },
  { value: '/catalog', label: '模型库' },
  { value: '/models', label: '模型' },
  { value: '/keys', label: '秘钥' },
  { value: '/skills', label: '技能' },
  { value: '/connector', label: '连接器生成器' },
  { value: '/logs', label: '调用日志' },
]

// def 返回某个运行参数在未显式设置时的默认值，用于输入框占位提示。
function def(key: keyof RuntimeOptions): string {
  const v = defaults.value?.[key]
  return v === undefined ? '' : String(v)
}

async function load() {
  loading.value = true
  try {
    const meta = state.meta || (await api.getMeta())
    state.meta = meta
    const [settings, ms] = await Promise.all([api.getSettings(), api.listModels()])
    models.value = ms
    const s = { ...meta.settings, ...settings }

    form.panel_title = s.panel_title || ''
    form.system_model = s.system_model || ''
    form.default_page = s.default_page || '/'
    form.theme = s.theme || 'dark'
    form.compact = s.compact === 'on'
    form.confirm_danger = s.confirm_danger !== 'off'
    form.auto_refresh_sec = s.auto_refresh_sec || '0'

    form.max_log_rows = s.max_log_rows || ''
    form.stream_rate = s.stream_rate || ''
    form.heartbeat_sec = s.heartbeat_sec || ''
    form.video_poll_sec = s.video_poll_sec || ''
    // 开关类没有「留空」状态：没设置过就按当前生效值显示（可能来自环境变量）。
    form.log_body = s.log_body ? s.log_body !== 'off' : (effective.value?.log_body ?? true)
    form.stream_smooth = s.stream_smooth
      ? s.stream_smooth !== 'off'
      : (effective.value?.stream_smooth ?? true)

    panelTitle.value = form.panel_title || '模型中转站'
    document.title = panelTitle.value
  } catch (e: any) {
    toast(e.message || '加载失败', 'error')
  } finally {
    loading.value = false
  }
}

onMounted(load)

// numStr 只保留纯数字，空白或非法输入返回空串（表示沿用默认值）。
function numStr(v: string): string {
  const t = v.trim()
  return /^\d+$/.test(t) ? t : ''
}

async function save() {
  saving.value = true
  try {
    const payload: Record<string, string> = {
      panel_title: form.panel_title.trim(),
      system_model: form.system_model,
      default_page: form.default_page,
      theme: form.theme,
      compact: form.compact ? 'on' : 'off',
      confirm_danger: form.confirm_danger ? 'on' : 'off',
      auto_refresh_sec: numStr(form.auto_refresh_sec),
      max_log_rows: numStr(form.max_log_rows),
      log_body: form.log_body ? 'on' : 'off',
      stream_smooth: form.stream_smooth ? 'on' : 'off',
      stream_rate: numStr(form.stream_rate),
      heartbeat_sec: numStr(form.heartbeat_sec),
      video_poll_sec: numStr(form.video_poll_sec),
    }
    const saved = await api.saveSettings(payload)
    // 重新拉取元信息，让「当前生效值」与默认值提示保持准确。
    const meta = await api.getMeta()
    state.meta = meta
    applyPrefs({ ...meta.settings, ...saved })
    panelTitle.value = form.panel_title.trim() || '模型中转站'
    document.title = panelTitle.value
    toast('设置已保存并生效')
  } catch (e: any) {
    toast(e.message || '保存失败', 'error')
  } finally {
    saving.value = false
  }
}

const baseURL = computed(() => `${location.origin}/v1`)

// exportBackup 把全部配置下载成 JSON 文件。
async function exportBackup() {
  backupBusy.value = true
  try {
    const data = await api.exportBackup()
    const blob = new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `happy-relay-backup-${new Date().toISOString().slice(0, 10)}.json`
    a.click()
    URL.revokeObjectURL(url)
    toast('配置已导出')
  } catch (e: any) {
    toast(e.message || '导出失败', 'error')
  } finally {
    backupBusy.value = false
  }
}

// onImportFile 读取选中的备份文件并合并导入。
async function onImportFile(e: Event) {
  const input = e.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file) return
  try {
    const payload = JSON.parse(await file.text())
    const summary = await api.importBackup(payload)
    toast(
      `导入完成：渠道 ${summary.providers ?? 0}、模型 ${summary.models ?? 0}、秘钥 ${summary.keys ?? 0}、技能 ${summary.skills ?? 0}`,
    )
    await load()
  } catch (err: any) {
    toast(err.message || '导入失败', 'error')
  } finally {
    input.value = ''
  }
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h2>设置</h2>
        <p>面板偏好与服务端运行参数，保存后立即生效，无需重启。</p>
      </div>
      <div class="row">
        <button class="btn btn-sm" :disabled="loading" @click="load">刷新</button>
        <button class="btn btn-primary btn-sm" :disabled="saving || loading" @click="save">
          {{ saving ? '保存中…' : '保存设置' }}
        </button>
      </div>
    </div>

    <div class="grid cols-2">
      <div class="card">
        <h3 class="card-title">通用</h3>
        <div class="field">
          <label>面板标题</label>
          <input v-model="form.panel_title" class="input" placeholder="模型中转站" />
          <div class="hint">显示在侧边栏与浏览器标题上。</div>
        </div>
        <div class="field">
          <label>系统级 AI 模型</label>
          <select v-model="form.system_model" class="select">
            <option value="">（未指定）</option>
            <option v-for="m in chatModels" :key="m.id" :value="m.name">{{ m.name }}</option>
          </select>
          <div class="hint">「连接器生成器」与「技能生成」会用它，请选择一个文本对话模型。</div>
        </div>
        <div class="field" style="margin-bottom: 0">
          <label>默认落地页</label>
          <select v-model="form.default_page" class="select">
            <option v-for="p in pageOptions" :key="p.value" :value="p.value">{{ p.label }}</option>
          </select>
          <div class="hint">打开面板时自动进入的页面。</div>
        </div>
      </div>

      <div class="card">
        <h3 class="card-title">外观与体验</h3>
        <div class="field">
          <label>主题</label>
          <select v-model="form.theme" class="select">
            <option value="dark">深色</option>
            <option value="light">浅色</option>
            <option value="system">跟随系统</option>
          </select>
          <div class="hint">也可以点右上角的主题按钮快速切换。</div>
        </div>
        <div class="field">
          <label>紧凑模式</label>
          <label class="switch">
            <input v-model="form.compact" type="checkbox" />
            <span class="track" />
            <span>{{ form.compact ? '已开启' : '已关闭' }}</span>
          </label>
          <div class="hint">收紧卡片与列表间距，一屏显示更多内容。</div>
        </div>
        <div class="field">
          <label>危险操作二次确认</label>
          <label class="switch">
            <input v-model="form.confirm_danger" type="checkbox" />
            <span class="track" />
            <span>{{ form.confirm_danger ? '已开启' : '已关闭' }}</span>
          </label>
          <div class="hint">删除渠道、模型、秘钥等操作前先弹窗确认。</div>
        </div>
        <div class="field" style="margin-bottom: 0">
          <label>概览自动刷新间隔（秒）</label>
          <input
            v-model="form.auto_refresh_sec"
            class="input"
            type="number"
            min="0"
            placeholder="0"
          />
          <div class="hint">填 0 表示不自动刷新，仅「概览」页生效。</div>
        </div>
      </div>

      <div class="card">
        <h3 class="card-title">服务端运行参数</h3>
        <p class="muted" style="margin-top: 0">
          改动后立即生效、无需重启服务。输入框留空表示沿用默认值。
        </p>
        <div class="grid cols-2" style="gap: 0 14px">
          <div class="field">
            <label>日志保留条数</label>
            <input
              v-model="form.max_log_rows"
              class="input"
              type="number"
              min="100"
              :placeholder="'默认 ' + def('max_log_rows')"
            />
            <div class="hint">超出后自动清理最旧的记录。</div>
          </div>
          <div class="field">
            <label>视频轮询间隔（秒）</label>
            <input
              v-model="form.video_poll_sec"
              class="input"
              type="number"
              min="3"
              :placeholder="'默认 ' + def('video_poll_sec')"
            />
            <div class="hint">越小越及时，上游请求也越多。</div>
          </div>
          <div class="field">
            <label>SSE 心跳间隔（秒）</label>
            <input
              v-model="form.heartbeat_sec"
              class="input"
              type="number"
              min="0"
              :placeholder="'默认 ' + def('heartbeat_sec')"
            />
            <div class="hint">防止长连接被中间代理掐断，填 0 关闭。</div>
          </div>
          <div class="field">
            <label>流式平滑速率（字符/秒）</label>
            <input
              v-model="form.stream_rate"
              class="input"
              type="number"
              min="5"
              :placeholder="'默认 ' + def('stream_rate')"
            />
            <div class="hint">仅在开启平滑时生效。</div>
          </div>
        </div>
        <div class="field">
          <label>记录请求与响应体</label>
          <label class="switch">
            <input v-model="form.log_body" type="checkbox" />
            <span class="track" />
            <span>{{ form.log_body ? '已开启' : '已关闭（隐私模式）' }}</span>
          </label>
          <div class="hint">关闭后调用日志不再保存请求体与响应预览，适合对隐私敏感的场景。</div>
        </div>
        <div class="field" style="margin-bottom: 0">
          <label>流式输出平滑</label>
          <label class="switch">
            <input v-model="form.stream_smooth" type="checkbox" />
            <span class="track" />
            <span>{{ form.stream_smooth ? '已开启' : '已关闭' }}</span>
          </label>
          <div class="hint">把上游一次吐出的长文本按速率匀速下发，客户端观感更均匀。</div>
        </div>
      </div>

      <div class="card">
        <h3 class="card-title">客户端接入</h3>
        <p class="muted" style="margin-top: 0">任何兼容 OpenAI 的客户端填这两项即可使用：</p>
        <div class="meta-list">
          <dt>Base URL</dt>
          <dd class="mono">{{ baseURL }}</dd>
          <dt>API Key</dt>
          <dd class="mono">在「秘钥」页创建后复制</dd>
        </div>
        <div class="hint" style="margin-top: 16px">
          对外接口包括 /v1/chat/completions、/v1/images/generations、/v1/embeddings 与任务式的 /v1/videos。
        </div>
      </div>

      <div class="card">
        <h3 class="card-title">配置备份</h3>
        <p class="muted" style="margin-top: 0">
          导出全部渠道、模型、秘钥、技能、模型库与设置，便于迁移或留档。
        </p>
        <div class="row">
          <button class="btn" :disabled="backupBusy" @click="exportBackup">导出配置</button>
          <label class="btn" style="cursor: pointer">
            导入配置
            <input type="file" accept="application/json" style="display: none" @change="onImportFile" />
          </label>
        </div>
        <div class="hint" style="margin-top: 12px">
          导入为合并方式：同名的渠道 / 模型 / 秘钥 / 技能会被更新，不会删除现有数据。
        </div>
      </div>
    </div>

    <div class="row" style="margin-top: 18px">
      <div class="spacer" />
      <button class="btn btn-primary" :disabled="saving || loading" @click="save">
        {{ saving ? '保存中…' : '保存设置' }}
      </button>
    </div>
  </div>
</template>
