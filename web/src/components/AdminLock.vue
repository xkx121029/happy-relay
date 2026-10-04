<script setup lang="ts">
// 管理面板锁屏：未解锁时替换整个后台外壳。
// 入口手势：连点 sk- 图标 10 次（下同）唤出密码框，避免入口按钮直白暴露。
import { computed, onMounted, ref } from 'vue'
import {
  adminProtected,
  adminSource,
  adminUnlocked,
  adminLockedNow,
  tryUnlock,
} from '../adminLock'
import { panelTitle } from '../store'

const NEED_CLICKS = 10
const WINDOW_MS = 2500 // 超过这个间隔就重新计数，别让人慢慢点十个

const clicks = ref(0)
const armed = ref(false) // 是否已唤出密码框
const password = ref('')
const remember = ref(true) // 默认勾选：勾了才写 localStorage 长期保留
const busy = ref(false)
const errorMsg = ref('')
const lastClick = ref(0)

const sourceText = computed(() =>
  adminSource.value === 'env' ? '密码由环境变量 RELAY_ADMIN_PASSWORD 指定' : '密码在设置页配置',
)

function onLogoClick() {
  if (armed.value) return
  const now = Date.now()
  clicks.value = now - lastClick.value > WINDOW_MS ? 1 : clicks.value + 1
  lastClick.value = now
  if (clicks.value >= NEED_CLICKS) {
    clicks.value = 0
    armed.value = true
    errorMsg.value = ''
  }
}

async function submit() {
  if (!password.value || busy.value) return
  busy.value = true
  errorMsg.value = ''
  try {
    const ok = await tryUnlock(password.value, remember.value)
    if (!ok) {
      errorMsg.value = '密码不对，再试一次'
      password.value = ''
      return
    }
    armed.value = false
    password.value = ''
  } catch {
    errorMsg.value = '校验请求失败，检查网络或服务是否在线'
  } finally {
    busy.value = false
  }
}

onMounted(() => {
  // 已经解锁过（记住本浏览器）就不该看到锁屏，这里兜底纠正状态。
  adminLockedNow.value = adminProtected.value && !adminUnlocked.value
})
</script>

<template>
  <div class="lock">
    <div class="lock-card">
      <!-- sk- 图标：连点 10 次唤出密码 -->
      <button
        class="lock-logo"
        :class="{ armed }"
        title="管理面板入口"
        @click="onLogoClick"
      >
        sk-
      </button>

      <h1>{{ panelTitle }}</h1>
      <p class="muted">管理面板已锁定</p>

      <template v-if="!armed">
        <p class="hint">
          连点上方图标 10 次，输入管理员密码即可进入。
        </p>
        <p v-if="clicks > 0" class="hint">还差 {{ 10 - clicks }} 下…</p>
      </template>

      <form v-else class="lock-form" @submit.prevent="submit">
        <input
          v-model="password"
          class="input"
          type="password"
          placeholder="管理员密码"
          autocomplete="current-password"
          autofocus
        />
        <label class="lock-remember">
          <input v-model="remember" type="checkbox" />
          <span>记住本浏览器（勾选后长期保留，免重复输入）</span>
        </label>
        <div v-if="errorMsg" class="lock-error">{{ errorMsg }}</div>
        <div class="row">
          <button class="btn btn-primary" type="submit" :disabled="busy || !password">
            {{ busy ? '校验中…' : '进入管理面板' }}
          </button>
          <button class="btn btn-ghost" type="button" @click="armed = false">取消</button>
        </div>
        <div class="hint">{{ sourceText }}</div>
      </form>
    </div>
  </div>
</template>

<style scoped>
.lock {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: var(--s6);
  background: var(--bg);
  color: var(--text);
}
.lock-card {
  width: min(420px, 100%);
  text-align: center;
  background: var(--bg-elev);
  border: 1px solid var(--border);
  border-radius: var(--r-lg);
  padding: var(--s7);
  box-shadow: var(--e2);
}
.lock-logo {
  width: 64px;
  height: 64px;
  margin: 0 auto 14px;
  border-radius: var(--r);
  border: 1px solid var(--border-strong);
  background: var(--brand-grad);
  color: #fff;
  font: 700 20px/1 ui-monospace, SFMono-Regular, Menlo, monospace;
  cursor: pointer;
  user-select: none;
  transition: transform 0.15s ease;
}
.lock-logo:active {
  transform: scale(0.94);
}
.lock-logo.armed {
  box-shadow: 0 0 0 3px var(--accent-dim);
}
h1 {
  margin: 0 0 4px;
  font-size: 19px;
  color: var(--text);
}
.muted {
  color: var(--text-dim);
  margin: 0 0 18px;
  font-size: 13.5px;
}
.hint {
  color: var(--text-mute);
  font-size: 12.5px;
  margin: 12px 0 0;
}
.lock-form {
  display: flex;
  flex-direction: column;
  gap: 12px;
  text-align: left;
}
.lock-remember {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: var(--text-dim);
  cursor: pointer;
}
.lock-error {
  color: var(--danger);
  font-size: 12.5px;
}
.row {
  display: flex;
  gap: 10px;
  justify-content: center;
}
</style>
