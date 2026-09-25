<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import Modal from '../components/Modal.vue'
import { api, type UsageLog } from '../api'
import { state, toast } from '../store'
import { capLabel, fmtTime, fmtNumber, prettyJSON } from '../utils'

const list = ref<UsageLog[]>([])
const loading = ref(true)
const capFilter = ref('')
const keyword = ref('')
const detail = ref<UsageLog | null>(null)

const capOptions = computed(
  () =>
    state.meta?.capabilities || [
      { value: 'chat', label: '文本对话' },
      { value: 'image', label: '图片生成' },
      { value: 'video', label: '视频生成' },
      { value: 'embedding', label: '文本向量' },
    ],
)

async function load() {
  loading.value = true
  try {
    if (!state.meta) state.meta = await api.getMeta()
    list.value = await api.listLogs({ limit: 200, capability: capFilter.value, q: keyword.value.trim() })
  } catch (e: any) {
    toast(e.message || '加载失败', 'error')
  } finally {
    loading.value = false
  }
}

onMounted(load)

async function clearAll() {
  if (!confirm('确定清空所有调用日志？该操作不可恢复。')) return
  try {
    await api.clearLogs()
    toast('日志已清空')
    await load()
  } catch (e: any) {
    toast(e.message || '清空失败', 'error')
  }
}

function ok(l: UsageLog) {
  return l.status < 400 && !l.error
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h2>调用日志</h2>
        <p>记录每次调用的模型、渠道、耗时与 token 用量。</p>
      </div>
      <div class="row">
        <button class="btn btn-sm" :disabled="loading" @click="load">刷新</button>
        <button class="btn btn-sm btn-danger" @click="clearAll">清空日志</button>
      </div>
    </div>

    <div class="toolbar">
      <select v-model="capFilter" class="select" style="width: 160px" @change="load">
        <option value="">全部能力</option>
        <option v-for="c in capOptions" :key="c.value" :value="c.value">{{ c.label }}</option>
      </select>
      <input
        v-model="keyword"
        class="input"
        style="width: 260px"
        placeholder="搜索模型 / 渠道 / 秘钥 / 错误"
        @keydown.enter="load"
      />
      <button class="btn btn-sm" @click="load">查询</button>
    </div>

    <div v-if="loading" class="card"><div class="skeleton" style="width: 40%" /></div>

    <div v-else-if="list.length" class="table-wrap">
      <table class="table">
        <thead>
          <tr>
            <th>时间</th>
            <th>能力</th>
            <th>模型</th>
            <th>渠道</th>
            <th>秘钥</th>
            <th>状态</th>
            <th>耗时</th>
            <th>Token</th>
            <th style="text-align: right">详情</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="l in list" :key="l.id">
            <td class="nowrap muted">{{ fmtTime(l.created_at) }}</td>
            <td><span class="badge brand">{{ capLabel(l.capability) }}</span></td>
            <td class="mono clamp-1">{{ l.model }}</td>
            <td class="dim clamp-1">{{ l.provider_name || '—' }}</td>
            <td class="dim clamp-1">{{ l.api_key_name || '—' }}</td>
            <td>
              <span class="badge" :class="ok(l) ? 'ok' : 'danger'">{{ ok(l) ? '成功' : '失败' }}</span>
              <span v-if="l.stream" class="badge" style="margin-left: 4px">流式</span>
            </td>
            <td class="muted nowrap">{{ l.latency_ms }} ms</td>
            <td class="muted nowrap">{{ fmtNumber(l.prompt_tokens + l.completion_tokens) }}</td>
            <td>
              <div class="row" style="justify-content: flex-end">
                <button class="btn btn-sm btn-ghost" @click="detail = l">查看</button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-else class="card empty">
      <div class="big">暂无日志</div>
      <div>发起一次调用后这里会记录明细。</div>
    </div>

    <Modal v-if="detail" wide title="调用详情" :subtitle="fmtTime(detail.created_at)" @close="detail = null">
      <div class="meta-list" style="margin-bottom: 16px">
        <dt>能力 / 模型</dt>
        <dd>{{ capLabel(detail.capability) }} · <span class="mono">{{ detail.model }}</span></dd>
        <dt>渠道 / 秘钥</dt>
        <dd>{{ detail.provider_name || '—' }} · {{ detail.api_key_name || '—' }}</dd>
        <dt>状态 / 耗时</dt>
        <dd>{{ ok(detail) ? '成功' : '失败' }} · {{ detail.latency_ms }} ms{{ detail.stream ? ' · 流式' : '' }}</dd>
        <dt>Token</dt>
        <dd>输入 {{ detail.prompt_tokens }} / 输出 {{ detail.completion_tokens }}</dd>
      </div>

      <div v-if="detail.error" class="field">
        <div class="field-label">错误信息</div>
        <pre class="code-block" style="color: var(--danger)">{{ detail.error }}</pre>
      </div>

      <div v-if="detail.request_body" class="field">
        <div class="field-label">请求体</div>
        <pre class="code-block">{{ prettyJSON(detail.request_body) }}</pre>
      </div>

      <div v-if="detail.response_preview" class="field">
        <div class="field-label">响应预览</div>
        <pre class="code-block">{{ detail.response_preview }}</pre>
      </div>
    </Modal>
  </div>
</template>
