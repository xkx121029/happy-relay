<script setup lang="ts">
defineProps<{
  title: string
  subtitle?: string
  wide?: boolean
}>()

const emit = defineEmits<{ (e: 'close'): void }>()
</script>

<template>
  <div class="modal-mask" @mousedown.self="emit('close')">
    <div class="modal" :class="{ wide }" @mousedown.stop>
      <div class="modal-head">
        <div>
          <h3>{{ title }}</h3>
          <div v-if="subtitle" class="sub">{{ subtitle }}</div>
        </div>
        <button class="close-x" type="button" @click="emit('close')">×</button>
      </div>
      <div class="modal-body">
        <slot />
      </div>
      <div v-if="$slots.footer" class="modal-foot">
        <slot name="footer" />
      </div>
    </div>
  </div>
</template>
