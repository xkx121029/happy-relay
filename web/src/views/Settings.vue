<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import type { RuntimeOptions, SelfCheck } from '../api'
import { api } from '../api'
import { applyPrefs, models, panelTitle, state, toast } from '../store'

const loading = ref(true)
const saving = ref(false)
const backupBusy = ref(false)
// emailPassSet 表示库里已经存了 SMTP 密码；密码不回传明文，
// 输入框留空 + 这个标记就足以告诉用户「已配置」。
const emailPassSet = ref(false)
const notifying = ref(false)
const checking = ref(false)
const selfCheck = ref<SelfCheck | null>(null)
const showCheck = ref(false)

// 表单统一用字符串保存，便于直接映射到 settings 表的键值；
// 运行参数留空表示沿用环境变量提供的默认值。
const form = reactive({
  panel_title: '',
  system_model: '',
  auto_force_tier: '',
  auto_exclude_models: '',
  auto_hard_keywords: '',
  default_page: '/',
  theme: 'dark',
  compact: false,
  confirm_danger: true,
  auto_refresh_sec: '0',
  max_log_rows: '',
  log_body: true,
  log_body_limit_kb: '',
  stream_smooth: true,
  stream_rate: '',
  heartbeat_sec: '',
  video_poll_sec: '',
  context_policy: 'reject',
  context_safety_percent: '',
  cache_ttl: '',
  request_timeout_sec: '',
  log_retention_days: '',
  alert_enabled: false,
  alert_webhook: '',
  notify_feishu_chat_id: '',
  alert_min_fails: '',
  // ---- 飞书整合（入站 webhook 桥） ----
  bridge_feishu_enabled: false,
  bridge_feishu_transport: 'ws',
  bridge_feishu_app_id: '',
  bridge_feishu_app_secret: '',
  bridge_feishu_encrypt_key: '',
  bridge_feishu_token: '',
  bridge_feishu_mode: 'agent',
  bridge_feishu_model: '',
  bridge_feishu_key: '',
  bridge_feishu_prompt: '',
  bridge_feishu_history: '',
  // ---- 无人值守 ----
  health_enabled: true,
  health_interval: '',
  health_recover: true,
  health_keep: '',
  health_timeout: '',
  report_enabled: false,
  report_hour: '',
  report_weekday: '',
  report_fail_rate: '',
  report_key_days: '',
  balance_mode: 'prefer_healthy',
  balance_bad: '',
  // ---- 无人值守：自动备份 ----
  backup_auto_enabled: false,
  backup_auto_hour: '',
  backup_auto_keep: '',
  email_enabled: false,
  email_host: '',
  email_port: '',
  email_user: '',
  email_pass: '',
  email_from: '',
  email_to: '',
  email_insecure: false,
  public_url: '',
})

const chatModels = computed(() => models.value.filter((m) => m.enabled && m.capability === 'chat'))
const defaults = computed(() => state.meta?.runtime?.defaults)
const effective = computed(() => state.meta?.runtime?.values)

// dirty 用于提示「有改动未保存」，避免用户改完直接关页面。
const snapshot = ref('')
const parsedSnapshot = ref<Record<string, any> | null>(null)
const dirty = computed(() => snapshot.value !== '' && JSON.stringify(form) !== snapshot.value)

// explicitMap 记录哪些 key 在数据库里被显式存过。没显式存过的开关，
// 当前显示的是服务端默认值（可能来自环境变量），给个「默认」标记，
// 避免用户误以为这是自己改过的设置。
const explicitMap = ref<Record<string, boolean>>({})

// inherited 判断某个开关当前值是不是「服务端默认」而非用户显式配置：
// 1) 库里没存过；2) 本地也没改过（form 还等于快照）。
function inherited(key: string): boolean {
  if (explicitMap.value[key]) return false
  if (!parsedSnapshot.value) return true
  return (form as any)[key] === parsedSnapshot.value[key]
}

// dirtySections 列出有未保存改动的分节名，给底部保存条做精确定位提示。
const dirtySections = computed(() => SECTIONS.filter((s) => sectionDirty(s.id)).map((s) => s.label))

function takeSnapshot() {
  snapshot.value = JSON.stringify(form)
  parsedSnapshot.value = JSON.parse(snapshot.value)
}

const pageOptions = [
  { value: '/', label: '概览' },
  { value: '/playground', label: '调试台' },
  { value: '/tasks', label: '任务' },
  { value: '/providers', label: '渠道' },
  { value: '/catalog', label: '模型库' },
  { value: '/models', label: '模型' },
  { value: '/builtin', label: '内置模型' },
  { value: '/keys', label: '秘钥' },
  { value: '/skills', label: '技能' },
  { value: '/connector', label: '连接器生成器' },
  { value: '/logs', label: '调用日志' },
]

// ---------- 左侧分节 ----------
// 每个 section 对应右侧一块内容；id 同时用于「切换」和「未保存改动」的定位。
type SectionId = 'general' | 'appearance' | 'runtime' | 'unattended' | 'alert' | 'backup'

const SECTIONS: { id: SectionId; label: string; desc: string; icon: string }[] = [
  { id: 'general', label: '通用', desc: '面板标题、系统级 AI、默认落地页', icon: '⚙' },
  { id: 'appearance', label: '外观与体验', desc: '主题、密度、交互确认', icon: '◐' },
  { id: 'runtime', label: '运行参数', desc: '日志、流式、上下文与缓存', icon: '⚡' },
  { id: 'unattended', label: '无人值守', desc: '巡检、自动恢复、日报、再平衡与备份', icon: '◈' },
  { id: 'alert', label: '通知与告警', desc: '飞书 / 企业微信 / 邮件通道', icon: '◔' },
  { id: 'backup', label: '备份与接入', desc: '导出导入、客户端连接信息', icon: '⇅' },
]

const activeSection = ref<SectionId>('general')

// 各分节包含的字段名，用于在左侧标出「这一节有未保存改动」。
const SECTION_KEYS: Record<SectionId, string[]> = {
  general: ['panel_title', 'system_model', 'auto_force_tier', 'auto_exclude_models', 'auto_hard_keywords', 'default_page'],
  appearance: ['theme', 'compact', 'confirm_danger', 'auto_refresh_sec'],
  runtime: [
    'max_log_rows',
    'video_poll_sec',
    'heartbeat_sec',
    'stream_rate',
    'stream_smooth',
    'log_body',
    'log_body_limit_kb',
    'context_policy',
    'context_safety_percent',
    'cache_ttl',
    'request_timeout_sec',
    'log_retention_days',
  ],
  alert: [
    'alert_enabled',
    'alert_webhook',
    'notify_feishu_chat_id',
    'alert_min_fails',
    'bridge_feishu_enabled',
    'bridge_feishu_transport',
    'bridge_feishu_app_id',
    'bridge_feishu_app_secret',
    'bridge_feishu_encrypt_key',
    'bridge_feishu_token',
    'bridge_feishu_mode',
    'bridge_feishu_model',
    'bridge_feishu_key',
    'bridge_feishu_prompt',
    'bridge_feishu_history',
    'email_enabled',
    'email_host',
    'email_port',
    'email_user',
    'email_pass',
    'email_from',
    'email_to',
    'email_insecure',
    'public_url',
  ],
  unattended: [
    'health_enabled',
    'health_interval',
    'health_recover',
    'health_keep',
    'health_timeout',
    'report_enabled',
    'report_hour',
    'report_weekday',
    'report_fail_rate',
    'report_key_days',
    'balance_mode',
    'balance_bad',
    'backup_auto_enabled',
    'backup_auto_hour',
    'backup_auto_keep',
  ],
  backup: [],
}

function sectionDirty(id: SectionId): boolean {
  if (!dirty.value) return false
  if (!parsedSnapshot.value) return false
  return SECTION_KEYS[id].some((k) => (form as any)[k] !== parsedSnapshot.value[k])
}

// def 返回某个运行参数在未显式设置时的默认值，用于输入框占位提示。
function def(key: keyof RuntimeOptions): string {
  const v = defaults.value?.[key]
  return v === undefined ? '' : String(v)
}

// resetRuntimeDefaults 把运行参数里的数值输入框清空为「默认」——
// 留空即沿用服务端默认值。只动数值字段，不动开关：开关的「默认」由环境变量
// 决定，无法在客户端安全还原，强行改反而可能覆盖掉用户的环境变量意图。
function resetRuntimeDefaults() {
  form.max_log_rows = ''
  form.log_body_limit_kb = ''
  form.stream_rate = ''
  form.heartbeat_sec = ''
  form.video_poll_sec = ''
  form.context_safety_percent = ''
  form.cache_ttl = ''
  form.request_timeout_sec = ''
  form.log_retention_days = ''
  toast('运行参数数值已清空为默认，记得保存才会生效')
}

async function load() {
  loading.value = true
  try {
    const meta = state.meta || (await api.getMeta())
    state.meta = meta
    // 系统级模型选择用全量列表：已选中的模型即使暂时不可用也要能显示出来。
    const [settings, ms] = await Promise.all([api.getSettings(), api.listModels({ all: true })])
    models.value = ms
    const s = { ...meta.settings, ...settings }
    // 记录哪些 key 是库里显式存过的，用于「默认」标记判定。
    explicitMap.value = Object.fromEntries(Object.keys(settings).map((k) => [k, true]))

    form.panel_title = s.panel_title || ''
    form.system_model = s.system_model || ''
    form.default_page = s.default_page || '/'
    form.auto_force_tier = s.auto_force_tier || ''
    form.auto_exclude_models = s.auto_exclude_models || ''
    form.auto_hard_keywords = s.auto_hard_keywords || ''
    form.theme = s.theme || 'dark'
    form.compact = s.compact === 'on'
    form.confirm_danger = s.confirm_danger !== 'off'
    form.auto_refresh_sec = s.auto_refresh_sec || '0'

    form.max_log_rows = s.max_log_rows || ''
    form.log_body_limit_kb = s.log_body_limit_kb || ''
    form.stream_rate = s.stream_rate || ''
    form.heartbeat_sec = s.heartbeat_sec || ''
    form.video_poll_sec = s.video_poll_sec || ''
    form.context_policy = s.context_policy || 'reject'
    form.context_safety_percent = s.context_safety_percent || ''
    form.cache_ttl = s.cache_ttl || ''
    form.request_timeout_sec = s.request_timeout_sec || ''
    form.log_retention_days = s.log_retention_days || ''
    form.alert_webhook = s.alert_webhook || ''
    form.notify_feishu_chat_id = s.notify_feishu_chat_id || ''
    form.alert_min_fails = s.alert_min_fails || ''
    // ---- 飞书整合 ----
    form.bridge_feishu_enabled = s.bridge_feishu_enabled
      ? s.bridge_feishu_enabled !== 'off'
      : (effective.value?.bridge_feishu_enabled ?? false)
    form.bridge_feishu_transport = s.bridge_feishu_transport || 'ws'
    form.bridge_feishu_app_id = s.bridge_feishu_app_id || ''
    form.bridge_feishu_app_secret = s.bridge_feishu_app_secret || ''
    form.bridge_feishu_encrypt_key = s.bridge_feishu_encrypt_key || ''
    form.bridge_feishu_token = s.bridge_feishu_token || ''
    form.bridge_feishu_mode = s.bridge_feishu_mode || 'agent'
    form.bridge_feishu_model = s.bridge_feishu_model || ''
    form.bridge_feishu_key = s.bridge_feishu_key || ''
    form.bridge_feishu_prompt = s.bridge_feishu_prompt || ''
    form.bridge_feishu_history = s.bridge_feishu_history || ''
    // 开关类没有「留空」状态：没设置过就按当前生效值显示（可能来自环境变量）。
    form.log_body = s.log_body ? s.log_body !== 'off' : (effective.value?.log_body ?? true)
    form.stream_smooth = s.stream_smooth
      ? s.stream_smooth !== 'off'
      : (effective.value?.stream_smooth ?? true)
    form.alert_enabled = s.alert_enabled
      ? s.alert_enabled !== 'off'
      : (effective.value?.alert_enabled ?? false)

    // ---- 无人值守 ----
    form.health_interval = s.health_interval || ''
    form.health_keep = s.health_keep || ''
    form.health_timeout = s.health_timeout || ''
    form.report_hour = s.report_hour || ''
    form.report_weekday = s.report_weekday || ''
    form.report_fail_rate = s.report_fail_rate || ''
    form.report_key_days = s.report_key_days || ''
    form.balance_mode = s.balance_mode || effective.value?.balance_mode || 'prefer_healthy'
    form.balance_bad = s.balance_bad || ''

    // ---- 无人值守：自动备份 ----
    form.backup_auto_hour = s.backup_auto_hour || ''
    form.backup_auto_keep = s.backup_auto_keep || ''
    form.backup_auto_enabled = s.backup_auto_enabled
      ? s.backup_auto_enabled !== 'off'
      : (effective.value?.backup_auto_enabled ?? false)
    form.health_enabled = s.health_enabled
      ? s.health_enabled !== 'off'
      : (effective.value?.health_enabled ?? true)
    form.health_recover = s.health_recover
      ? s.health_recover !== 'off'
      : (effective.value?.health_recover ?? true)
    form.report_enabled = s.report_enabled
      ? s.report_enabled !== 'off'
      : (effective.value?.report_enabled ?? false)

    // ---- 通知渠道 ----
    form.email_host = s.email_host || ''
    form.email_port = s.email_port || ''
    form.email_user = s.email_user || ''
    form.email_from = s.email_from || ''
    form.email_to = s.email_to || ''
    form.public_url = s.public_url || ''
    form.email_enabled = s.email_enabled
      ? s.email_enabled !== 'off'
      : (effective.value?.email_enabled ?? false)
    form.email_insecure = s.email_insecure
      ? s.email_insecure !== 'off'
      : (effective.value?.email_insecure ?? false)
    emailPassSet.value = effective.value?.has_email_pass ?? false
    form.email_pass = ''

    panelTitle.value = form.panel_title || '模型中转站'
    document.title = panelTitle.value
    takeSnapshot()
  } catch (e: any) {
    toast(e.message || '加载失败', 'error')
  } finally {
    loading.value = false
  }
}

onMounted(load)
onMounted(loadFeishuStatus)

// Ctrl/Cmd+S 直接保存：设置页字段多，用键盘保存比挪鼠标去点按钮顺手。
// 仅在本页挂载期间监听，离开页面即解除，不影响其它页面。
function onKeydown(e: KeyboardEvent) {
  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's') {
    e.preventDefault()
    if (dirty.value && !saving.value && !loading.value) save()
  }
}
onMounted(() => window.addEventListener('keydown', onKeydown))
onUnmounted(() => window.removeEventListener('keydown', onKeydown))

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
      auto_force_tier: form.auto_force_tier || '',
      auto_exclude_models: form.auto_exclude_models.trim(),
      auto_hard_keywords: form.auto_hard_keywords.trim(),
      default_page: form.default_page,
      theme: form.theme,
      compact: form.compact ? 'on' : 'off',
      confirm_danger: form.confirm_danger ? 'on' : 'off',
      auto_refresh_sec: numStr(form.auto_refresh_sec),
      max_log_rows: numStr(form.max_log_rows),
      log_body: form.log_body ? 'on' : 'off',
      log_body_limit_kb: numStr(form.log_body_limit_kb),
      stream_smooth: form.stream_smooth ? 'on' : 'off',
      stream_rate: numStr(form.stream_rate),
      heartbeat_sec: numStr(form.heartbeat_sec),
      video_poll_sec: numStr(form.video_poll_sec),
      context_policy: form.context_policy,
      context_safety_percent: numStr(form.context_safety_percent),
      cache_ttl: numStr(form.cache_ttl),
      request_timeout_sec: numStr(form.request_timeout_sec),
      log_retention_days: numStr(form.log_retention_days),
      alert_enabled: form.alert_enabled ? 'on' : 'off',
      alert_webhook: form.alert_webhook.trim(),
      notify_feishu_chat_id: form.notify_feishu_chat_id.trim(),
      alert_min_fails: numStr(form.alert_min_fails),

      // ---- 飞书整合 ----
      bridge_feishu_enabled: form.bridge_feishu_enabled ? 'on' : 'off',
      bridge_feishu_transport: form.bridge_feishu_transport || 'ws',
      bridge_feishu_app_id: form.bridge_feishu_app_id.trim(),
      bridge_feishu_app_secret: form.bridge_feishu_app_secret.trim(),
      bridge_feishu_encrypt_key: form.bridge_feishu_encrypt_key.trim(),
      bridge_feishu_token: form.bridge_feishu_token.trim(),
      bridge_feishu_mode: form.bridge_feishu_mode,
      bridge_feishu_model: form.bridge_feishu_model.trim(),
      bridge_feishu_key: form.bridge_feishu_key.trim(),
      bridge_feishu_prompt: form.bridge_feishu_prompt,
      bridge_feishu_history: numStr(form.bridge_feishu_history),

      // ---- 无人值守 ----
      health_enabled: form.health_enabled ? 'on' : 'off',
      health_interval: numStr(form.health_interval),
      health_recover: form.health_recover ? 'on' : 'off',
      health_keep: numStr(form.health_keep),
      health_timeout: numStr(form.health_timeout),
      report_enabled: form.report_enabled ? 'on' : 'off',
      report_hour: numStr(form.report_hour),
      report_weekday: form.report_weekday || '',
      report_fail_rate: numStr(form.report_fail_rate),
      report_key_days: numStr(form.report_key_days),
      balance_mode: form.balance_mode,
      balance_bad: numStr(form.balance_bad),

      // ---- 无人值守：自动备份 ----
      backup_auto_enabled: form.backup_auto_enabled ? 'on' : 'off',
      backup_auto_hour: numStr(form.backup_auto_hour),
      backup_auto_keep: numStr(form.backup_auto_keep),

      // ---- 通知渠道 ----
      email_enabled: form.email_enabled ? 'on' : 'off',
      email_host: form.email_host.trim(),
      email_port: numStr(form.email_port),
      email_user: form.email_user.trim(),
      email_from: form.email_from.trim(),
      email_to: form.email_to.trim(),
      public_url: form.public_url.trim(),
      email_insecure: form.email_insecure ? 'on' : 'off',
    }
    // 密码留空表示不改动：直接下发空串会把已有密码清掉，那是灾难。
    const pass = form.email_pass.trim()
    if (pass) payload.email_pass = pass
    const saved = await api.saveSettings(payload)
    // 重新拉取元信息，让「当前生效值」与默认值提示保持准确。
    const meta = await api.getMeta()
    state.meta = meta
    // 保存后这些 key 都进了库，不再需要「默认」标记。
    explicitMap.value = Object.fromEntries(Object.keys(payload).map((k) => [k, true]))
    applyPrefs({ ...meta.settings, ...saved })
    panelTitle.value = form.panel_title.trim() || '模型中转站'
    document.title = panelTitle.value
    takeSnapshot()
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

// runCheck 执行一次配置自省，把结论摊在设置页里。
// 不做缓存：用户点它就是为了看「现在」的状态。
async function runCheck() {
  checking.value = true
  try {
    selfCheck.value = await api.selfCheck()
    showCheck.value = true
  } catch (e: any) {
    toast(e.message || '自检失败', 'error')
  } finally {
    checking.value = false
  }
}

// testNotify 发送一条测试通知。必须先保存，否则服务端读到的还是旧配置。
const bridgeTesting = ref(false)
const bridgeWebhookURL = `${location.origin}/webhook/feishu`

// testBridge 校验飞书应用凭据（App ID/Secret 能否换取应用 token）。必须先保存。
async function testBridge() {
  bridgeTesting.value = true
  try {
    await api.bridgeFeishuTest()
    toast('飞书应用凭据校验通过 ✅ 事件订阅回调已可用', 'info')
  } catch (e: any) {
    toast(e.message || '校验失败：请检查 App ID / App Secret 并先保存', 'error')
  } finally {
    bridgeTesting.value = false
  }
}
// ---- 飞书配置向导：实时链路状态（GET /api/bridge/feishu/status，读的是已保存配置）----
const feishuStatus = ref<any>(null)
const feishuStatusLoading = ref(false)

async function loadFeishuStatus() {
  feishuStatusLoading.value = true
  try {
    feishuStatus.value = await api.bridgeFeishuStatus()
  } catch {
    feishuStatus.value = null
  } finally {
    feishuStatusLoading.value = false
  }
}

// gotoField 平滑滚动到指定字段并短暂高亮。
function gotoField(id: string) {
  const el = document.getElementById(id)
  if (!el) return
  el.scrollIntoView({ behavior: 'smooth', block: 'center' })
  el.classList.remove('flash-target')
  // 强制重排后再加类，保证连续点击同一目标也能重新闪一下。
  void el.offsetWidth
  el.classList.add('flash-target')
  setTimeout(() => el.classList.remove('flash-target'), 1600)
}

// feishuSteps 把链路状态翻译成向导步骤；done 由服务端实时状态判定，不是表单里的值。
const feishuSteps = computed(() => {
  const st = feishuStatus.value
  if (!st) return []
  const steps: { title: string; desc: string; done: boolean; optional?: boolean; target?: string }[] = [
    {
      title: '启用整合',
      desc: st.enabled ? '整合已开启' : '打开「启用飞书整合」开关',
      done: !!st.enabled,
      target: 'f-feishu-enable',
    },
    {
      title: '应用凭据',
      desc: st.credentials_ok
        ? 'App ID / Secret 有效，可换取应用身份'
        : '填入 App ID 与 App Secret（需先保存，可点下方「校验应用凭据」）',
      done: !!st.credentials_ok,
      target: 'f-feishu-app',
    },
    {
      title: '事件接入',
      desc:
        st.transport === 'ws'
          ? st.ws_connected
            ? '长连接已建立，实时接收飞书消息'
            : '长连接未建立：保存配置并保持服务运行，稍后自动重连'
          : '回调 URL 模式：需公网可达并已在飞书开放平台配置',
      done: st.transport === 'ws' ? !!st.ws_connected : !!st.credentials_ok,
      target: 'f-feishu-transport',
    },
    {
      title: '回复通道',
      desc:
        st.mode === 'agent'
          ? st.agent_ready
            ? 'Agent 模式就绪：主模型可指挥面板查配置 / 建模型 / 改设置'
            : 'Agent 模式需先在「通用」里选择系统级 AI 模型'
          : st.chat_ready
            ? 'Chat 模式就绪：回环链路已配置'
            : 'Chat 模式需选择回复模型，或站点存在至少一把启用中的秘钥',
      done: st.mode === 'agent' ? !!st.agent_ready : !!st.chat_ready,
      target: 'f-feishu-mode',
    },
    {
      title: '告警 / 日报推送（可选）',
      desc: st.notify_chat_id
        ? `告警与周报卡片直推会话 ${String(st.notify_chat_id).slice(0, 14)}…`
        : '填「飞书应用推送目标 chat_id」后，异常告警与每周状态卡片会直推到你的飞书',
      done: !!st.notify_chat_id,
      optional: true,
      target: 'f-feishu-notify',
    },
  ]
  return steps
})

async function testNotify() {
  if (dirty.value) {
    toast('请先保存设置，测试会使用已保存的配置', 'error')
    return
  }
  notifying.value = true
  try {
    const res = await api.testNotify()
    toast(`测试通知已发送：${(res.channels || []).join('、') || '未知通道'}`)
  } catch (e: any) {
    toast(e.message || '测试通知发送失败', 'error')
  } finally {
    notifying.value = false
  }
}

// checkIcon / checkClass 把自检等级映射成视觉标记。
function checkIcon(level: string): string {
  if (level === 'error') return '✕'
  if (level === 'warn') return '!'
  return '✓'
}
function checkClass(level: string): string {
  if (level === 'error') return 'danger'
  if (level === 'warn') return 'warn'
  return 'ok'
}

const checkSummary = computed(() => {
  const c = selfCheck.value
  if (!c) return ''
  if (c.error_count === 0 && c.warn_count === 0) return '配置正常，未发现问题'
  const parts: string[] = []
  if (c.error_count) parts.push(`${c.error_count} 个问题`)
  if (c.warn_count) parts.push(`${c.warn_count} 个提醒`)
  return '发现 ' + parts.join('、')
})
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h2>设置</h2>
        <p>面板偏好与服务端运行参数，保存后立即生效，无需重启。</p>
      </div>
      <div class="row">
        <span v-if="dirty" class="badge warn">有未保存的改动</span>
        <button class="btn btn-sm" :disabled="loading" @click="load">刷新</button>
        <button
          class="btn btn-primary btn-sm"
          :class="{ 'is-loading': saving }"
          :disabled="saving || loading"
          @click="save"
        >
          {{ saving ? '保存中…' : '保存设置' }}
        </button>
      </div>
    </div>

    <div class="settings-layout">
      <!-- 左侧：分节导航（窄屏时退化成横向可滚胶囊条） -->
      <div class="rail-scroll">
        <nav class="settings-rail">
          <button
            v-for="s in SECTIONS"
            :key="s.id"
            type="button"
            class="rail-item"
            :class="{ active: activeSection === s.id }"
            @click="activeSection = s.id"
          >
            <span class="rail-icon" aria-hidden="true">{{ s.icon }}</span>
            <span class="rail-text">
              <span class="rail-label">
                {{ s.label }}
                <!-- 该节有未保存改动时点一个小圆点，不用来回翻 -->
                <span v-if="sectionDirty(s.id)" class="rail-dot" title="这一节有未保存的改动" />
              </span>
              <span class="rail-desc">{{ s.desc }}</span>
            </span>
          </button>
        </nav>
      </div>

      <!-- 右侧：当前分节的设置内容 -->
      <div class="settings-panel">
        <!-- 通用 -->
        <section v-show="activeSection === 'general'" class="card">
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
          <div class="subhead">auto 路由规则</div>
          <div class="field">
            <label>强制档位（留空 = 按消息难度自动评估）</label>
            <select v-model="form.auto_force_tier" class="select">
              <option value="">跟随难度评估（推荐）</option>
              <option value="light">强制 light · 轻量档</option>
              <option value="standard">强制 standard · 标准档</option>
              <option value="pro">强制 pro · 旗舰档</option>
            </select>
            <div class="hint">强制后所有 model=auto 的请求都优先走该档位，不再做难度评估（候选失败仍会向相邻档位回退）。</div>
          </div>
          <div class="grid-2">
            <div class="field">
              <label>排除模型（逗号分隔）</label>
              <input
                v-model="form.auto_exclude_models"
                class="input"
                placeholder="如：m-light,实验模型"
              />
              <div class="hint">这些模型不会出现在 auto 候选里（大小写不敏感）。</div>
            </div>
            <div class="field">
              <label>提档关键词（逗号分隔）</label>
              <input
                v-model="form.auto_hard_keywords"
                class="input"
                placeholder="如：毛肚,部署,复盘"
              />
              <div class="hint">消息命中即加分提档，与内置关键词（代码/算法/重构等）同权。</div>
            </div>
          </div>
          <div class="field" style="margin-bottom: 0">
            <label>默认落地页</label>
            <select v-model="form.default_page" class="select">
              <option v-for="p in pageOptions" :key="p.value" :value="p.value">{{ p.label }}</option>
            </select>
            <div class="hint">打开面板时自动进入的页面。</div>
          </div>
        </section>

        <!-- 外观与体验 -->
        <section v-show="activeSection === 'appearance'" class="card">
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
        </section>

        <!-- 运行参数 -->
        <section v-show="activeSection === 'runtime'" class="card">
          <h3 class="card-title flex-title">
            <span>服务端运行参数</span>
            <button class="btn btn-sm btn-ghost" type="button" @click="resetRuntimeDefaults">数值清空为默认</button>
          </h3>
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
            <label>记录请求与响应体<span v-if="inherited('log_body')" class="tag-default" title="未单独配置，当前状态来自服务端默认值">默认</span></label>
            <label class="switch">
              <input v-model="form.log_body" type="checkbox" />
              <span class="track" />
              <span>{{ form.log_body ? '已开启' : '已关闭（隐私模式）' }}</span>
            </label>
            <div class="hint">关闭后调用日志不再保存请求体与响应预览，适合对隐私敏感的场景。</div>
          </div>
          <div class="field">
            <label>单条日志体积上限（KB）</label>
            <input
              v-model="form.log_body_limit_kb"
              class="input"
              type="number"
              min="1"
              :placeholder="'默认 ' + def('log_body_limit_kb')"
            />
            <div class="hint">请求体与响应体（含流式完整正文）各自超出该大小时截断保存，防止长文对话撑爆数据库。</div>
          </div>
          <div class="field">
            <label>流式输出平滑<span v-if="inherited('stream_smooth')" class="tag-default" title="未单独配置，当前状态来自服务端默认值">默认</span></label>
            <label class="switch">
              <input v-model="form.stream_smooth" type="checkbox" />
              <span class="track" />
              <span>{{ form.stream_smooth ? '已开启' : '已关闭' }}</span>
            </label>
            <div class="hint">把上游一次吐出的长文本按速率匀速下发，客户端观感更均匀。</div>
          </div>

          <div class="grid cols-2" style="gap: 0 14px">
            <div class="field">
              <label>上下文超限策略</label>
              <select v-model="form.context_policy" class="select">
                <option value="reject">直接报错（推荐）</option>
                <option value="truncate">自动裁剪最早的历史</option>
                <option value="off">不校验</option>
              </select>
              <div class="hint">按估算输入长度与模型上下文窗口判断，估算值偏保守。</div>
            </div>
            <div class="field">
              <label>上下文安全系数（%）</label>
              <input
                v-model="form.context_safety_percent"
                class="input"
                type="number"
                min="50"
                max="100"
                :placeholder="'默认 ' + def('context_safety_percent')"
              />
              <div class="hint">只把窗口的该比例用于输入，其余留给输出与估算误差。</div>
            </div>
          </div>

          <div class="field">
            <label>响应缓存 TTL（秒）</label>
            <input
              v-model="form.cache_ttl"
              class="input"
              type="number"
              min="0"
              :placeholder="'默认 ' + def('cache_ttl')"
            />
            <div class="hint">非流式、非 auto 的相同请求会直接命中缓存；填 0 关闭。</div>
          </div>

          <div class="grid cols-2" style="gap: 0 14px; margin-bottom: 0">
            <div class="field">
              <label>全局上游请求超时（秒）</label>
              <input
                v-model="form.request_timeout_sec"
                class="input"
                type="number"
                min="10"
                :placeholder="'默认 ' + def('request_timeout_sec')"
              />
              <div class="hint">渠道未单独配置超时时生效。流式长回答建议保持较大值，避免被提前掐断。</div>
            </div>
            <div class="field">
              <label>日志保留天数</label>
              <input
                v-model="form.log_retention_days"
                class="input"
                type="number"
                min="0"
                :placeholder="'默认 ' + def('log_retention_days')"
              />
              <div class="hint">超过天数的旧日志自动删除；填 0 表示不按天清理（仍受条数上限约束）。</div>
            </div>
          </div>
        </section>

        <!-- 无人值守 -->
        <section v-show="activeSection === 'unattended'" class="card">
          <h3 class="card-title">无人值守</h3>
          <p class="muted" style="margin-top: 0">
            让中转站自己发现问题、自己恢复、定期汇报。这些开关打开后，不需要盯着面板。
          </p>

          <div class="subhead">健康巡检</div>
          <div class="field">
            <label>启用后台巡检<span v-if="inherited('health_enabled')" class="tag-default" title="未单独配置，当前状态来自服务端默认值">默认</span></label>
            <label class="switch">
              <input v-model="form.health_enabled" type="checkbox" />
              <span class="track" />
              <span>{{ form.health_enabled ? '已开启' : '已关闭' }}</span>
            </label>
            <div class="hint">
              定期探测每个启用渠道的模型列表接口。关闭后，只能靠真实调用失败才发现渠道挂了。
            </div>
          </div>
          <div class="grid-2">
            <div class="field">
              <label>巡检间隔（秒）</label>
              <input
                v-model="form.health_interval"
                class="input"
                type="number"
                min="30"
                :disabled="!form.health_enabled"
                :placeholder="'默认 ' + def('health_interval')"
              />
              <div class="hint">每渠道一次请求，别设太短以免浪费配额。</div>
            </div>
            <div class="field">
              <label>单次巡检超时（秒）</label>
              <input
                v-model="form.health_timeout"
                class="input"
                type="number"
                min="3"
                :disabled="!form.health_enabled"
                :placeholder="'默认 ' + def('health_timeout')"
              />
              <div class="hint">超过这个时间未响应即判为失败。</div>
            </div>
          </div>
          <div class="field">
            <label>自动清除熔断<span v-if="inherited('health_recover')" class="tag-default" title="未单独配置，当前状态来自服务端默认值">默认</span></label>
            <label class="switch">
              <input v-model="form.health_recover" type="checkbox" :disabled="!form.health_enabled" />
              <span class="track" />
              <span>{{ form.health_recover ? '已开启' : '已关闭' }}</span>
            </label>
            <div class="hint">
              巡检发现渠道恢复后立即解锁，不必等熔断冷却走完，也不用人工点「重置」。
            </div>
          </div>
          <div class="field" style="margin-bottom: 0">
            <label>每渠道保留巡检记录</label>
            <input
              v-model="form.health_keep"
              class="input"
              type="number"
              min="20"
              :disabled="!form.health_enabled"
              :placeholder="'默认 ' + def('health_keep')"
            />
            <div class="hint">超出上限的老记录会被自动裁剪，避免数据库膨胀。</div>
          </div>

          <div class="subhead">日报与异常预警</div>
          <div class="field">
            <label>推送每日用量日报<span v-if="inherited('report_enabled')" class="tag-default" title="未单独配置，当前状态来自服务端默认值">默认</span></label>
            <label class="switch">
              <input v-model="form.report_enabled" type="checkbox" />
              <span class="track" />
              <span>{{ form.report_enabled ? '已开启' : '已关闭' }}</span>
            </label>
            <div class="hint">汇总昨日的调用量、失败率、渠道表现与巡检可用率。</div>
          </div>
          <div class="field">
            <label>推送频率</label>
            <select v-model="form.report_weekday" class="select" :disabled="!form.report_enabled">
              <option value="">每天推送</option>
              <option value="1">每周一（周报）</option>
              <option value="2">每周二</option>
              <option value="3">每周三</option>
              <option value="4">每周四</option>
              <option value="5">每周五</option>
              <option value="6">每周六</option>
              <option value="0">每周日</option>
            </select>
            <div class="hint">选「每周一」即每周一早上按推送时刻发一次系统状态卡片。</div>
          </div>
          <div class="grid-3">
            <div class="field">
              <label>推送时刻（0-23）</label>
              <input
                v-model="form.report_hour"
                class="input"
                type="number"
                min="0"
                max="23"
                :disabled="!form.report_enabled"
                :placeholder="'默认 ' + def('report_hour')"
              />
            </div>
            <div class="field">
              <label>失败率预警线（%）</label>
              <input
                v-model="form.report_fail_rate"
                class="input"
                type="number"
                min="0"
                max="100"
                :disabled="!form.report_enabled"
                :placeholder="'默认 ' + def('report_fail_rate')"
              />
            </div>
            <div class="field">
              <label>秘钥到期提醒（天）</label>
              <input
                v-model="form.report_key_days"
                class="input"
                type="number"
                min="0"
                :disabled="!form.report_enabled"
                :placeholder="'默认 ' + def('report_key_days')"
              />
            </div>
          </div>
          <div class="hint" style="margin-bottom: 18px">
            失败率与秘钥两项填 0 表示关闭对应提醒；预警线只对当天调用超过 20 次的渠道生效，
            避免小样本误报。
          </div>

          <div class="subhead">负载再平衡</div>
          <div class="grid-2">
            <div class="field">
              <label>排序依据</label>
              <select v-model="form.balance_mode" class="select">
                <option value="off">关闭（按配置顺序）</option>
                <option value="prefer_healthy">优先可用率（默认）</option>
                <option value="prefer_fast">优先延迟</option>
              </select>
              <div class="hint">多个上游/渠道时，按近期表现在运行期动态调整尝试顺序。</div>
            </div>
            <div class="field">
              <label>劣质渠道阈值（%）</label>
              <input
                v-model="form.balance_bad"
                class="input"
                type="number"
                min="0"
                max="100"
                :disabled="form.balance_mode === 'off'"
                :placeholder="'默认 ' + def('balance_bad')"
              />
              <div class="hint">可用率低于此值的渠道会被排到候选末尾。</div>
            </div>
          </div>
          <div class="hint notice-inline">
            「优先可用率」不会让 100% 与 98% 的渠道互相颠倒——只有掉到阈值以下的渠道才被降权，
            避免调度抖动导致响应缓存反复失效。
          </div>

          <div class="subhead">自动备份</div>
          <div class="field">
            <label>每日自动备份配置<span v-if="inherited('backup_auto_enabled')" class="tag-default" title="未单独配置，当前状态来自服务端默认值">默认</span></label>
            <label class="switch">
              <input v-model="form.backup_auto_enabled" type="checkbox" />
              <span class="track" />
              <span>{{ form.backup_auto_enabled ? '已开启' : '已关闭' }}</span>
            </label>
            <div class="hint">
              每天把渠道、模型、秘钥、技能与设置导出到数据目录 backups/ 下，误删、迁移都有底可抄。
            </div>
          </div>
          <div class="grid-2">
            <div class="field">
              <label>备份时刻（0-23）</label>
              <input
                v-model="form.backup_auto_hour"
                class="input"
                type="number"
                min="0"
                max="23"
                :disabled="!form.backup_auto_enabled"
                :placeholder="'默认 ' + def('backup_auto_hour')"
              />
            </div>
            <div class="field">
              <label>保留份数</label>
              <input
                v-model="form.backup_auto_keep"
                class="input"
                type="number"
                min="1"
                :disabled="!form.backup_auto_enabled"
                :placeholder="'默认 ' + def('backup_auto_keep')"
              />
              <div class="hint">超出份数的旧备份自动删除，不会撑爆磁盘。</div>
            </div>
          </div>

          <div class="subhead">配置自省</div>
          <div class="row">
            <button class="btn" :class="{ 'is-loading': checking }" :disabled="checking" @click="runCheck">
              {{ checking ? '检查中…' : '运行一次自检' }}
            </button>
            <span v-if="selfCheck" class="muted" style="font-size: 12.5px">{{ checkSummary }}</span>
          </div>
          <div v-if="showCheck && selfCheck" class="checklist">
            <div
              v-for="it in selfCheck.items"
              :key="it.id"
              class="check-item"
              :class="checkClass(it.level)"
            >
              <span class="check-icon">{{ checkIcon(it.level) }}</span>
              <div>
                <div class="check-title">{{ it.title }}</div>
                <div v-if="it.detail" class="check-detail">{{ it.detail }}</div>
                <div v-if="it.hint" class="check-hint">→ {{ it.hint }}</div>
              </div>
            </div>
          </div>
          <div class="hint" style="margin-top: 12px">
            同样的检查在服务启动时也会跑一次，结论直接写进日志。
          </div>
        </section>

        <!-- 通知与告警 -->
        <section v-show="activeSection === 'alert'" class="card">
          <h3 class="card-title">通知与告警</h3>
          <p class="muted" style="margin-top: 0">
            渠道失败、持续不可用、每日日报都走这里配置的通道。Webhook 与邮件可以同时开，互不影响。
          </p>
          <div class="field">
            <label>启用失败告警<span v-if="inherited('alert_enabled')" class="tag-default" title="未单独配置，当前状态来自服务端默认值">默认</span></label>
            <label class="switch">
              <input v-model="form.alert_enabled" type="checkbox" />
              <span class="track" />
              <span>{{ form.alert_enabled ? '已开启' : '已关闭' }}</span>
            </label>
            <div class="hint">总开关。关闭时下面所有通道都不会收到失败告警。</div>
          </div>
          <div class="field">
            <label>告警 Webhook（飞书 / 企业微信 / 钉钉）</label>
            <input
              v-model="form.alert_webhook"
              class="input"
              :disabled="!form.alert_enabled"
              placeholder="https://open.feishu.cn/open-apis/bot/v2/hook/..."
            />
            <div class="hint">粘贴群机器人 Webhook；代码按域名自动选择消息格式。</div>
          </div>
          <div class="field" id="f-feishu-notify">
            <label>飞书应用推送目标 chat_id（可选）</label>
            <input
              v-model="form.notify_feishu_chat_id"
              class="input code"
              :disabled="!form.alert_enabled"
              placeholder="oc_xxxxxxxx"
            />
            <div class="hint">
              复用「飞书整合」的应用凭据，把告警与日报以卡片直推到该会话（无需群机器人）。
              私聊机器人发条消息，服务日志 [feishu-bridge] 会打印 chat_id。
            </div>
          </div>
          <div class="field">
            <label>连续失败次数才告警</label>
            <input
              v-model="form.alert_min_fails"
              class="input"
              type="number"
              min="1"
              :disabled="!form.alert_enabled"
              :placeholder="'默认 ' + def('alert_min_fails')"
            />
            <div class="hint">按模型 5 分钟窗口统计，避免偶发抖动刷屏。</div>
          </div>

          <div class="subhead">飞书深度整合（入站）</div>
          <p class="muted" style="margin-top: 0">
            让飞书消息直达中继：私聊机器人、或在群里 @机器人 说话，它会回复你。
            回复可「回环」走本站对外模型（路由/熔断/日志全记账），也可直接调用站内 Agent（主模型）指挥面板改配置。
          </p>
          <!-- 配置向导：缺哪一步一目了然，直接跳转 -->
          <div class="feishu-wizard" v-if="feishuStatus">
            <div class="fw-head">
              <span class="fw-title">配置向导</span>
              <span class="muted" style="font-size: 12px">链路状态实时来自服务端（已保存的配置）</span>
              <div class="spacer" />
              <button class="btn btn-ghost btn-sm" :disabled="feishuStatusLoading" @click="loadFeishuStatus">
                <span v-if="feishuStatusLoading" class="spinner" />刷新
              </button>
            </div>
            <div class="fw-steps">
              <div
                v-for="s in feishuSteps"
                :key="s.title"
                class="fw-step"
                :class="{ done: s.done, warn: !s.done && !s.optional, optional: !s.done && s.optional }"
              >
                <span class="fw-dot">{{ s.done ? '✓' : s.optional ? '!' : '✕' }}</span>
                <div class="fw-body">
                  <div class="fw-name">{{ s.title }}</div>
                  <div class="fw-desc">{{ s.desc }}</div>
                </div>
                <a v-if="!s.done && s.target" class="fw-jump" @click.prevent="gotoField(s.target!)">去配置 →</a>
              </div>
            </div>
            <div
              v-if="feishuStatus.transport === 'ws' && !feishuStatus.ws_connected && feishuStatus.ws_last_error"
              class="fw-err"
            >
              长连接最近一次错误：{{ feishuStatus.ws_last_error }}
            </div>
          </div>
          <div class="field" id="f-feishu-enable">
            <label>启用飞书整合</label>
            <label class="switch">
              <input v-model="form.bridge_feishu_enabled" type="checkbox" />
              <span class="track" />
              <span>{{ form.bridge_feishu_enabled ? '已开启' : '已关闭' }}</span>
            </label>
            <div class="hint">
              前置条件：在飞书开放平台创建「企业自建应用」，开通 im:message 收发权限，
              订阅 im.message.receive_v1 事件。
              推荐用「长连接」接收事件：无需公网 IP/域名、无需加解密配置，保存后自动建连。
            </div>
          </div>
          <div class="field" id="f-feishu-transport">
            <label>事件接入方式</label>
            <select v-model="form.bridge_feishu_transport" class="select" :disabled="!form.bridge_feishu_enabled">
              <option value="ws">长连接（推荐，无需公网）</option>
              <option value="webhook">回调 URL 推送（需公网可达）</option>
            </select>
            <div class="hint">
              长连接：在飞书「事件与回调 → 事件配置」把订阅方式改为「使用长连接接收事件」即可（需先保存本页并保持服务运行）。
            </div>
          </div>
          <div class="field" v-if="form.bridge_feishu_transport === 'webhook'">
            <label>事件订阅回调地址（复制到飞书开放平台）</label>
            <input class="input code" :value="bridgeWebhookURL" readonly @focus="($event.target as HTMLInputElement).select()" />
            <div class="hint">面板必须能被飞书服务器访问到（公网或内网穿透）。长连接模式下此地址不需要。</div>
          </div>
          <div class="grid-2">
            <div class="field">
              <label>App ID</label>
              <input
                v-model="form.bridge_feishu_app_id"
                class="input code"
                :disabled="!form.bridge_feishu_enabled"
                placeholder="cli_xxx"
              />
            </div>
            <div class="field">
              <label>App Secret</label>
              <input
                v-model="form.bridge_feishu_app_secret"
                class="input"
                type="password"
                autocomplete="new-password"
                :disabled="!form.bridge_feishu_enabled"
                placeholder="应用凭据 Secret"
              />
            </div>
          </div>
          <div class="grid-2">
            <div class="field">
              <label>Encrypt Key（可选）</label>
              <input
                v-model="form.bridge_feishu_encrypt_key"
                class="input"
                :disabled="!form.bridge_feishu_enabled"
                placeholder="事件加密密钥，留空不加密"
              />
            </div>
            <div class="field">
              <label>Verification Token（可选）</label>
              <input
                v-model="form.bridge_feishu_token"
                class="input"
                :disabled="!form.bridge_feishu_enabled"
                placeholder="校验 Token，留空不校验"
              />
            </div>
          </div>
          <div class="grid-2">
            <div class="field">
              <label>回复模式</label>
              <select v-model="form.bridge_feishu_mode" class="select" :disabled="!form.bridge_feishu_enabled">
                <option value="agent">Agent 模式 · 主模型指挥面板（查配置 / 建模型 / 改设置）</option>
                <option value="chat">Chat 模式 · 回环走对外模型对话</option>
              </select>
              <div class="hint">Agent 模式使用「通用」里的系统级 AI 模型；Chat 模式走完整回环链路。</div>
            </div>
            <div class="field">
              <label>飞书回复模型（Chat 模式）</label>
              <select
                v-model="form.bridge_feishu_model"
                class="select"
                :disabled="!form.bridge_feishu_enabled || form.bridge_feishu_mode !== 'chat'"
              >
                <option value="">选择对外模型</option>
                <option value="auto">auto · 自动选型（按消息难度挑模型）</option>
                <option v-for="m in chatModels" :key="m.name" :value="m.name">{{ m.name }}</option>
              </select>
              <div class="hint">
                选 auto：每条消息按难度评估（长度 / 图片 / 复杂关键词）自动挑合适档位的模型，
                首个候选失败自动换下一个，整条候选链走完才报错。
              </div>
            </div>
          </div>
          <div class="grid-2">
            <div class="field">
              <label>回环秘钥（Chat 模式，可选）</label>
              <input
                v-model="form.bridge_feishu_key"
                class="input code"
                :disabled="!form.bridge_feishu_enabled || form.bridge_feishu_mode !== 'chat'"
                placeholder="留空自动取第一把启用中的秘钥"
              />
            </div>
            <div class="field">
              <label>附加系统提示词（Chat 模式，可选）</label>
              <input
                v-model="form.bridge_feishu_prompt"
                class="input"
                :disabled="!form.bridge_feishu_enabled || form.bridge_feishu_mode !== 'chat'"
                placeholder="例如：你是运维助手，回答保持简短"
              />
            </div>
          </div>
          <div class="field">
            <label>会话上下文轮数（0 = 每条消息独立，不带记忆）</label>
            <input
              v-model="form.bridge_feishu_history"
              class="input"
              type="number"
              min="0"
              max="25"
              :disabled="!form.bridge_feishu_enabled"
              placeholder="默认 10"
            />
            <div class="hint">机器人记住同一会话最近 N 轮对话，可连续追问；30 分钟没说话自动清空记忆。</div>
          </div>
          <div class="field">
            <button class="btn" :disabled="bridgeTesting || !form.bridge_feishu_enabled" @click="testBridge">
              <span v-if="bridgeTesting" class="spinner" />校验应用凭据
            </button>
            <div class="hint">先保存再校验：验证 App ID / Secret 能否正确换取应用身份凭据。</div>
          </div>

          <div class="subhead">邮件通道</div>
          <div class="field">
            <label>启用邮件通知<span v-if="inherited('email_enabled')" class="tag-default" title="未单独配置，当前状态来自服务端默认值">默认</span></label>
            <label class="switch">
              <input v-model="form.email_enabled" type="checkbox" />
              <span class="track" />
              <span>{{ form.email_enabled ? '已开启' : '已关闭' }}</span>
            </label>
            <div class="hint">SMTP 直发，不依赖第三方推送服务。</div>
          </div>
          <div class="grid-2">
            <div class="field">
              <label>SMTP 服务器</label>
              <input
                v-model="form.email_host"
                class="input"
                :disabled="!form.email_enabled"
                placeholder="smtp.qq.com"
              />
            </div>
            <div class="field">
              <label>端口</label>
              <input
                v-model="form.email_port"
                class="input"
                type="number"
                :disabled="!form.email_enabled"
                :placeholder="'默认 ' + def('email_port')"
              />
              <div class="hint">465 走 SSL，其余端口走 STARTTLS。</div>
            </div>
          </div>
          <div class="grid-2">
            <div class="field">
              <label>账号</label>
              <input
                v-model="form.email_user"
                class="input"
                :disabled="!form.email_enabled"
                placeholder="you@qq.com"
              />
            </div>
            <div class="field">
              <label>密码 / 授权码</label>
              <input
                v-model="form.email_pass"
                class="input"
                type="password"
                autocomplete="new-password"
                :disabled="!form.email_enabled"
                :placeholder="emailPassSet ? '已配置（留空则不改动）' : 'SMTP 授权码，不是登录密码'"
              />
            </div>
          </div>
          <div class="grid-2">
            <div class="field">
              <label>发件人（可选）</label>
              <input
                v-model="form.email_from"
                class="input"
                :disabled="!form.email_enabled"
                placeholder="留空则用账号"
              />
            </div>
            <div class="field">
              <label>收件人</label>
              <input
                v-model="form.email_to"
                class="input"
                :disabled="!form.email_enabled"
                placeholder="多个用逗号分隔"
              />
            </div>
          </div>
          <div class="field">
            <label>跳过 TLS 证书校验<span v-if="inherited('email_insecure')" class="tag-default" title="未单独配置，当前状态来自服务端默认值">默认</span></label>
            <label class="switch">
              <input v-model="form.email_insecure" type="checkbox" :disabled="!form.email_enabled" />
              <span class="track" />
              <span>{{ form.email_insecure ? '已跳过' : '正常校验' }}</span>
            </label>
            <div class="hint">仅在自签证书的自建邮件服务上打开，公网邮箱不要开。</div>
          </div>
          <div class="field" style="margin-bottom: 0">
            <label>面板对外地址（可选）</label>
            <input
              v-model="form.public_url"
              class="input"
              placeholder="https://relay.example.com"
            />
            <div class="hint">填了之后，通知里会附上可直接点开面板的链接。</div>
          </div>

          <div class="row" style="margin-top: 18px">
            <button class="btn" :class="{ 'is-loading': notifying }" :disabled="notifying || loading" @click="testNotify">
              {{ notifying ? '发送中…' : '发送测试通知' }}
            </button>
            <span class="muted" style="font-size: 12.5px">用已保存的配置真实推送一条</span>
          </div>
          <div class="hint notice-inline">
            收不到？先保存设置再点测试；仍失败就看服务端日志里的 <code>[notify]</code> 行，
            常见原因是未开 SMTP 服务或用错了登录密码（多数邮箱要单独申请授权码）。
          </div>
        </section>

        <!-- 备份与接入 -->
        <template v-if="activeSection === 'backup'">
          <section class="card" style="margin-bottom: 16px">
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
          </section>

          <section class="card">
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
          </section>
        </template>
      </div>
    </div>

    <!-- 底部固定保存条：滚到哪都能保存，不用回到顶部 -->
    <div class="settings-savebar" :class="{ dirty }">
      <span class="muted" style="font-size: 12.5px">
        <template v-if="dirty">有未保存的改动<template v-if="dirtySections.length">（{{ dirtySections.join('、') }}）</template></template>
        <template v-else>所有改动均已保存</template>
      </span>
      <div class="spacer" />
      <span v-if="dirty" class="muted" style="font-size: 11px">Ctrl+S</span>
      <button
        class="btn btn-primary"
        :class="{ 'is-loading': saving }"
        :disabled="saving || loading || !dirty"
        @click="save"
      >
        {{ saving ? '保存中…' : '保存设置' }}
      </button>
    </div>
  </div>
</template>
