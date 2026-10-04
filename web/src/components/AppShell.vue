<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import NavIcon from './NavIcon.vue'
import { cycleTheme, panelTitle, prefs, themeLabel, toast } from '../store'
import { api } from '../api'

const route = useRoute()
const router = useRouter()

interface NavItem {
  path: string
  label: string
  desc: string
  icon: string
}

const groups: { name: string; items: NavItem[] }[] = [
  {
    name: '运行',
    items: [
      { path: '/', label: '概览', desc: '调用统计与近期动态', icon: 'grid' },
      { path: '/agent', label: 'Agent', desc: '主模型指挥面板：自然语言查配置、建模型、改设置', icon: 'spark' },
      { path: '/playground', label: '调试台', desc: '直接在网页里对话、出图、出视频', icon: 'chat' },
      { path: '/tasks', label: '任务', desc: '视频任务的进度与结果', icon: 'film' },
    ],
  },
  {
    name: '配置',
    items: [
      { path: '/providers', label: '渠道', desc: '管理上游服务商与密钥', icon: 'server' },
      { path: '/catalog', label: '模型库', desc: '探测到的可用上游模型，仅供配置引用', icon: 'box' },
      { path: '/models', label: '模型', desc: '配置对外模型与映射', icon: 'chip' },
      { path: '/builtin', label: '内置模型', desc: '永久免费平台目录与一键接入', icon: 'gift' },
      { path: '/keys', label: '秘钥', desc: '分发给客户端的访问凭据', icon: 'key' },
    ],
  },
  {
    name: '工具',
    items: [
      { path: '/connector', label: '连接器生成器', desc: '用 AI 把接口描述翻译成模板', icon: 'plug' },
      { path: '/user', label: '用户门户', desc: '持秘钥用户的自助界面（独立布局）', icon: 'chat' },
      { path: '/logs', label: '调用日志', desc: '请求明细与用量统计', icon: 'list' },
      { path: '/settings', label: '设置', desc: '面板与服务端选项', icon: 'sliders' },
    ],
  },
]

const allItems = groups.flatMap((g) => g.items)

// 移动端底部标签栏只放高频入口，其余收进「更多」面板。
const TAB_PATHS = ['/', '/agent', '/playground', '/providers', '/models']
const tabs = computed(() => TAB_PATHS.map((p) => allItems.find((i) => i.path === p)).filter(Boolean) as NavItem[])
const moreItems = computed(() => allItems.filter((i) => !TAB_PATHS.includes(i.path)))

const current = computed(() => allItems.find((i) => i.path === route.path) || allItems[0])

const moreOpen = ref(false)

function go(path: string) {
  moreOpen.value = false
  if (route.path !== path) {
    router.push(path)
  }
}

// 切页后收起浮层，避免返回时仍停在「更多」上。
watch(
  () => route.path,
  () => {
    moreOpen.value = false
  },
)

// 「更多」展开时锁定背景滚动，避免浮层下方的页面跟着一起滑动。
watch(moreOpen, (open) => {
  document.body.style.overflow = open ? 'hidden' : ''
})

onUnmounted(() => {
  document.body.style.overflow = ''
})

function toggleTheme() {
  toast(`主题已切换为「${themeLabel(cycleTheme())}」`, 'info')
}

// 页面滚动后给顶栏加投影，让悬浮的 sticky 顶栏与内容分层
const scrolled = ref(false)
function onScroll() {
  scrolled.value = (window.scrollY || document.documentElement.scrollTop) > 4
}
onMounted(() => {
  window.addEventListener('scroll', onScroll, { passive: true })
})
onUnmounted(() => window.removeEventListener('scroll', onScroll))
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
            :title="item.desc"
            @click="go(item.path)"
          >
            <NavIcon :name="item.icon" />
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
      <header class="topbar" :class="{ scrolled }">
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

    <!-- 移动端底部标签栏 -->
    <nav class="tabbar">
      <button
        v-for="t in tabs"
        :key="t.path"
        type="button"
        class="tabbar-item"
        :class="{ active: route.path === t.path }"
        @click="go(t.path)"
      >
        <NavIcon :name="t.icon" />
        <span>{{ t.label }}</span>
      </button>
      <button
        type="button"
        class="tabbar-item"
        :class="{ active: moreOpen }"
        @click="moreOpen = !moreOpen"
      >
        <NavIcon name="more" />
        <span>更多</span>
      </button>
    </nav>

    <!-- 「更多」面板：从底部升起的页面清单 -->
    <Transition name="sheet">
      <div v-if="moreOpen" class="sheet-mask" @click.self="moreOpen = false">
        <div class="sheet">
          <div class="sheet-handle" />
          <div class="sheet-title">全部功能</div>
          <div class="more-grid">
            <button
              v-for="item in moreItems"
              :key="item.path"
              type="button"
              class="more-item"
              :class="{ active: route.path === item.path }"
              @click="go(item.path)"
            >
              <NavIcon :name="item.icon" />
              <span>{{ item.label }}</span>
            </button>
          </div>
          <button class="btn btn-block" style="margin-top: 14px" @click="moreOpen = false">
            关闭
          </button>
        </div>
      </div>
    </Transition>
  </div>
</template>
