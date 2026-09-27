<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import Modal from '../components/Modal.vue'
import TemplateEditor from '../components/TemplateEditor.vue'
import { api, type ModelConfig } from '../api'
import { models, providers, state, toast } from '../store'
import { prettyJSON } from '../utils'

const systemModel = ref('')
const providerId = ref(0)
const generating = ref(false)
const saving = ref(false)
const trying = ref(false)

const form = reactive({
  description: '',
  base_url: '',
  sample_request: '',
  sample_response: '',
  capability: 'video',
})

const draft = ref<any | null>(null)
const rawReply = ref('')
const tryResult = ref<any | null>(null)
const tryForm = reactive({ upstream_name: '', prompt: '', duration: 5, size: '' })
const showRaw = ref(false)

async function load() {
  try {
    if (!state.meta) state.meta = await api.getMeta()
    const [settings, ms, ps] = await Promise.all([
      api.getSettings(),
      api.listModels(),
      api.listProviders(),
    ])
    systemModel.value = settings.system_model || ''
    models.value = ms
    providers.value = ps
    if (ps.length) providerId.value = ps[0].id
  } catch (e: any) {
    toast(e.message || '加载失败', 'error')
  }
}

onMounted(load)

const capOptions = computed(() => [
  { value: 'video', label: '视频生成' },
  { value: 'image', label: '图片生成' },
  { value: 'chat', label: '文本对话' },
])

function normalizeDraft(d: any): any {
  const out = { ...d }
  if (!out.capability) out.capability = form.capability
  if (!out.name) out.name = ''
  if (!out.upstream_name) out.upstream_name = ''
  if (out.capability === 'video') {
    if (!out.video) out.video = {}
    if (!out.video.response) out.video.response = {}
    if (out.video_query && !out.video_query.response) out.video_query.response = {}
  } else if (out.capability === 'image') {
    if (!out.image) out.image = {}
    if (!out.image.response) out.image.response = {}
  } else {
    if (!out.chat) out.chat = {}
    if (!out.chat.response) out.chat.response = {}
  }
  return out
}

async function generate() {
  if (!form.description.trim() && !form.sample_request.trim()) {
    toast('请至少填写接口描述或示例请求', 'error')
    return
  }
  generating.value = true
  draft.value = null
  rawReply.value = ''
  try {
    const res = await api.connectorGenerate({ ...form })
    draft.value = normalizeDraft(res.draft || {})
    rawReply.value = res.raw_reply || ''
    if (draft.value?.parse_error) {
      toast('AI 未返回可识别的 JSON，请参考原文手工调整', 'error')
    } else {
      toast('已生成草案，请检查后验证或保存')
    }
  } catch (e: any) {
    toast(e.message || '生成失败', 'error')
  } finally {
    generating.value = false
  }
}

function buildConfig(): ModelConfig {
  const d = draft.value
  const config: ModelConfig = {}
  if (!d) return config
  if (d.remark) config.system_prompt = undefined
  if (d.capability === 'video') {
    if (d.video && Object.keys(d.video).length) config.video = d.video
    if (d.video_query && Object.keys(d.video_query).length) config.video_query = d.video_query
  } else if (d.capability === 'image') {
    if (d.image && Object.keys(d.image).length) config.image = d.image
  } else {
    if (d.chat && Object.keys(d.chat).length) config.chat = d.chat
  }
  return config
}

async function tryIt() {
  const d = draft.value
  if (!d) return
  if (!providerId.value) {
    toast('请选择用于验证的渠道', 'error')
    return
  }
  const spec = d.capability === 'video' ? d.video : d.capability === 'image' ? d.image : d.chat
  if (!spec || !spec.path) {
    toast('请先填写模板路径', 'error')
    return
  }
  trying.value = true
  tryResult.value = null
  try {
    const res = await api.connectorTry({
      provider_id: providerId.value,
      upstream_name: tryForm.upstream_name || d.upstream_name,
      prompt: tryForm.prompt,
      duration: tryForm.duration,
      size: tryForm.size,
      spec,
    })
    tryResult.value = res
    toast(res.ok ? '验证通过，上游已返回响应' : '验证失败', res.ok ? 'success' : 'error')
  } catch (e: any) {
    toast(e.message || '验证失败', 'error')
  } finally {
    trying.value = false
  }
}

async function saveAsModel() {
  const d = draft.value
  if (!d) return
  if (!d.name) {
    toast('请填写对外模型名', 'error')
    return
  }
  if (!providerId.value) {
    toast('请选择要挂载的渠道', 'error')
    return
  }
  saving.value = true
  try {
    await api.saveModel({
      provider_id: providerId.value,
      name: d.name,
      upstream_name: d.upstream_name || d.name,
      capability: d.capability || 'video',
      config: buildConfig(),
      enabled: true,
      remark: d.remark || '',
    })
    toast('已保存为新模型')
    await load()
  } catch (e: any) {
    toast(e.message || '保存失败', 'error')
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h2>连接器生成器</h2>
        <p>把一段接口说明交给系统级 AI，自动翻译成可用的上游适配模板，并可当场验证。</p>
      </div>
    </div>

    <div v-if="!systemModel" class="card notice-warn" style="margin-bottom: 16px">
      <div class="notice-title">尚未指定系统级 AI</div>
      <div class="notice-body">
        连接器生成器需要一个文本对话模型来理解接口说明。请先到
        <RouterLink to="/settings">设置</RouterLink> 里选择一个「系统级 AI」。
      </div>
    </div>

    <div v-else-if="!providers.length" class="card notice-warn" style="margin-bottom: 16px">
      <div class="notice-title">还没有可挂载的渠道</div>
      <div class="notice-body">
        生成的模板最终要挂到某个渠道上。请先到
        <RouterLink to="/providers">渠道</RouterLink> 添加一个上游渠道，再回来生成。
      </div>
    </div>

    <div class="grid" style="grid-template-columns: 1fr 1fr; gap: 16px; align-items: start">
      <!-- 输入 -->
      <div class="card">
        <h3 class="card-title">接口信息</h3>
        <div class="field">
          <label>接口描述</label>
          <textarea
            v-model="form.description"
            class="textarea"
            rows="4"
            placeholder="用自然语言描述这个上游怎么调：提交任务的地址、参数、返回结构……"
          />
        </div>
        <div class="grid cols-2" style="gap: 0 14px">
          <div class="field">
            <label>上游地址（参考）</label>
            <input v-model="form.base_url" class="input code" placeholder="https://api.example.com" />
          </div>
          <div class="field">
            <label>目标能力</label>
            <select v-model="form.capability" class="select">
              <option v-for="c in capOptions" :key="c.value" :value="c.value">{{ c.label }}</option>
            </select>
          </div>
        </div>
        <div class="field">
          <label>示例请求（curl 或 JSON）</label>
          <textarea v-model="form.sample_request" class="textarea code" rows="4" placeholder="curl -X POST ..." />
        </div>
        <div class="field">
          <label>示例响应</label>
          <textarea v-model="form.sample_response" class="textarea code" rows="4" placeholder='{"data": {"task_id": "..."}}' />
        </div>
        <button
          class="btn btn-primary btn-block"
          :class="{ 'is-loading': generating }"
          :disabled="generating || !systemModel"
          @click="generate"
        >
          {{ generating ? '生成中…' : '生成模板草案' }}
        </button>
        <div class="hint" style="text-align: center">系统级 AI：{{ systemModel || '未指定' }}</div>
      </div>

      <!-- 结果 -->
      <div class="card">
        <h3 class="card-title">草案与验证</h3>

        <div v-if="!draft" class="empty">
          <div class="big">还没有草案</div>
          <div>填写左侧接口信息后点击「生成模板草案」。</div>
        </div>

        <template v-else>
          <div class="field">
            <label>对外模型名</label>
            <input v-model="draft.name" class="input code" placeholder="kling-video" />
          </div>
          <div class="field">
            <label>上游真实模型名</label>
            <input v-model="draft.upstream_name" class="input code" />
          </div>
          <div class="grid cols-2" style="gap: 0 14px">
            <div class="field">
              <label>能力</label>
              <select v-model="draft.capability" class="select">
                <option value="video">视频生成</option>
                <option value="image">图片生成</option>
                <option value="chat">文本对话</option>
              </select>
            </div>
            <div class="field">
              <label>备注</label>
              <input v-model="draft.remark" class="input" />
            </div>
          </div>

          <div v-if="draft.parse_error" class="badge danger" style="margin-bottom: 12px">
            {{ draft.parse_error }}
          </div>

          <template v-if="draft.capability === 'video'">
            <TemplateEditor v-if="draft.video" :spec="draft.video" label="视频提交模板" show-response />
            <div v-if="!draft.video_query" class="row" style="margin-top: 10px">
              <button class="btn btn-sm" @click="draft.video_query = { method: 'GET', path: '', response: {} }">
                + 添加查询模板（异步任务）
              </button>
            </div>
            <TemplateEditor
              v-if="draft.video_query"
              :spec="draft.video_query"
              label="视频查询模板"
              show-response
            />
          </template>
          <template v-else-if="draft.capability === 'image'">
            <TemplateEditor v-if="draft.image" :spec="draft.image" label="图片模板" show-response />
          </template>
          <template v-else>
            <TemplateEditor v-if="draft.chat" :spec="draft.chat" label="对话模板" show-response />
          </template>

          <div class="field" style="margin-top: 16px">
            <label>挂载到渠道</label>
            <select v-model.number="providerId" class="select">
              <option v-for="p in providers" :key="p.id" :value="p.id">{{ p.name }}</option>
            </select>
          </div>

          <div class="row" style="justify-content: flex-end; gap: 8px">
            <button class="btn btn-sm" :disabled="saving" @click="saveAsModel">
              {{ saving ? '保存中…' : '保存为模型' }}
            </button>
            <button class="btn btn-sm btn-ghost" @click="showRaw = true">查看 AI 原文</button>
          </div>

          <details class="expander" style="margin-top: 14px">
            <summary>调试验证</summary>
            <div class="grid cols-2" style="gap: 0 14px">
              <div class="field">
                <label>上游模型名（验证用）</label>
                <input v-model="tryForm.upstream_name" class="input code" :placeholder="draft.upstream_name" />
              </div>
              <div class="field">
                <label>时长 / 尺寸</label>
                <div class="row">
                  <input v-model.number="tryForm.duration" type="number" class="input" style="width: 90px" />
                  <input v-model="tryForm.size" class="input" placeholder="尺寸（可选）" />
                </div>
              </div>
            </div>
            <div class="field">
              <label>测试提示词</label>
              <input v-model="tryForm.prompt" class="input" placeholder="a cat walking on the beach" />
            </div>
            <button class="btn btn-sm" :disabled="trying" @click="tryIt">
              {{ trying ? '验证中…' : '用该模板真实调用一次' }}
            </button>
            <div v-if="tryResult" style="margin-top: 12px">
              <span class="badge" :class="tryResult.ok ? 'ok' : 'danger'">
                {{ tryResult.ok ? '调用成功' : '调用失败' }}
              </span>
              <pre class="code-block" style="margin-top: 8px">{{
                prettyJSON(tryResult.ok ? tryResult.response : tryResult.error)
              }}</pre>
            </div>
          </details>
        </template>
      </div>
    </div>

    <Modal v-if="showRaw" wide title="AI 原始回复" @close="showRaw = false">
      <pre class="code-block">{{ rawReply || '（空）' }}</pre>
    </Modal>
  </div>
</template>
