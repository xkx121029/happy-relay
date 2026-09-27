import { createApp } from 'vue'
import App from './App.vue'
import { router } from './router'
import './styles.css'
// 移动端适配层放在最后加载，确保能覆盖桌面端规则。
import './mobile.css'

createApp(App).use(router).mount('#app')
