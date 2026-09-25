<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { cycleTheme, panelTitle, prefs, themeLabel, toast } from '../store'

const route = useRoute()
const router = useRouter()

interface NavItem {
  path: string
  label: string
  desc: string
}

const groups: { name: string; items: NavItem[] }[] = [
  {
    name: '运行',
    items: [
      { path: '/', label: '概览', desc: '调用统计与近期动态' },
      { path: '/playground', label: '调试台', desc: '直接在网页里对话、出图、出视频' },
      { path: '/tasks', label: '任务', desc: '视频任务的进度与结果' },
    ],
  },
  {
    name: '配置',
    items: [
      { path: '/providers', label: '渠道', desc: '管理上游服务商与密钥' },
      { path: '/catalog', label: '模型库', desc: '探测到的可用上游模型，仅供配置引用' },
      { path: '/models', label: '模型', desc: '配置对外模型与映射' },
      { path: '/keys', label: '秘钥', desc: '分发给客户端的访问凭据' },
      { path: '/skills', label: '技能', desc: '带参数的提示词模板' },
    ],
  },
  {
    name: '工具',
    items: [
      { path: '/connector', label: '连接器生成器', desc: '用 AI 把接口描述翻译成模板' },
      { path: '/logs', label: '调用日志', desc: '请求明细与用量统计' },
      { path: '/settings', label: '设置', desc: '系统级 AI 与面板选项' },
    ],
  },
]

const current = computed(() => {
  const all = groups.flatMap((g) => g.items)
  return all.find((i) => i.path === route.path) || all[0]
})

function go(path: string) {
  router.push(path)
}

// toggleTheme 在深色 / 浅色 / 跟随系统之间循环，即时生效。
function toggleTheme() {
  toast(`主题已切换为「${themeLabel(cycleTheme())}」`, 'info')
}
</script>

<template>
  <div class="shell">
    <aside class="sidebar">
      <div class="brand">
        <span class="logo">H</span>
        <span>{{ panelTitle }}</span>
      </div>
      <nav class="nav">
        <template v-for="g in groups" :key="g.name">
          <div class="nav-group">{{ g.name }}</div>
          <div
            v-for="item in g.items"
            :key="item.path"
            class="nav-item"
            :class="{ active: route.path === item.path }"
            @click="go(item.path)"
          >
            <span class="dot" />
            <span>{{ item.label }}</span>
          </div>
        </template>
      </nav>
      <div class="sidebar-foot">
        <div>Happy Relay</div>
        <div>自用模型中转站</div>
      </div>
    </aside>

    <div class="main">
      <header class="topbar">
        <h1>{{ current.label }}</h1>
        <span class="sub">{{ current.desc }}</span>
        <div class="spacer" />
        <button
          class="btn btn-sm btn-ghost"
          :title="'切换主题（当前：' + themeLabel(prefs.theme) + '）'"
          @click="toggleTheme"
        >
          {{ themeLabel(prefs.theme) }}
        </button>
      </header>
      <main class="content">
        <slot />
      </main>
    </div>
  </div>
</template>
