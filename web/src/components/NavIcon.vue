<script setup lang="ts">
// 统一的线性图标集：线宽与圆角对齐主流原生 App 的底部导航观感。
defineProps<{ name: string }>()

const PATHS: Record<string, string> = {
  grid: 'M4 4h6v6H4zM14 4h6v6h-6zM4 14h6v6H4zM14 14h6v6h-6z',
  spark: 'M12 3l1.9 5.1L19 10l-5.1 1.9L12 17l-1.9-5.1L5 10l5.1-1.9zM19 16l.8 2.2L22 19l-2.2.8L19 22l-.8-2.2L16 19l2.2-.8z',
  chat: 'M20 12a8 8 0 0 1-8 8H7l-4 3 1.2-4.2A8 8 0 1 1 20 12z',
  film: 'M4 5h16v14H4zM9 5v14M15 5v14M4 9.5h5M4 14.5h5M15 9.5h5M15 14.5h5',
  server: 'M4 5h16v6H4zM4 13h16v6H4zM8 8h.01M8 16h.01',
  box: 'M12 3l8 4.5v9L12 21l-8-4.5v-9zM4 7.5l8 4.5 8-4.5M12 12v9',
  chip: 'M8 8h8v8H8zM4 10h2M4 14h2M18 10h2M18 14h2M10 4v2M14 4v2M10 18v2M14 18v2',
  key: 'M15.5 8.5a3.5 3.5 0 1 0-2.6 5.9L11 16.3l-2-2-2.5 2.5 2 2L6 21.3l-1.5-1.5 2-2 2 2 2.6-2.6a3.5 3.5 0 0 0 4.4-5.2z',
  wand: 'M5 19L16 8M14 6l1.5 1.5M18 4l.5 2 2 .5-2 .5-.5 2-.5-2-2-.5 2-.5zM6 6l.4 1.6L8 8l-1.6.4L6 10l-.4-1.6L4 8l1.6-.4z',
  plug: 'M9 2v6M15 2v6M6 8h12v4a6 6 0 0 1-12 0zM12 18v4',
  list: 'M8 6h13M8 12h13M8 18h13M3.5 6h.01M3.5 12h.01M3.5 18h.01',
  sliders: 'M4 7h5M13 7h7M4 17h9M17 17h3M11 4.5v5M15 14.5v5',
  gift: 'M4 11h16v10H4zM4 7h16v4H4zM12 7v14M8.5 7a2 2 0 1 1 1.3-3.5C11 4.5 12 7 12 7s1-2.5 2.2-3.5A2 2 0 1 1 15.5 7',
  more: 'M6 12h.01M12 12h.01M18 12h.01',
}
</script>

<template>
  <svg
    class="nav-icon"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="1.7"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
  >
    <path :d="PATHS[name] || PATHS.grid" />
  </svg>
</template>
