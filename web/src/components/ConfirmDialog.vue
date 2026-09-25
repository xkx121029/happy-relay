<script setup lang="ts">
import Modal from './Modal.vue'
import { confirmState, resolveConfirm } from '../store'
</script>

<template>
  <Modal v-if="confirmState.open" :title="confirmState.title" @close="resolveConfirm(false)">
    <p v-if="confirmState.message" style="margin: 0">{{ confirmState.message }}</p>
    <template #footer>
      <div class="row">
        <div class="spacer" />
        <button class="btn" @click="resolveConfirm(false)">取消</button>
        <button
          class="btn"
          :class="confirmState.danger ? 'btn-danger' : 'btn-primary'"
          @click="resolveConfirm(true)"
        >
          {{ confirmState.confirmText }}
        </button>
      </div>
    </template>
  </Modal>
</template>
