<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import Modal from '../components/Modal.vue'
import { api, type CatalogModel } from '../api'
import { confirmDialog, providers, state, toast } from '../store'
import { capLabel, kindLabel } from '../utils'

const list = ref<CatalogModel[]>([])
const loading = ref(true)
const editing = ref(false)
const saving = ref(false)
const filterProvider = ref(0)
const filterCapability = ref('')

function blank() {
  return {
    id: 0,
    provider_id: providers.value[0]?.id || 0,
    model_id: '',
    capability: 'chat',
    enabled: true,
    remark: '',
    context_length: '',
  }
}

const form = reactive(blank())

const capabilities = computed(
  () =>
    state.meta?.capabilities || [
      { value: 'chat', label: '文本对话' },
      { value: 'image', label: '图片生成' },
      { value: 'video', label: '视频生成' },
      { value: 'embedding', label: '文本向量' },
    ],
)

// fmtK 把上下文长度压缩成易读形式，如 128000 → 128K。
function fmtK(n: number): string {
  if (n >= 1000000) return Math.round(n / 10000) / 100 + 'M'
  if (n >= 1000) return Math.round(n / 1000) + 'K'
  return String(n)
}

const grouped = computed(() => {
  const map = new Map<number, CatalogModel[]>()
  for (const item of list.value) {
    const arr = map.get(item.provider_id) || []
    arr.push(item)
    map.set(item.provider_id, arr)
  }
  return [...map.entries()].map(([providerID, items]) => ({
    providerID,
    providerName: items[0]?.provider_name || `渠道 #${providerID}`,
    providerKind: items[0]?.provider_kind || '',
    items,
  }))
})

async function load() {
  loading.value = true
  try {
    list.value = await api.listCatalog({
      provider_id: filterProvider.value || undefined,
      capability: filterCapability.value || undefined,
    })
  } catch (e: any) {
    toast(e.message || '加载模型库失败', 'error')
  } finally {
    loading.value = false
  }
}

onMounted(async () => {
  try {
    if (!state.meta) state.meta = await api.getMeta()
    if (!providers.value.length) providers.value = await api.listProviders()
  } catch {
    /* 元信息失败不阻塞 */
  }
  await load()
})

function openCreate() {
  if (!providers.value.length) {
    toast('请先在「渠道」里创建上游', 'error')
    return
  }
  Object.assign(form, blank())
  editing.value = true
}

function openEdit(item: CatalogModel) {
  Object.assign(form, {
    id: item.id,
    provider_id: item.provider_id,
    model_id: item.model_id,
    capability: item.capability,
    enabled: item.enabled,
    remark: item.remark,
    context_length: item.context_length ? String(item.context_length) : '',
  })
  editing.value = true
}

async function save() {
  if (!form.model_id.trim()) {
    toast('请填写上游模型名', 'error')
    return
  }
  if (!form.provider_id) {
    toast('请选择所属渠道', 'error')
    return
  }
  saving.value = true
  try {
    await api.saveCatalog({
      id: form.id || undefined,
      provider_id: form.provider_id,
      model_id: form.model_id.trim(),
      capability: form.capability,
      enabled: form.enabled,
      remark: form.remark.trim(),
      context_length: Math.max(0, Math.round(Number(form.context_length) || 0)),
      source: 'manual',
    })
    toast('已保存到模型库')
    editing.value = false
    await load()
  } catch (e: any) {
    toast(e.message || '保存失败', 'error')
  } finally {
    saving.value = false
  }
}

async function remove(item: CatalogModel) {
  if (item.used_by) {
    toast(`该模型已被 ${item.used_by} 个对外模型引用，请先解除引用`, 'error')
    return
  }
  const ok = await confirmDialog({
    title: '从模型库移除',
    message: `确定从模型库移除「${item.model_id}」？`,
    confirmText: '移除',
  })
  if (!ok) return
  try {
    await api.deleteCatalog(item.id)
    toast('已移除')
    await load()
  } catch (e: any) {
    toast(e.message || '移除失败', 'error')
  }
}

function capBadge(capability: string) {
  return { chat: 'brand', image: 'warn', video: 'ok', embedding: '' }[capability] || ''
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h2>模型库</h2>
        <p>
          渠道探测到的上游模型会汇总在这里，<strong>仅作为配置候选，不会直接对外开放</strong>；
          需要对外提供时，在「模型」里引用它即可。
        </p>
      </div>
      <div class="row">
        <button class="btn btn-sm" :disabled="loading" @click="load">刷新</button>
        <button class="btn btn-primary btn-sm" @click="openCreate">手动登记</button>
      </div>
    </div>

    <div class="toolbar">
      <select v-model.number="filterProvider" class="select" style="width: 180px" @change="load">
        <option :value="0">全部渠道</option>
        <option v-for="p in providers" :key="p.id" :value="p.id">{{ p.name }}</option>
      </select>
      <select v-model="filterCapability" class="select" style="width: 160px" @change="load">
        <option value="">全部能力</option>
        <option v-for="c in capabilities" :key="c.value" :value="c.value">{{ c.label }}</option>
      </select>
      <span class="muted" style="font-size: 12.5px">共 {{ list.length }} 个可用模型</span>
    </div>

    <div v-if="loading" class="card"><div class="skeleton" style="height: 160px" /></div>

    <div v-else-if="list.length" style="display: flex; flex-direction: column; gap: 16px">
      <div v-for="group in grouped" :key="group.providerID" class="card" style="padding: 0">
        <div class="row" style="padding: 14px 16px; border-bottom: 1px solid var(--border)">
          <strong>{{ group.providerName }}</strong>
          <span class="badge">{{ kindLabel(group.providerKind) }}</span>
          <span class="muted" style="font-size: 12.5px">{{ group.items.length }} 个模型</span>
        </div>
        <div class="table-wrap" style="border: none">
          <table class="table">
            <thead>
              <tr>
                <th>上游模型名</th>
                <th>能力</th>
                <th>上下文</th>
                <th>来源</th>
                <th class="num">被引用</th>
                <th>状态</th>
                <th style="text-align: right">操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="item in group.items" :key="item.id">
                <td>
                  <div class="mono">{{ item.model_id }}</div>
                  <div v-if="item.remark" class="muted" style="font-size: 11.5px">{{ item.remark }}</div>
                </td>
                <td><span class="badge" :class="capBadge(item.capability)">{{ capLabel(item.capability) }}</span></td>
                <td class="dim mono">{{ item.context_length ? fmtK(item.context_length) : '—' }}</td>
                <td>
                  <span class="badge" :class="item.source === 'probe' ? 'brand' : ''">
                    {{ item.source === 'probe' ? '探测' : '手工' }}
                  </span>
                </td>
                <td class="num dim">{{ item.used_by ? item.used_by + ' 个' : '—' }}</td>
                <td>
                  <span class="badge" :class="item.enabled ? 'ok' : ''">
                    <span class="dot" />{{ item.enabled ? '可用' : '停用' }}
                  </span>
                </td>
                <td>
                  <div class="row" style="justify-content: flex-end; gap: 4px">
                    <button class="btn btn-sm btn-ghost" @click="openEdit(item)">编辑</button>
                    <button class="btn btn-sm btn-ghost" style="color: var(--danger)" @click="remove(item)">
                      移除
                    </button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>

    <div v-else class="card empty">
      <div class="empty-icon">▦</div>
      <div class="big">模型库还是空的</div>
      <div>到「渠道」里点「获取模型」，把探测到的模型加入模型库。</div>
      <div class="empty-cta">
        <RouterLink class="btn btn-sm" to="/providers">去渠道探测</RouterLink>
        <button class="btn btn-sm btn-ghost" @click="openCreate">手动登记</button>
      </div>
    </div>

    <Modal
      v-if="editing"
      :title="form.id ? '编辑模型库条目' : '登记上游模型'"
      subtitle="模型库只是候选清单，不会直接对外提供服务。"
      submit-on-enter
      :busy="saving"
      @submit="save"
      @close="editing = false"
    >
      <div class="field">
        <label>所属渠道</label>
        <select v-model.number="form.provider_id" class="select">
          <option v-for="p in providers" :key="p.id" :value="p.id">{{ p.name }}</option>
        </select>
      </div>
      <div class="field">
        <label>上游模型名</label>
        <input v-model="form.model_id" class="input code" placeholder="gpt-4o-mini" />
      </div>
      <div class="field">
        <label>能力</label>
        <select v-model="form.capability" class="select">
          <option v-for="c in capabilities" :key="c.value" :value="c.value">{{ c.label }}</option>
        </select>
      </div>
      <div class="field">
        <label>上下文窗口（token）</label>
        <input
          v-model="form.context_length"
          class="input"
          type="number"
          min="0"
          placeholder="探测不到时可手工填写"
        />
        <div class="hint">对外模型未声明上下文时，会回退到这里的值。</div>
      </div>
      <div class="field">
        <label>备注</label>
        <input v-model="form.remark" class="input" placeholder="可选说明" />
      </div>
      <label class="switch">
        <input v-model="form.enabled" type="checkbox" />
        <span class="track" />
        <span>标记为可用</span>
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
