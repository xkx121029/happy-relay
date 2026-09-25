<script setup lang="ts">
import { onMounted } from 'vue'
import AppShell from './components/AppShell.vue'
import Toasts from './components/Toasts.vue'
import { api } from './api'
import { panelTitle, state } from './store'

onMounted(async () => {
  try {
    const meta = await api.getMeta()
    state.meta = meta
    const title = meta.settings?.panel_title
    if (title) {
      panelTitle.value = title
      document.title = title
    }
  } catch {
    // 元信息失败不阻塞面板渲染，各页面会自行重试。
  }
})
</script>

<template>
  <AppShell>
    <router-view v-slot="{ Component }">
      <component :is="Component" />
    </router-view>
  </AppShell>
  <Toasts />
</template>
