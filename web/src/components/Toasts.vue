<script setup lang="ts">
import { toasts } from '../store'
</script>

<template>
  <div class="toasts">
    <div v-for="t in toasts" :key="t.id" class="toast" :class="t.type">
      <span class="toast-dot" />
      <span>{{ t.message }}</span>
    </div>
  </div>
</template>
