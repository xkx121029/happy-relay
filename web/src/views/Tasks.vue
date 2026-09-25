<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { api, type VideoTask } from '../api'
import { confirmDialog, toast } from '../store'
import { fmtTime } from '../utils'

const list = ref<VideoTask[]>([])
const loading = ref(true)
const auto = ref(true)
let timer: number | null = null

async function load(silent = false) {
  if (!silent) loading.value = true
  try {
    list.value = await api.listTasks(50)
  } catch (e: any) {
    if (!silent) toast(e.message || '加载失败', 'error')
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  load()
  timer = window.setInterval(() => {
    if (auto.value) load(true)
  }, 5000)
})

onUnmounted(() => {
  if (timer !== null) window.clearInterval(timer)
})

async function remove(t: VideoTask) {
  const ok = await confirmDialog({
    title: '删除任务',
    message: `确定删除任务 ${t.id}？生成中的任务也会从列表移除。`,
    confirmText: '删除',
  })
  if (!ok) return
  try {
    await api.deleteTask(t.id)
    toast('已删除')
    await load()
  } catch (e: any) {
    toast(e.message || '删除失败', 'error')
  }
}

function taskBadge(status: string) {
  if (status === 'succeeded') return 'ok'
  if (status === 'failed') return 'danger'
  return 'warn'
}

function taskLabel(status: string) {
  return { queued: '排队中', running: '生成中', succeeded: '已完成', failed: '失败' }[status] || status
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h2>任务</h2>
        <p>视频生成任务的提交记录与实时进度，后台会自动轮询上游状态。</p>
      </div>
      <div class="row">
        <label class="switch">
          <input v-model="auto" type="checkbox" />
          <span class="track" />
          <span>自动刷新</span>
        </label>
        <button class="btn btn-sm" :disabled="loading" @click="load()">刷新</button>
      </div>
    </div>

    <div v-if="loading" class="card"><div class="skeleton" style="width: 40%" /></div>

    <div v-else-if="list.length" class="table-wrap">
      <table class="table">
        <thead>
          <tr>
            <th>任务 ID</th>
            <th>模型</th>
            <th>状态</th>
            <th style="width: 150px">进度</th>
            <th>创建时间</th>
            <th>更新时间</th>
            <th style="text-align: right">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="t in list" :key="t.id">
            <td class="mono clamp-1" style="max-width: 190px">{{ t.id }}</td>
            <td class="mono dim clamp-1">{{ t.model }}</td>
            <td>
              <span class="badge" :class="taskBadge(t.status)">{{ taskLabel(t.status) }}</span>
              <div v-if="t.error" class="muted clamp-1" style="font-size: 11.5px; max-width: 220px">
                {{ t.error.message }}
              </div>
            </td>
            <td>
              <div class="progress"><i :style="{ width: (t.progress || 0) + '%' }" /></div>
              <div class="muted" style="font-size: 11.5px; margin-top: 4px">{{ t.progress || 0 }}%</div>
            </td>
            <td class="muted nowrap">{{ fmtTime(t.created_at) }}</td>
            <td class="muted nowrap">{{ fmtTime(t.updated_at) }}</td>
            <td>
              <div class="row" style="justify-content: flex-end; gap: 6px">
                <a
                  v-if="t.video_url"
                  class="btn btn-sm btn-ghost"
                  :href="t.video_url"
                  target="_blank"
                  rel="noreferrer"
                >
                  查看视频
                </a>
                <button class="btn btn-sm btn-ghost" style="color: var(--danger)" @click="remove(t)">
                  删除
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-else class="card empty">
      <div class="big">还没有视频任务</div>
      <div>在调试台或通过 /v1/videos 提交任务后，这里会显示进度与结果。</div>
    </div>
  </div>
</template>
