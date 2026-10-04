<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import AgentChat from '../components/AgentChat.vue'
import { api } from '../api'

// ---------- Agent 独立页（双栏工作台） ----------
// 左栏：主模型状态 + 分组快捷指令（点即执行）；右栏：全高聊天区。
// 与悬浮球共享同一份会话（localStorage agent.chat.v1），随时切换不丢上下文。

const chatEl = ref<InstanceType<typeof AgentChat> | null>(null)
const systemModel = ref('')
const fullAccess = ref(false)

onMounted(async () => {
  window.addEventListener('keydown', onKeydown)
  try {
    const s = await api.getSettings()
    if (s.system_model) systemModel.value = s.system_model
    fullAccess.value = s.agent_full_access === 'on'
  } catch {}
})
onBeforeUnmount(() => window.removeEventListener('keydown', onKeydown))

function onKeydown(e: KeyboardEvent) {
  if ((e.ctrlKey || e.metaKey) && e.key === 'Backspace') {
    e.preventDefault()
    chatEl.value?.clearChat()
  }
}

// 快捷指令：点击即发送（不经过输入框，一键跑完整轮）
const QUICK: { group: string; icon: string; items: string[] }[] = [
  {
    group: '配置',
    icon: '⚙',
    items: ['现在飞书整合是什么状态？', '把巡检间隔改成 120 秒', '跑一次配置自检并解读结果'],
  },
  {
    group: '模型',
    icon: '▣',
    items: ['列出所有启用的模型', '把智谱的视觉模型整合成一个聚合模型', '新建一个绘图模型'],
  },
  {
    group: '排障',
    icon: '◔',
    items: ['最近有哪些调用失败了？', '看看渠道熔断状态，有问题的复位一下', '导出一份配置备份'],
  },
]
function ask(text: string) {
  chatEl.value?.ask(text)
  sideOpen.value = false // 移动端抽屉里点完指令自动收起
}
const sideOpen = ref(false)
</script>

<template>
  <div class="agent-page">
    <!-- 左栏：返回 + 状态 + 快捷指令 -->
    <aside class="agent-side card" :class="{ open: sideOpen }">
      <div class="ag-brand">
        <span class="agent-logo">✦</span>
        <div>
          <h2 class="agent-title">Agent</h2>
          <p class="agent-sub">主模型指挥台</p>
        </div>
        <router-link to="/" class="ag-back" title="返回管理面板">⌂</router-link>
      </div>

      <div class="ag-status">
        <div class="ag-stat">
          <label>主模型</label>
          <span v-if="systemModel" class="mono ag-badge">{{ systemModel }}</span>
          <router-link v-else to="/settings" class="agent-link">去设置选择 →</router-link>
        </div>
        <div class="ag-stat">
          <label>执行权限</label>
          <span class="badge" :class="fullAccess ? 'warn' : 'ok'">
            <span class="dot" />{{ fullAccess ? '最大权限' : '删除需确认' }}
          </span>
        </div>
      </div>

      <div class="ag-quick">
        <div v-for="g in QUICK" :key="g.group" class="ag-qgroup">
          <div class="ag-qhead">{{ g.icon }} {{ g.group }}</div>
          <button v-for="q in g.items" :key="q" class="ag-qitem" :title="q" @click="ask(q)">{{ q }}</button>
        </div>
      </div>

      <div class="ag-side-foot">
        <button class="btn btn-ghost btn-sm btn-block" @click="chatEl?.clearChat()">清空会话</button>
        <p class="ag-tip">与悬浮球共享对话 · Ctrl+⌫ 清空</p>
      </div>
    </aside>
    <!-- 移动端抽屉遮罩 -->
    <div v-if="sideOpen" class="ag-backdrop" @click="sideOpen = false" />

    <!-- 右栏：聊天区 -->
    <div class="agent-stage card">
      <AgentChat ref="chatEl" />
      <!-- 窄屏：左栏收进抽屉，用这个按钮唤出 -->
      <button class="ag-fab" title="快捷指令" @click="sideOpen = !sideOpen">☰</button>
    </div>
  </div>
</template>

<style scoped>
.agent-page {
  display: flex;
  gap: 12px;
  /* 顶栏(52) + 页边距，剩余全给聊天区（管理外壳内，不再全屏） */
  height: calc(100vh - 52px - 24px);
  min-height: 460px;
}

/* ---------- 左栏 ---------- */
.agent-side {
  width: 236px;
  flex: none;
  display: flex;
  flex-direction: column;
  padding: 14px;
  overflow: hidden;
}
.ag-brand {
  display: flex;
  align-items: center;
  gap: 10px;
  padding-bottom: 12px;
  border-bottom: 1px solid var(--border);
}
.agent-logo {
  width: 38px;
  height: 38px;
  border-radius: 11px;
  display: grid;
  place-items: center;
  font-size: 18px;
  color: #fff;
  background: linear-gradient(135deg, var(--accent), var(--accent-2));
  box-shadow: var(--glow);
  flex: none;
}
.agent-title {
  margin: 0;
  font-size: 16px;
  line-height: 1.2;
}
.agent-sub {
  margin: 2px 0 0;
  font-size: 11.5px;
  color: var(--text-mute);
}
.ag-back {
  margin-left: auto;
  width: 28px;
  height: 28px;
  display: grid;
  place-items: center;
  border-radius: 8px;
  color: var(--text-mute);
  font-size: 15px;
  transition: 0.12s;
}
.ag-back:hover {
  background: var(--bg-hover);
  color: var(--text);
}
.ag-status {
  padding: 10px 0;
  border-bottom: 1px solid var(--border);
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.ag-stat {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.ag-stat label {
  font-size: 11.5px;
  color: var(--text-mute);
  flex: none;
}
.ag-badge {
  font-size: 11.5px;
  background: var(--accent-dim);
  color: var(--accent-text);
  padding: 2px 8px;
  border-radius: 999px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.agent-link {
  font-size: 12px;
  color: var(--accent-text);
}
.ag-quick {
  flex: 1;
  overflow-y: auto;
  padding: 10px 0;
  min-height: 0;
}
.ag-qgroup + .ag-qgroup {
  margin-top: 12px;
}
.ag-qhead {
  font-size: 11px;
  letter-spacing: 0.1em;
  color: var(--text-mute);
  padding: 0 4px 6px;
}
.ag-qitem {
  display: block;
  width: 100%;
  text-align: left;
  padding: 7px 10px;
  margin: 2px 0;
  border: none;
  background: transparent;
  color: var(--text-dim);
  font-size: 12.5px;
  border-radius: var(--radius-sm);
  cursor: pointer;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  transition: 0.12s;
}
.ag-qitem:hover {
  background: var(--accent-dim);
  color: var(--accent-text);
}
.ag-side-foot {
  padding-top: 10px;
  border-top: 1px solid var(--border);
}
.ag-tip {
  margin: 8px 0 0;
  font-size: 10.5px;
  color: var(--text-mute);
  text-align: center;
}

/* ---------- 右栏 ---------- */
.agent-stage {
  flex: 1;
  min-width: 0;
  min-height: 0;
  display: flex;
  flex-direction: column;
  padding: 0;
  overflow: hidden;
}

/* 抽屉唤出钮：仅窄屏显示（基础规则在前，媒体查询才能覆盖） */
.ag-fab {
  display: none;
  position: absolute;
  top: 12px;
  right: 12px;
  z-index: 10;
  width: 36px;
  height: 36px;
  place-items: center;
  border: 1px solid var(--border);
  border-radius: 10px;
  background: var(--bg-elev);
  color: var(--text-dim);
  font-size: 15px;
  cursor: pointer;
}
.agent-stage {
  position: relative;
}

/* 窄屏：左栏收成抽屉 */
@media (max-width: 860px) {
  .agent-page {
    height: calc(100dvh - var(--topbar-h) - var(--tabbar-h) - var(--safe-bottom) - 12px);
    min-height: 380px;
  }
  .agent-side {
    position: fixed;
    left: 0;
    top: 0;
    bottom: 0;
    z-index: 95;
    width: 280px;
    border-radius: 0;
    transform: translateX(-105%);
    transition: transform 0.24s cubic-bezier(0.32, 0.72, 0, 1);
    box-shadow: var(--shadow);
  }
  .agent-side.open {
    transform: none;
  }
  .ag-backdrop {
    position: fixed;
    inset: 0;
    z-index: 90;
    background: rgba(5, 6, 9, 0.55);
    backdrop-filter: blur(2px);
  }
  .ag-fab {
    display: grid;
  }
}
</style>
