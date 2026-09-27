import { reactive, ref } from 'vue'
import type { Meta, Model, Provider, Skill } from './api'

// 全局共享状态：面板元信息与跨页面复用列表。
export const state = reactive({
  meta: null as Meta | null,
  loading: false,
})

export const providers = ref<Provider[]>([])
export const models = ref<Model[]>([])
export const skills = ref<Skill[]>([])

export const panelTitle = ref('模型中转站')

// ---------- 面板偏好 ----------
// 偏好本身存在服务端 settings 表里（便于换设备后一致），但只影响前端呈现。

export type ThemeMode = 'dark' | 'light' | 'system'

const THEME_ORDER: ThemeMode[] = ['dark', 'light', 'system']
const THEME_LABELS: Record<string, string> = { dark: '深色', light: '浅色', system: '跟随系统' }

export const prefs = reactive({
  theme: 'dark' as ThemeMode,
  defaultPage: '/',
  autoRefreshSec: 0,
  compact: false,
  confirmDanger: true,
})

export function themeLabel(mode: string): string {
  return THEME_LABELS[mode] || THEME_LABELS.dark
}

// prefersLight 用于「跟随系统」模式，系统配色变化时自动重绘。
const prefersLight = window.matchMedia('(prefers-color-scheme: light)')

function effectiveTheme(): 'dark' | 'light' {
  if (prefs.theme === 'system') {
    return prefersLight.matches ? 'light' : 'dark'
  }
  return prefs.theme
}

export function applyTheme() {
  document.documentElement.dataset.theme = effectiveTheme()
}

export function applyCompact() {
  document.documentElement.dataset.density = prefs.compact ? 'compact' : 'cozy'
}

// cycleTheme 供侧边栏的快捷按钮在三档主题之间循环。
export function cycleTheme(): ThemeMode {
  const next = THEME_ORDER[(THEME_ORDER.indexOf(prefs.theme) + 1) % THEME_ORDER.length]
  prefs.theme = next
  applyTheme()
  return next
}

// applyPrefs 用服务端设置刷新本地偏好并立即生效。
export function applyPrefs(settings?: Record<string, string>) {
  const s = settings || {}
  const theme = (s.theme || 'dark') as ThemeMode
  prefs.theme = THEME_ORDER.includes(theme) ? theme : 'dark'
  prefs.defaultPage = s.default_page || '/'
  prefs.autoRefreshSec = Math.max(0, Number(s.auto_refresh_sec) || 0)
  prefs.compact = s.compact === 'on'
  // 默认开启二次确认，只有显式关闭才不拦截危险操作。
  prefs.confirmDanger = s.confirm_danger !== 'off'
  applyTheme()
  applyCompact()
}

prefersLight.addEventListener('change', () => {
  if (prefs.theme === 'system') applyTheme()
})

applyTheme()
applyCompact()

// 轻量提示条。
export interface ToastItem {
  id: number
  type: 'success' | 'error' | 'info'
  message: string
}

export const toasts = ref<ToastItem[]>([])

// ---------- 全局确认弹窗 ----------
// 供「危险操作二次确认」使用：prefs.confirmDanger 关闭时直接放行。

export interface ConfirmState {
  open: boolean
  title: string
  message: string
  confirmText: string
  danger: boolean
}

export const confirmState = reactive<ConfirmState>({
  open: false,
  title: '',
  message: '',
  confirmText: '确定',
  danger: true,
})

let confirmResolve: ((ok: boolean) => void) | null = null

export function confirmDialog(options: {
  title: string
  message?: string
  confirmText?: string
  danger?: boolean
}): Promise<boolean> {
  if (!prefs.confirmDanger) {
    return Promise.resolve(true)
  }
  // 重复触发时先结算上一个，避免 Promise 悬挂。
  if (confirmResolve) {
    confirmResolve(false)
    confirmResolve = null
  }
  confirmState.open = true
  confirmState.title = options.title
  confirmState.message = options.message || ''
  confirmState.confirmText = options.confirmText || '确定'
  confirmState.danger = options.danger !== false
  return new Promise((resolve) => {
    confirmResolve = resolve
  })
}

export function resolveConfirm(ok: boolean) {
  confirmState.open = false
  if (confirmResolve) {
    confirmResolve(ok)
    confirmResolve = null
  }
}

let toastSeq = 0

// toast 会「排队去重」：短时间内同文案同类型的提示只留一条，避免连点按钮刷屏。
export function toast(message: string, type: ToastItem['type'] = 'success') {
  const dup = toasts.value.find((t) => t.message === message && t.type === type)
  if (dup) {
    dup.id = ++toastSeq
    return
  }
  const id = ++toastSeq
  toasts.value.push({ id, type, message })
  if (toasts.value.length > 4) {
    toasts.value.splice(0, toasts.value.length - 4)
  }
  setTimeout(() => dismissToast(id), 3600)
}

export function dismissToast(id: number) {
  toasts.value = toasts.value.filter((t) => t.id !== id)
}
