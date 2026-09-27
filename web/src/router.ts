import { createRouter, createWebHistory } from 'vue-router'

// 懒加载各页面，减小首屏体积。
const routes = [
  { path: '/', name: 'overview', component: () => import('./views/Overview.vue') },
  { path: '/agent', name: 'agent', component: () => import('./views/Agent.vue') },
  { path: '/playground', name: 'playground', component: () => import('./views/Playground.vue') },
  { path: '/providers', name: 'providers', component: () => import('./views/Providers.vue') },
  { path: '/catalog', name: 'catalog', component: () => import('./views/Catalog.vue') },
  { path: '/models', name: 'models', component: () => import('./views/Models.vue') },
  { path: '/builtin', name: 'builtin', component: () => import('./views/Builtin.vue') },
  { path: '/keys', name: 'keys', component: () => import('./views/Keys.vue') },
  { path: '/skills', name: 'skills', component: () => import('./views/Skills.vue') },
  { path: '/tasks', name: 'tasks', component: () => import('./views/Tasks.vue') },
  { path: '/connector', name: 'connector', component: () => import('./views/Connector.vue') },
  { path: '/logs', name: 'logs', component: () => import('./views/Logs.vue') },
  { path: '/settings', name: 'settings', component: () => import('./views/Settings.vue') },
  { path: '/:pathMatch(.*)*', redirect: '/' },
]

export const router = createRouter({
  history: createWebHistory(),
  routes,
  scrollBehavior: () => ({ top: 0 }),
})
