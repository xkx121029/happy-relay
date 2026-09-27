<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'

const props = defineProps<{
  title: string
  subtitle?: string
  wide?: boolean
  // submitOnEnter 打开后，在弹窗内按 Enter（非输入框内的换行）即触发 submit 事件。
  submitOnEnter?: boolean
  busy?: boolean
}>()

const emit = defineEmits<{ (e: 'close'): void; (e: 'submit'): void }>()

// ESC 关闭 + 锁定背景滚动 + 打开时自动聚焦弹窗，减少「鼠标找关闭按钮」的负担。
const box = ref<HTMLElement | null>(null)
let prevOverflow = ''

function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') {
    e.stopPropagation()
    emit('close')
    return
  }
  if (e.key === 'Enter' && props.submitOnEnter && !props.busy) {
    const el = e.target as HTMLElement | null
    const tag = el?.tagName
    // 多行文本域和按钮自身的 Enter 语义不拦截。
    if (tag === 'TEXTAREA' || tag === 'BUTTON') return
    e.preventDefault()
    emit('submit')
  }
}

onMounted(() => {
  prevOverflow = document.body.style.overflow
  document.body.style.overflow = 'hidden'
  document.addEventListener('keydown', onKey)
  // 聚焦弹窗容器，让键盘用户可以直接 Tab 进表单。
  requestAnimationFrame(() => box.value?.focus())
})

onUnmounted(() => {
  document.body.style.overflow = prevOverflow
  document.removeEventListener('keydown', onKey)
})
</script>

<template>
  <div class="modal-mask" @mousedown.self="emit('close')">
    <div
      ref="box"
      class="modal"
      :class="{ wide, busy }"
      role="dialog"
      aria-modal="true"
      tabindex="-1"
      @mousedown.stop
    >
      <div class="modal-head">
        <div>
          <h3>{{ title }}</h3>
          <div v-if="subtitle" class="sub">{{ subtitle }}</div>
        </div>
        <button class="close-x" type="button" aria-label="关闭" @click="emit('close')">×</button>
      </div>
      <div class="modal-body">
        <slot />
      </div>
      <div v-if="$slots.footer" class="modal-foot">
        <slot name="footer" />
      </div>
    </div>
  </div>
</template>
