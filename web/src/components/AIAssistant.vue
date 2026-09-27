<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'

// ---------- 站内 Agent 悬浮入口 ----------
// 聊天能力已升级为独立 Agent 页（/agent，全屏更好用）。
// 悬浮球只做一件事：任意页面一键直达；对话记录两处共享。

const router = useRouter()
const hasChat = ref(false)

onMounted(() => {
  try {
    hasChat.value = !!localStorage.getItem('agent.chat.v1')
  } catch {}
})

function go() {
  router.push('/agent')
}
</script>

<template>
  <button class="ai-fab" title="站内 Agent（主模型指挥面板）" @click="go">
    <span>✦</span>
    <span v-if="hasChat" class="ai-fab-badge" />
  </button>
</template>

<style scoped>
.ai-fab {
  position: fixed;
  right: 22px;
  bottom: 22px;
  z-index: 300;
  width: 48px;
  height: 48px;
  border-radius: 50%;
  border: 1px solid var(--border-strong);
  background: linear-gradient(135deg, var(--accent), var(--accent-2));
  color: #fff;
  font-size: 19px;
  cursor: pointer;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.4), var(--glow);
  transition: transform 0.15s, box-shadow 0.15s;
  user-select: none;
}
.ai-fab:hover {
  transform: scale(1.08);
}
.ai-fab:active {
  transform: scale(0.96);
}
.ai-fab-badge {
  position: absolute;
  top: 3px;
  right: 3px;
  width: 9px;
  height: 9px;
  border-radius: 50%;
  background: var(--ok);
  border: 2px solid var(--bg);
}
</style>
