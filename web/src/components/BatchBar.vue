<script setup lang="ts">
import type { BatchAction } from '../batch'

// 批量操作工具条：选中行 > 0 时浮现在表格上方。
defineProps<{ count: number; busy: boolean }>()
const emit = defineEmits<{ (e: 'act', a: BatchAction): void; (e: 'clear'): void }>()
</script>

<template>
  <transition name="fade">
    <div v-if="count" class="batch-bar">
      <span class="bb-count">已选 <b>{{ count }}</b> 项</span>
      <span class="bb-sep" />
      <button class="btn btn-sm" :disabled="busy" @click="emit('act', 'enable')">批量启用</button>
      <button class="btn btn-sm" :disabled="busy" @click="emit('act', 'disable')">批量禁用</button>
      <button class="btn btn-sm bb-danger" :disabled="busy" @click="emit('act', 'delete')">批量删除</button>
      <button class="btn btn-sm btn-ghost" :disabled="busy" @click="emit('clear')">取消选择</button>
    </div>
  </transition>
</template>
