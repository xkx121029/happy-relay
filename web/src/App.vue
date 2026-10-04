<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppShell from './components/AppShell.vue'
import AdminLock from './components/AdminLock.vue'
import ConfirmDialog from './components/ConfirmDialog.vue'
import SetupGuide from './components/SetupGuide.vue'
import Toasts from './components/Toasts.vue'
import UserPortal from './views/User.vue'
import { api } from './api'
import { adminChecked, adminLockedNow, adminProtected, adminUnlocked } from './adminLock'
import { applyPrefs, panelTitle, prefs, state } from './store'

const route = useRoute()
const router = useRouter()

// 管理面板上锁时不渲染后台外壳；用户门户（/user）走秘钥认证，始终公开。
const locked = computed(() => adminProtected.value && (!adminUnlocked.value || adminLockedNow.value))

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
  <!-- 用户门户走独立布局，不套管理后台的外壳 -->
  <UserPortal v-if="route.path.startsWith('/user')" />
  <!-- 管理员密码锁：连点 sk- 图标 10 次 + 密码才进管理面板 -->
  <div v-else-if="!adminChecked" class="boot-splash" />
  <AdminLock v-else-if="locked" />
  <AppShell v-else>
    <router-view v-slot="{ Component }">
      <transition name="page" mode="out-in">
        <component :is="Component" :key="route.path" />
      </transition>
    </router-view>
  </AppShell>
  <Toasts />
  <ConfirmDialog />
  <!-- 首次进入时引导填写内置渠道的 API Key -->
  <SetupGuide v-if="!locked" />
</template>

<style scoped>
.boot-splash {
  min-height: 100vh;
}
</style>
