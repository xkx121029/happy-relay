<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import Modal from '../components/Modal.vue'
import { api, type APIKey } from '../api'
import { models, toast } from '../store'
import { copyText, fmtTime, relative } from '../utils'

const list = ref<APIKey[]>([])
const loading = ref(true)
const editing = ref(false)
const saving = ref(false)

function blank() {
  return { id: 0, name: '', models: [] as string[], enabled: true, expires_at: '', remark: '' }
}

const form = reactive(blank())

async function load() {
  loading.value = true
  try {
    const [keys, ms] = await Promise.all([api.listKeys(), api.listModels()])
    list.value = keys
    models.value = ms
  } catch (e: any) {
    toast(e.message || '加载失败', 'error')
  } finally {
    loading.value = false
  }
}

onMounted(load)

const allModelNames = computed(() => models.value.map((m) => m.name))

function openCreate() {
  Object.assign(form, blank())
  editing.value = true
}

function openEdit(k: APIKey) {
  Object.assign(form, {
    id: k.id,
    name: k.name,
    models: [...k.models],
    enabled: k.enabled,
    expires_at: k.expires_at ? k.expires_at.slice(0, 10) : '',
    remark: k.remark,
  })
  editing.value = true
}

function toggleModel(name: string) {
  const i = form.models.indexOf(name)
  if (i >= 0) form.models.splice(i, 1)
  else form.models.push(name)
}

async function save() {
  if (!form.name.trim()) {
    toast('请填写秘钥名称', 'error')
    return
  }
  saving.value = true
  try {
    await api.saveKey({
      id: form.id || undefined,
      name: form.name.trim(),
      models: form.models,
      enabled: form.enabled,
      expires_at: form.expires_at,
      remark: form.remark.trim(),
    })
    toast('秘钥已保存')
    editing.value = false
    await load()
  } catch (e: any) {
    toast(e.message || '保存失败', 'error')
  } finally {
    saving.value = false
  }
}

async function remove(k: APIKey) {
  if (!confirm(`确定删除秘钥「${k.name}」？使用它的客户端将立即失效。`)) return
  try {
    await api.deleteKey(k.id)
    toast('已删除')
    await load()
  } catch (e: any) {
    toast(e.message || '删除失败', 'error')
  }
}

async function copy(k: string) {
  const ok = await copyText(k)
  toast(ok ? '已复制秘钥' : '复制失败', ok ? 'success' : 'error')
}

function mask(k: string) {
  if (k.length <= 14) return k
  return k.slice(0, 10) + '••••' + k.slice(-4)
}

function expired(k: APIKey) {
  return !!k.expires_at && new Date(k.expires_at).getTime() < Date.now()
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h2>秘钥</h2>
        <p>给不同客户端各发一把，可限定能调用哪些模型，也可以设置过期时间。</p>
      </div>
      <div class="row">
        <button class="btn btn-sm" :disabled="loading" @click="load">刷新</button>
        <button class="btn btn-primary btn-sm" @click="openCreate">新建秘钥</button>
      </div>
    </div>

    <div v-if="loading" class="card"><div class="skeleton" style="width: 40%" /></div>

    <div v-else-if="list.length" class="table-wrap">
      <table class="table">
        <thead>
          <tr>
            <th>名称</th>
            <th>秘钥</th>
            <th>可用模型</th>
            <th>调用次数</th>
            <th>状态</th>
            <th>最近使用</th>
            <th style="text-align: right">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="k in list" :key="k.id">
            <td>
              <div style="font-weight: 600">{{ k.name }}</div>
              <div v-if="k.remark" class="muted" style="font-size: 12px">{{ k.remark }}</div>
            </td>
            <td>
              <div class="row" style="gap: 8px">
                <span class="mono dim">{{ mask(k.key) }}</span>
                <button class="btn btn-sm btn-ghost" @click="copy(k.key)">复制</button>
              </div>
            </td>
            <td>
              <span v-if="!k.models.length" class="badge brand">全部模型</span>
              <span v-else class="dim">{{ k.models.length }} 个</span>
            </td>
            <td class="dim">{{ k.calls ?? 0 }}</td>
            <td>
              <span
                class="badge"
                :class="!k.enabled ? '' : expired(k) ? 'warn' : 'ok'"
              >
                <span class="dot" />
                {{ !k.enabled ? '停用' : expired(k) ? '已过期' : '启用' }}
              </span>
              <div v-if="k.expires_at" class="muted" style="font-size: 11.5px; margin-top: 4px">
                {{ k.expires_at.slice(0, 10) }} 到期
              </div>
            </td>
            <td class="muted nowrap">{{ k.last_used_at ? relative(k.last_used_at) : '—' }}</td>
            <td>
              <div class="row" style="justify-content: flex-end; gap: 6px">
                <button class="btn btn-sm btn-ghost" @click="openEdit(k)">编辑</button>
                <button class="btn btn-sm btn-ghost" style="color: var(--danger)" @click="remove(k)">
                  删除
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-else class="card empty">
      <div class="big">还没有秘钥</div>
      <div>创建后把「Base URL + API Key」填进任意 OpenAI 兼容客户端即可使用。</div>
    </div>

    <Modal
      v-if="editing"
      :title="form.id ? '编辑秘钥' : '新建秘钥'"
      :subtitle="form.id ? '' : '保存后会自动生成一把 sk- 开头的随机秘钥。'"
      @close="editing = false"
    >
      <div class="field">
        <label>名称</label>
        <input v-model="form.name" class="input" placeholder="例如：手机上的 ChatBox" />
      </div>

      <div class="field">
        <label>可用模型（不选表示全部模型）</label>
        <div v-if="allModelNames.length" class="chips">
          <span
            v-for="name in allModelNames"
            :key="name"
            class="chip"
            :class="{ active: form.models.includes(name) }"
            @click="toggleModel(name)"
          >
            {{ name }}
          </span>
        </div>
        <div v-else class="hint">还没有模型，先去「模型」页创建。</div>
      </div>

      <div class="field">
        <label>过期时间（可选）</label>
        <input v-model="form.expires_at" type="date" class="input" />
        <div class="hint">留空表示永不过期。</div>
      </div>

      <div class="field">
        <label>备注</label>
        <input v-model="form.remark" class="input" placeholder="用途说明" />
      </div>

      <label class="switch">
        <input v-model="form.enabled" type="checkbox" />
        <span class="track" />
        <span>启用该秘钥</span>
      </label>

      <template #footer>
        <button class="btn" @click="editing = false">取消</button>
        <button class="btn btn-primary" :disabled="saving" @click="save">
          {{ saving ? '保存中…' : '保存' }}
        </button>
      </template>
    </Modal>
  </div>
</template>
