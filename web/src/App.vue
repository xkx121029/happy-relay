<script setup lang="ts">
import { onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppShell from './components/AppShell.vue'
import ConfirmDialog from './components/ConfirmDialog.vue'
import Toasts from './components/Toasts.vue'
import { api } from './api'
import { applyPrefs, panelTitle, prefs, state } from './store'

const route = useRoute()
const router = useRouter()

onMounted(async () => {
  try {
    const meta = await api.getMeta()
    state.meta = meta
    const title = meta.settings?.panel_title
    if (title) {
      panelTitle.value = title
      document.title = title
    }
    // 应用面板偏好：主题、密度、默认落地页等。
    applyPrefs(meta.settings)
    if (prefs.defaultPage !== '/' && route.path === '/') {
      router.replace(prefs.defaultPage)
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
  <ConfirmDialog />
</template>
