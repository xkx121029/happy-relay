<script setup lang="ts">
import { toasts, dismissToast } from '../store'

// 每类提示配一个字形，扫一眼就能分辨成功/失败，不必读完整句话。
const GLYPH: Record<string, string> = { success: '✓', error: '!', info: 'i' }
</script>

<template>
  <div class="toasts" role="status" aria-live="polite">
    <div
      v-for="t in toasts"
      :key="t.id"
      class="toast"
      :class="t.type"
      title="点击关闭"
      @click="dismissToast(t.id)"
    >
      <span class="toast-dot">{{ GLYPH[t.type] }}</span>
      <span class="toast-msg">{{ t.message }}</span>
    </div>
  </div>
</template>
