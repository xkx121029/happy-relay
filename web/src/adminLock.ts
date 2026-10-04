// 管理面板锁：管理员密码逐请求携带（与后端 internal/api/admin_auth.go 对应）。
//
// 交互约定：
//   - 没设密码时 protected=false，面板照常直接进（自用部署零摩擦）。
//   - 设了密码后 protected=true，裸路由仍对外可访问，但管理面板上锁：
//     点 sk- 图标 10 次唤出密码框，输入正确即进入。
//   - 勾选「记住本浏览器」才写 localStorage 长期保留；不勾只放 sessionStorage，
//     关掉浏览器就失效。

import { ref } from 'vue'

const LS_KEY = 'relay.admin.password'
const SS_KEY = 'relay.admin.password.session'

/** 后端是否已启用管理员密码保护 */
export const adminProtected = ref(false)
/** 是否已完成首次状态探测（探测完成前不渲染面板，避免闪一下再进锁屏） */
export const adminChecked = ref(false)
/** 密码来源：settings（设置页）/ env（环境变量）/ 空=未启用 */
export const adminSource = ref('')
/** 当前是否已解锁（浏览器里有可用密码） */
export const adminUnlocked = ref(false)
/** 锁屏被 401 触发时置 true，前端据此展示锁屏 */
export const adminLockedNow = ref(false)

function readLS(): string {
  try {
    return localStorage.getItem(LS_KEY) || ''
  } catch {
    return ''
  }
}
function readSS(): string {
  try {
    return sessionStorage.getItem(SS_KEY) || ''
  } catch {
    return ''
  }
}

/** 当前要发给后端的管理员密码（长期记忆优先于本次会话） */
export function currentAdminPassword(): string {
  return readLS() || readSS()
}

/** 解锁成功后的本地留存：remember=true 写 localStorage，否则只留 sessionStorage */
export function rememberAdminPassword(password: string, remember: boolean) {
  try {
    if (remember) {
      localStorage.setItem(LS_KEY, password)
      sessionStorage.removeItem(SS_KEY)
    } else {
      sessionStorage.setItem(SS_KEY, password)
      localStorage.removeItem(LS_KEY)
    }
  } catch {
    /* 隐私模式下写不进去也不影响本次会话使用 */
  }
  adminUnlocked.value = true
  adminLockedNow.value = false
}

/** 退出管理面板：清掉本地密码，回到锁屏 */
export function forgetAdminPassword() {
  try {
    localStorage.removeItem(LS_KEY)
    sessionStorage.removeItem(SS_KEY)
  } catch {
    /* 忽略 */
  }
  adminUnlocked.value = false
  adminLockedNow.value = true
}

/** 是否处于「需要显示锁屏」的状态 */
export function needLockScreen(): boolean {
  return adminProtected.value && !adminUnlocked.value
}

async function fetchStatus(): Promise<void> {
  try {
    const res = await fetch('/api/admin/status', { headers: { 'X-Admin-Password': currentAdminPassword() } })
    if (!res.ok) return
    const data = await res.json()
    adminProtected.value = !!data.protected
    adminSource.value = data.source || ''
    adminUnlocked.value = !!data.unlocked
    adminLockedNow.value = adminProtected.value && !adminUnlocked.value
  } catch {
    // 后端不可达时不锁屏（离线状态下不该把用户挡在门外）
  } finally {
    adminChecked.value = true
  }
}

/**
 * 装一次 fetch 拦截：所有同源 /api/ 请求自动带上管理员密码；
 * 收到 X-Admin-Lock 头说明密码不对/已变更，立刻切锁屏。
 * 放在这里而不是逐个 api 方法里改，是为了 SSE、multipart、blob 下载这些
 * 非 JSON 通道也能一起覆盖。
 */
export function installAdminLock() {
  const rawFetch = window.fetch.bind(window)
  window.fetch = ((input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : (input as Request).url
    const sameOrigin = url.startsWith('/') || url.startsWith(location.origin)
    if (sameOrigin && url.includes('/api/')) {
      const headers = new Headers(init.headers || (input instanceof Request ? (input as Request).headers : undefined))
      const pw = currentAdminPassword()
      if (pw) headers.set('X-Admin-Password', pw)
      init = { ...init, headers }
    }
    return rawFetch(input, init).then((res) => {
      if (res.headers.get('X-Admin-Lock') === '1') {
        adminProtected.value = true
        adminUnlocked.value = false
        adminLockedNow.value = true
      }
      return res
    })
  }) as typeof window.fetch

  void fetchStatus()
}

/** 解锁校验：密码对就本地留存，错就返回 false */
export async function tryUnlock(password: string, remember: boolean): Promise<boolean> {
  const res = await fetch('/api/admin/unlock', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Admin-Password': password },
    body: JSON.stringify({ password }),
  })
  if (!res.ok) return false
  const data = await res.json().catch(() => ({} as any))
  if (data.protected === false) {
    // 后端其实没开保护（密码已被清除），直接放行
    adminProtected.value = false
    adminUnlocked.value = true
    adminLockedNow.value = false
    return true
  }
  rememberAdminPassword(password, remember)
  return true
}
