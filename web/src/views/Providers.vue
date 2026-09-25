<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import Modal from '../components/Modal.vue'
import { api, type ProbeResult, type Provider } from '../api'
import { providers, state, toast } from '../store'
import { copyText, kindLabel, strategyLabel } from '../utils'

const loading = ref(true)
const editing = ref(false)
const saving = ref(false)
const testing = ref(false)

const probeOpen = ref(false)
const probeTitle = ref('')
const probeResults = ref<ProbeResult[]>([])

interface HeaderRow {
  k: string
  v: string
}

function blank() {
  return {
    id: 0,
    name: '',
    kind: 'openai',
    base_urls: [''],
    api_keys: [''],
    headers: [] as HeaderRow[],
    strategy: 'round_robin',
    timeout_sec: 600,
    enabled: true,
    remark: '',
  }
}

const form = reactive(blank())

const kinds = computed(
  () =>
    state.meta?.kinds || [
      { value: 'openai', label: 'OpenAI 兼容' },
      { value: 'gemini', label: 'Google Gemini' },
      { value: 'anthropic', label: 'Anthropic Claude' },
      { value: 'template', label: '自定义模板' },
    ],
)

const strategies = computed(
  () =>
    state.meta?.strategies || [
      { value: 'round_robin', label: '轮询' },
      { value: 'failover', label: '主备' },
      { value: 'random', label: '随机' },
    ],
)

async function load() {
  loading.value = true
  try {
    providers.value = await api.listProviders()
  } catch (e: any) {
    toast(e.message || '加载渠道失败', 'error')
  } finally {
    loading.value = false
  }
}

onMounted(load)

function openCreate() {
  Object.assign(form, blank())
  form.headers = []
  editing.value = true
}

function openEdit(p: Provider) {
  Object.assign(form, {
    id: p.id,
    name: p.name,
    kind: p.kind,
    base_urls: p.base_urls.length ? [...p.base_urls] : [''],
    api_keys: p.api_keys.length ? [...p.api_keys] : [''],
    headers: Object.entries(p.headers || {}).map(([k, v]) => ({ k, v })),
    strategy: p.strategy,
    timeout_sec: p.timeout_sec,
    enabled: p.enabled,
    remark: p.remark,
  })
  editing.value = true
}

function addRow(list: string[]) {
  list.push('')
}
function removeRow(list: string[], i: number) {
  list.splice(i, 1)
  if (list.length === 0) list.push('')
}

function clean(list: string[]) {
  return list.map((s) => s.trim()).filter(Boolean)
}

async function save() {
  if (!form.name.trim()) {
    toast('请填写渠道名称', 'error')
    return
  }
  const headers: Record<string, string> = {}
  for (const h of form.headers) {
    if (h.k.trim()) headers[h.k.trim()] = h.v.trim()
  }
  saving.value = true
  try {
    await api.saveProvider({
      id: form.id || undefined,
      name: form.name.trim(),
      kind: form.kind,
      base_urls: clean(form.base_urls),
      api_keys: clean(form.api_keys),
      headers,
      strategy: form.strategy,
      timeout_sec: Number(form.timeout_sec) || 600,
      enabled: form.enabled,
      remark: form.remark.trim(),
    })
    toast('渠道已保存')
    editing.value = false
    await load()
  } catch (e: any) {
    toast(e.message || '保存失败', 'error')
  } finally {
    saving.value = false
  }
}

async function remove(p: Provider) {
  if (!confirm(`确定删除渠道「${p.name}」？其下的模型也会一并删除。`)) return
  try {
    await api.deleteProvider(p.id)
    toast('已删除')
    await load()
  } catch (e: any) {
    toast(e.message || '删除失败', 'error')
  }
}

async function testSaved(p: Provider) {
  testing.value = true
  probeTitle.value = `连通性测试 · ${p.name}`
  probeResults.value = []
  probeOpen.value = true
  try {
    const res = await api.testProvider({ id: p.id })
    probeResults.value = res.results || []
  } catch (e: any) {
    toast(e.message || '测试失败', 'error')
    probeOpen.value = false
  } finally {
    testing.value = false
  }
}

async function testForm() {
  const urls = clean(form.base_urls)
  if (!urls.length) {
    toast('请先填写入口地址', 'error')
    return
  }
  const key = clean(form.api_keys)[0] || ''
  testing.value = true
  probeTitle.value = `连通性测试 · ${form.name || '未命名渠道'}`
  probeResults.value = []
  probeOpen.value = true
  try {
    const all: ProbeResult[] = []
    for (const url of urls) {
      try {
        const res = await api.testProvider({ kind: form.kind, base_url: url, api_key: key })
        all.push(...(res.results || []))
      } catch (e: any) {
        all.push({ url, ok: false, status: 0, latency_ms: 0, detail: e.message || '请求失败' })
      }
    }
    probeResults.value = all
  } finally {
    testing.value = false
  }
}

async function copyKey(k: string) {
  const ok = await copyText(k)
  toast(ok ? '已复制密钥' : '复制失败', ok ? 'success' : 'error')
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h2>渠道</h2>
        <p>一个渠道可配置多个入口地址与多个密钥，实际调用是两者的组合。</p>
      </div>
      <div class="row">
        <button class="btn btn-sm" :disabled="loading" @click="load">刷新</button>
        <button class="btn btn-primary btn-sm" @click="openCreate">新建渠道</button>
      </div>
    </div>

    <div v-if="loading" class="card"><div class="skeleton" style="width: 40%" /></div>

    <div v-else-if="providers.length" class="table-wrap">
      <table class="table">
        <thead>
          <tr>
            <th>名称</th>
            <th>类型</th>
            <th>入口 / 密钥</th>
            <th>策略</th>
            <th>模型</th>
            <th>状态</th>
            <th style="text-align: right">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="p in providers" :key="p.id">
            <td>
              <div style="font-weight: 600">{{ p.name }}</div>
              <div v-if="p.remark" class="muted clamp-1" style="font-size: 12px; max-width: 220px">
                {{ p.remark }}
              </div>
            </td>
            <td><span class="badge">{{ kindLabel(p.kind) }}</span></td>
            <td class="muted nowrap">{{ p.base_urls.length }} 入口 / {{ p.api_keys.length }} 密钥</td>
            <td class="dim">{{ strategyLabel(p.strategy) }}</td>
            <td class="dim">{{ p.model_count ?? 0 }}</td>
            <td>
              <span class="badge" :class="p.enabled ? 'ok' : ''">
                <span class="dot" />{{ p.enabled ? '启用' : '停用' }}
              </span>
            </td>
            <td>
              <div class="row" style="justify-content: flex-end; gap: 6px">
                <button class="btn btn-sm btn-ghost" @click="testSaved(p)">测试</button>
                <button class="btn btn-sm btn-ghost" @click="openEdit(p)">编辑</button>
                <button class="btn btn-sm btn-ghost" style="color: var(--danger)" @click="remove(p)">
                  删除
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-else class="card empty">
      <div class="big">还没有渠道</div>
      <div>先接入一个上游服务商，再在「模型」里把它的模型暴露出去。</div>
      <div style="margin-top: 16px"><button class="btn btn-primary" @click="openCreate">新建渠道</button></div>
    </div>

    <!-- 编辑弹窗 -->
    <Modal
      v-if="editing"
      wide
      :title="form.id ? '编辑渠道' : '新建渠道'"
      subtitle="入口地址与密钥会做笛卡尔积组合，配合调度策略使用。"
      @close="editing = false"
    >
      <div class="grid cols-2" style="gap: 0 18px">
        <div class="field">
          <label>渠道名称</label>
          <input v-model="form.name" class="input" placeholder="例如：主力中转" />
        </div>
        <div class="field">
          <label>渠道类型</label>
          <select v-model="form.kind" class="select">
            <option v-for="k in kinds" :key="k.value" :value="k.value">{{ k.label }}</option>
          </select>
          <div v-if="kinds.find((k) => k.value === form.kind)?.hint" class="hint">
            {{ kinds.find((k) => k.value === form.kind)?.hint }}
          </div>
        </div>
      </div>

      <div class="field">
        <label>入口地址（Base URL）</label>
        <div v-for="(_, i) in form.base_urls" :key="'u' + i" class="list-row">
          <input v-model="form.base_urls[i]" class="input code" placeholder="https://api.example.com/v1" />
          <button class="btn btn-sm btn-ghost" @click="removeRow(form.base_urls, i)">移除</button>
        </div>
        <button class="btn btn-sm" @click="addRow(form.base_urls)">+ 添加入口</button>
        <div class="hint">OpenAI 兼容类型可只填域名，会自动补 /v1；留空则使用官方默认地址。</div>
      </div>

      <div class="field">
        <label>密钥</label>
        <div v-for="(_, i) in form.api_keys" :key="'k' + i" class="list-row">
          <input v-model="form.api_keys[i]" class="input code" placeholder="sk-..." />
          <button class="btn btn-sm btn-ghost" @click="copyKey(form.api_keys[i])">复制</button>
          <button class="btn btn-sm btn-ghost" @click="removeRow(form.api_keys, i)">移除</button>
        </div>
        <button class="btn btn-sm" @click="addRow(form.api_keys)">+ 添加密钥</button>
      </div>

      <div class="field">
        <label>自定义请求头（可选）</label>
        <div v-for="(h, i) in form.headers" :key="'h' + i" class="list-row">
          <input v-model="h.k" class="input code" placeholder="Header 名" />
          <input v-model="h.v" class="input code" placeholder="值" />
          <button class="btn btn-sm btn-ghost" @click="form.headers.splice(i, 1)">移除</button>
        </div>
        <button class="btn btn-sm" @click="form.headers.push({ k: '', v: '' })">+ 添加请求头</button>
      </div>

      <div class="grid cols-2" style="gap: 0 18px">
        <div class="field">
          <label>调度策略</label>
          <select v-model="form.strategy" class="select">
            <option v-for="s in strategies" :key="s.value" :value="s.value">{{ s.label }}</option>
          </select>
          <div class="hint">{{ strategies.find((s) => s.value === form.strategy)?.hint }}</div>
        </div>
        <div class="field">
          <label>单次请求超时（秒）</label>
          <input v-model.number="form.timeout_sec" type="number" min="1" class="input" />
        </div>
      </div>

      <div class="field">
        <label>备注</label>
        <input v-model="form.remark" class="input" placeholder="可选说明" />
      </div>

      <label class="switch">
        <input v-model="form.enabled" type="checkbox" />
        <span class="track" />
        <span>启用该渠道</span>
      </label>

      <template #footer>
        <button class="btn" :disabled="testing" @click="testForm">
          <span v-if="testing" class="spinner" />
          <span v-else>测试连通性</span>
        </button>
        <div class="spacer" />
        <button class="btn" @click="editing = false">取消</button>
        <button class="btn btn-primary" :disabled="saving" @click="save">
          {{ saving ? '保存中…' : '保存' }}
        </button>
      </template>
    </Modal>

    <!-- 探测结果 -->
    <Modal v-if="probeOpen" :title="probeTitle" subtitle="逐个入口地址的探测结果" @close="probeOpen = false">
      <div v-if="testing" class="row"><span class="spinner" /> <span class="muted">正在探测…</span></div>
      <div v-else style="display: flex; flex-direction: column; gap: 12px">
        <div v-for="(r, i) in probeResults" :key="i" class="card" style="padding: 14px">
          <div class="row" style="justify-content: space-between">
            <span class="mono clamp-1" style="max-width: 60%">{{ r.url }}</span>
            <span class="badge" :class="r.ok ? 'ok' : 'danger'">{{ r.ok ? '正常' : '失败' }}</span>
          </div>
          <div class="muted" style="font-size: 12.5px; margin-top: 6px">
            {{ r.detail }}
            <span v-if="r.status">· HTTP {{ r.status }}</span>
            <span> · {{ r.latency_ms }} ms</span>
          </div>
          <details v-if="r.models && r.models.length" class="expander">
            <summary>探测到 {{ r.models.length }} 个模型</summary>
            <div class="mono" style="font-size: 12px; max-height: 200px; overflow: auto">
              <div v-for="m in r.models" :key="m">{{ m }}</div>
            </div>
          </details>
        </div>
        <div v-if="!probeResults.length" class="muted">暂无结果。</div>
      </div>
    </Modal>
  </div>
</template>
