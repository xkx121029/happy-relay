<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import AgentChat from '../components/AgentChat.vue'
import { api } from '../api'

// ---------- Agent 独立页 ----------
// 全屏与站内主模型对话：查配置 / 建模型 / 改设置 / 删秘钥。
// 与右下角悬浮球共享同一份会话（localStorage agent.chat.v1），随时切换不丢上下文。

const chatEl = ref<InstanceType<typeof AgentChat> | null>(null)
const systemModel = ref('')

onMounted(async () => {
  try {
    const s = await api.getSettings()
    if (s.system_model) systemModel.value = s.system_model
  } catch {}
})
onBeforeUnmount(() => window.removeEventListener('keydown', onKeydown))

function onKeydown(e: KeyboardEvent) {
  if ((e.ctrlKey || e.metaKey) && e.key === 'Backspace') {
    e.preventDefault()
    chatEl.value?.clearChat()
  }
}
window.addEventListener('keydown', onKeydown)
</script>

<template>
  <div class="agent-page">
    <div class="agent-head">
      <div class="agent-head-main">
        <span class="agent-logo">✦</span>
        <div>
          <h2 class="agent-title">Agent</h2>
          <p class="agent-sub">
            站内主模型指挥面板：自然语言查配置、建模型、改设置、删秘钥
            <template v-if="systemModel"> · 主模型 <strong>{{ systemModel }}</strong></template>
            <template v-else> · <router-link to="/settings" class="agent-link">去设置选择主模型</router-link></template>
          </p>
        </div>
      </div>
      <span class="agent-tip">与悬浮球共享对话记录 · 删除类操作需二次确认</span>
    </div>

    <div class="agent-stage card">
      <AgentChat ref="chatEl" />
    </div>
  </div>
</template>

<style scoped>
.agent-page {
  display: flex;
  flex-direction: column;
  /* 顶栏(52) + 页头 + 边距，剩余全给聊天区，输入框始终贴底可见 */
  height: calc(100vh - 52px - 118px);
  min-height: 460px;
}
.agent-head {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 12px;
  flex-wrap: wrap;
}
.agent-head-main {
  display: flex;
  align-items: center;
  gap: 12px;
}
.agent-logo {
  width: 40px;
  height: 40px;
  border-radius: 12px;
  display: grid;
  place-items: center;
  font-size: 19px;
  color: #fff;
  background: linear-gradient(135deg, var(--accent), var(--accent-2));
  box-shadow: var(--glow);
}
.agent-title {
  margin: 0;
  font-size: 17px;
  line-height: 1.2;
}
.agent-sub {
  margin: 2px 0 0;
  font-size: 12.5px;
  color: var(--text-mute);
}
.agent-sub strong {
  color: var(--text-dim);
}
.agent-link {
  color: var(--accent-text);
}
.agent-tip {
  font-size: 11.5px;
  color: var(--text-mute);
}
.agent-stage {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  padding: 0;
  overflow: hidden;
}
</style>
