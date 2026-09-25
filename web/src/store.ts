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

// 轻量提示条。
export interface ToastItem {
  id: number
  type: 'success' | 'error' | 'info'
  message: string
}

export const toasts = ref<ToastItem[]>([])
let toastSeq = 0

export function toast(message: string, type: ToastItem['type'] = 'success') {
  const id = ++toastSeq
  toasts.value.push({ id, type, message })
  setTimeout(() => {
    toasts.value = toasts.value.filter((t) => t.id !== id)
  }, 3600)
}
