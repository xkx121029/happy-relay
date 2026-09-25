<script setup lang="ts">
import { ref, watch } from 'vue'
import type { TemplateSpec } from '../api'
import { prettyJSON } from '../utils'

const props = defineProps<{
  spec: TemplateSpec
  label: string
  showResponse?: boolean
}>()

const bodyText = ref('')
const headersText = ref('')
const queryText = ref('')
const statusMapText = ref('')

function sync() {
  const s = props.spec
  if (!s.response) s.response = {}
  bodyText.value = s.body && Object.keys(s.body).length ? prettyJSON(s.body) : ''
  headersText.value = s.headers && Object.keys(s.headers).length ? prettyJSON(s.headers) : ''
  queryText.value = s.query && Object.keys(s.query).length ? prettyJSON(s.query) : ''
  statusMapText.value =
    s.response.status_map && Object.keys(s.response.status_map).length
      ? prettyJSON(s.response.status_map)
      : ''
}

sync()
watch(() => props.spec, sync)

function parseJSON(text: string): any {
  const t = text.trim()
  if (!t) return undefined
  try {
    return JSON.parse(t)
  } catch {
    return undefined
  }
}

function applyBody() {
  props.spec.body = parseJSON(bodyText.value)
}
function applyHeaders() {
  props.spec.headers = parseJSON(headersText.value)
}
function applyQuery() {
  props.spec.query = parseJSON(queryText.value)
}
function applyStatusMap() {
  if (props.spec.response) {
    props.spec.response.status_map = parseJSON(statusMapText.value)
  }
}

const placeholders = '{{prompt}} {{upstream_model}} {{model}} {{image_url}} {{size}} {{duration}} {{task_id}} {{key}}'
</script>

<template>
  <details class="expander" open>
    <summary>{{ label }}</summary>

    <div class="grid cols-2" style="gap: 0 18px">
      <div class="field">
        <label>请求方法</label>
        <select v-model="props.spec.method" class="select">
          <option value="POST">POST</option>
          <option value="GET">GET</option>
        </select>
      </div>
      <div class="field">
        <label>路径</label>
        <input v-model="spec.path" class="input code" placeholder="/videos/generations" />
      </div>
    </div>

    <div class="field">
      <label>请求体模板（JSON）</label>
      <textarea
        v-model="bodyText"
        class="textarea code"
        rows="4"
        placeholder='{"model": "{{upstream_model}}", "prompt": "{{prompt}}"}'
        @blur="applyBody"
      />
      <div class="hint">可用占位符：{{ placeholders }}</div>
    </div>

    <div class="grid cols-2" style="gap: 0 18px">
      <div class="field">
        <label>额外请求头（JSON）</label>
        <textarea v-model="headersText" class="textarea code" rows="3" placeholder="{}" @blur="applyHeaders" />
      </div>
      <div class="field">
        <label>URL 查询参数（JSON）</label>
        <textarea v-model="queryText" class="textarea code" rows="3" placeholder="{}" @blur="applyQuery" />
      </div>
    </div>

    <template v-if="showResponse">
      <div class="field-label" style="margin-top: 6px">响应字段路径</div>
      <div class="grid cols-2" style="gap: 0 18px">
        <div class="field">
          <label>任务 ID</label>
          <input v-model="props.spec.response!.task_id" class="input code" placeholder="data.task_id" />
        </div>
        <div class="field">
          <label>状态</label>
          <input v-model="props.spec.response!.status" class="input code" placeholder="data.status" />
        </div>
        <div class="field">
          <label>进度</label>
          <input v-model="props.spec.response!.progress" class="input code" placeholder="data.progress" />
        </div>
        <div class="field">
          <label>视频地址</label>
          <input v-model="props.spec.response!.video_url" class="input code" placeholder="data.video_url" />
        </div>
        <div class="field">
          <label>图片地址</label>
          <input v-model="props.spec.response!.image_url" class="input code" placeholder="data.image_url" />
        </div>
        <div class="field">
          <label>错误信息</label>
          <input v-model="props.spec.response!.error" class="input code" placeholder="message" />
        </div>
        <div class="field">
          <label>成功值</label>
          <input v-model="props.spec.response!.success_value" class="input code" placeholder="success" />
        </div>
        <div class="field">
          <label>进行中的值</label>
          <input v-model="props.spec.response!.pending_value" class="input code" placeholder="processing" />
        </div>
        <div class="field">
          <label>失败的值</label>
          <input v-model="props.spec.response!.failed_value" class="input code" placeholder="failed" />
        </div>
      </div>
      <div class="field">
        <label>状态映射（JSON，上游值 → queued/running/succeeded/failed）</label>
        <textarea
          v-model="statusMapText"
          class="textarea code"
          rows="3"
          placeholder='{"processing": "running", "done": "succeeded"}'
          @blur="applyStatusMap"
        />
      </div>
    </template>
  </details>
</template>
