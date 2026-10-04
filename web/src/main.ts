import { createApp } from 'vue'
import App from './App.vue'
import { router } from './router'
import './styles.css'
// 移动端适配层放在最后加载，确保能覆盖桌面端规则。
import './mobile.css'
import { installAdminLock } from './adminLock'

// 先装锁：所有 /api/ 请求从这一刻起自动带管理员密码，
// 401 也会即时把界面切到锁屏。
installAdminLock()

createApp(App).use(router).mount('#app')
