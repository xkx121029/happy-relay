<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import PresetWizard from '../components/PresetWizard.vue'
import { api, type ModelPreset } from '../api'
import { models, providers, state, toast } from '../store'
import { capLabel, kindLabel } from '../utils'

// 内置模型页：永久免费平台的独立目录页。
// 从「模型」页弹窗升级成页面——它本身就是一条完整的接入路径，值得一个入口。
const loading = ref(true)
const groupBusy = ref('')
const presetBusy = ref('')
const activeGroup = ref('')
const wizardGroup = ref<PresetGroup | null>(null)

// realnameFilter：全部 | 免实名 | 需实名。海外平台基本免实名，大陆平台普遍要实名。
const realnameFilter = ref<'all' | 'free' | 'realname'>('all')

const modelPresets = computed<ModelPreset[]>(() => state.meta?.model_presets || [])

const MODALITY_LABELS: Record<string, string> = {
  text: '文本',
  image: '图片',
  video: '视频',
  file: '文档',
  audio: '音频',
}

function modalityText(mods?: string[]): string {
  if (!mods?.length) return '—'
  return mods.map((m) => MODALITY_LABELS[m] || m).join(' + ')
}

interface PresetGroup {
  name: string
  kind: string
  baseURL: string
  signupURL: string
  note: string
  /** 开通该平台是否需要实名认证 */
  realname: boolean
  items: ModelPreset[]
}

const allGroups = computed<PresetGroup[]>(() => {
  const map = new Map<string, PresetGroup>()
  for (const p of modelPresets.value) {
    let g = map.get(p.provider_name)
    if (!g) {
      g = {
        name: p.provider_name,
        kind: p.provider_kind,
        baseURL: p.provider_base_url || '',
        signupURL: p.signup_url || '',
        note: p.provider_note || '',
        realname: !!p.realname_required,
        items: [],
      }
      map.set(p.provider_name, g)
    }
    g.items.push(p)
  }
  return [...map.values()]
})

const presetGroups = computed<PresetGroup[]>(() => {
  if (realnameFilter.value === 'free') return allGroups.value.filter((g) => !g.realname)
  if (realnameFilter.value === 'realname') return allGroups.value.filter((g) => g.realname)
  return allGroups.value
})

const counts = computed(() => ({
  all: allGroups.value.length,
  free: allGroups.value.filter((g) => !g.realname).length,
  realname: allGroups.value.filter((g) => g.realname).length,
}))

// 切换实名筛选后，当前选中的平台可能被过滤掉，回落到列表第一个。
watch(realnameFilter, () => {
  if (!presetGroups.value.some((g) => g.name === activeGroup.value)) {
    activeGroup.value = presetGroups.value[0]?.name || ''
  }
})

// 已建好的同名渠道，用于在分组里直接把预设挂上去。
function providerFor(g: PresetGroup): number {
  return providers.value.find((p) => p.name === g.name)?.id || 0
}

// probeOk 缓存本次会话里各渠道的连通测试结论：通过过的渠道不再重复探测。
// 这是「测试通过才能入模型列表」的执行点——添加前先真实探测一次。
const probeOk = ref<Record<number, boolean>>({})

// ensureTested 确保渠道配置完成且连通测试通过，否则返回 false 并给出原因。
async function ensureTested(pid: number): Promise<boolean> {
  if (probeOk.value[pid]) return true
  const prov = providers.value.find((p) => p.id === pid)
  if (!prov) return false
  if (!prov.api_keys?.length) {
    toast('该渠道还没配置密钥，请先走「配置向导」完成配置', 'error')
    return false
  }
  const r = await api.testProvider({ id: pid })
  const ok = !!r.results?.[0]?.ok
  probeOk.value[pid] = ok
  if (!ok) {
    toast('连通测试未通过：测试通过后才能把模型加入列表，可到「渠道」页排查', 'error')
  }
  return ok
}

// 该预设对应的对外模型是否已创建（按模型名比对）。
function added(p: ModelPreset): boolean {
  return models.value.some((m) => m.name === p.name)
}

// 一键添加该平台的全部模型。
async function useGroup(g: PresetGroup) {
  const pid = providerFor(g)
  if (!pid) {
    toast(`还没有「${g.name}」渠道，请点「配置向导」完成接入`, 'error')
    return
  }
  groupBusy.value = g.name
  try {
    if (!(await ensureTested(pid))) return
    let ok = 0
    let existed = 0
    const failed: string[] = []
    for (const p of g.items) {
      try {
        const r = await api.createModelFromPreset({ preset_id: p.id, provider_id: pid, name: p.name })
        if ((r as any).existed) existed++
        else ok++
      } catch (e: any) {
        failed.push(`${p.name}：${e.message || '创建失败'}`)
      }
    }
    if (ok) toast(`已从「${g.name}」添加 ${ok} 个模型${existed ? `，${existed} 个已存在` : ''}`)
    else if (existed) toast(`「${g.name}」的 ${existed} 个模型都已存在`, 'info')
    if (failed.length) toast(`${failed.length} 个模型创建失败：${failed[0]}`, 'error')
    await load()
  } finally {
    groupBusy.value = ''
  }
}

async function usePreset(p: ModelPreset, g: PresetGroup) {
  const pid = providerFor(g)
  if (!pid) {
    toast('请先通过「配置向导」创建渠道', 'error')
    return
  }
  presetBusy.value = p.id
  try {
    if (!(await ensureTested(pid))) return
    const r = await api.createModelFromPreset({ preset_id: p.id, provider_id: pid, name: p.name })
    if ((r as any).existed) toast(`模型「${(r as any).name}」已存在`, 'info')
    else toast(`已创建模型「${(r as any).name}」`)
    await load()
  } catch (e: any) {
    toast(e.message || '创建失败', 'error')
  } finally {
    presetBusy.value = ''
  }
}

async function load() {
  loading.value = true
  try {
    const meta = state.meta || (await api.getMeta())
    state.meta = meta
    // 已添加标记要覆盖「添加了但当前不可用」的模型，因此拿全量列表。
    const [p, m] = await Promise.all([api.listProviders(), api.listModels({ all: true })])
    providers.value = p
    models.value = m
    if (!activeGroup.value || !presetGroups.value.some((g) => g.name === activeGroup.value)) {
      activeGroup.value = presetGroups.value[0]?.name || ''
    }
  } catch (e: any) {
    toast(e.message || '加载失败', 'error')
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h2>内置模型</h2>
        <p>
          全部为「永久免费、无需充值」的平台目录。点「配置向导」从申请密钥一路走到模型入库；
          渠道配置完成且连通测试通过后，模型才会纳入「模型」列表。
        </p>
      </div>
      <div class="row">
        <button class="btn btn-sm" :disabled="loading" @click="load">刷新</button>
        <RouterLink class="btn btn-sm" to="/models">去模型列表</RouterLink>
      </div>
    </div>

    <div class="row" style="gap: 6px; margin-bottom: 14px; flex-wrap: wrap">
      <span class="chip" :class="{ active: realnameFilter === 'all' }" @click="realnameFilter = 'all'">
        全部平台（{{ counts.all }}）
      </span>
      <span class="chip" :class="{ active: realnameFilter === 'free' }" @click="realnameFilter = 'free'">
        无需实名（{{ counts.free }}）
      </span>
      <span class="chip" :class="{ active: realnameFilter === 'realname' }" @click="realnameFilter = 'realname'">
        需实名认证（{{ counts.realname }}）
      </span>
    </div>

    <div v-if="loading" class="card"><div class="skeleton" style="height: 200px" /></div>

    <div v-else-if="presetGroups.length" class="preset-groups">
      <aside class="preset-rail">
        <button
          v-for="g in presetGroups"
          :key="g.name"
          class="rail-item"
          :class="{ active: activeGroup === g.name }"
          @click="activeGroup = g.name"
        >
          <span class="rail-text">
            <span class="rail-label">{{ g.name }}</span>
            <span class="rail-desc">{{ g.items.length }} 个模型</span>
          </span>
          <span v-if="!providerFor(g)" class="rail-dot" title="尚未创建该渠道" />
        </button>
      </aside>

      <div class="preset-body">
        <template v-for="g in presetGroups" :key="g.name">
          <div v-show="activeGroup === g.name">
            <div class="preset-head">
              <div>
                <div class="row" style="gap: 8px">
                  <b>{{ g.name }}</b>
                  <span class="badge">{{ kindLabel(g.kind) }}</span>
                  <span class="badge" :class="g.realname ? 'warn' : 'ok'">
                    {{ g.realname ? '需实名认证' : '无需实名' }}
                  </span>
                  <span v-if="providerFor(g)" class="badge ok">渠道已就绪</span>
                  <span v-else class="badge warn">尚未创建渠道</span>
                </div>
                <div v-if="g.note" class="muted" style="font-size: 12.5px; margin-top: 6px">
                  {{ g.note }}
                </div>
                <div v-if="g.baseURL" class="mono muted" style="font-size: 11.5px; margin-top: 4px">
                  {{ g.baseURL }}
                </div>
              </div>
              <div class="row" style="gap: 8px; flex: 0 0 auto">
                <button class="btn btn-sm btn-primary" @click="wizardGroup = g">配置向导</button>
                <a
                  v-if="g.signupURL"
                  class="btn btn-sm"
                  :href="g.signupURL"
                  target="_blank"
                  rel="noreferrer"
                >
                  申请 Key
                </a>
                <button
                  class="btn btn-sm"
                  :disabled="!!groupBusy || !providerFor(g)"
                  @click="useGroup(g)"
                >
                  {{ groupBusy === g.name ? '添加中…' : '全部添加' }}
                </button>
              </div>
            </div>

            <div
              v-for="p in g.items"
              :key="p.id"
              class="row preset-row"
              style="align-items: flex-start; gap: 12px"
            >
              <div style="flex: 1; min-width: 0">
                <div class="row" style="gap: 8px">
                  <a
                    v-if="p.signup_url"
                    class="preset-name"
                    :href="p.signup_url"
                    target="_blank"
                    rel="noreferrer"
                    :title="'打开 ' + p.name + ' 的 API 配置页面（申请 / 配置密钥）'"
                  >{{ p.name }} ↗</a>
                  <b v-else>{{ p.name }}</b>
                  <span class="badge ok">免费</span>
                  <span class="badge">{{ capLabel(p.capability) }}</span>
                  <span v-if="added(p)" class="badge brand">已添加</span>
                </div>
                <div class="muted" style="font-size: 12.5px; margin-top: 4px">
                  {{ p.description }}
                </div>
                <div class="muted" style="font-size: 12px; margin-top: 4px">
                  {{ modalityText(p.input_modalities) }} → {{ modalityText(p.output_modalities) }}
                  <template v-if="p.context_length">
                    · 上下文 {{ Math.round(p.context_length / 1000) }}K
                  </template>
                </div>
                <div class="mono muted" style="font-size: 11.5px; margin-top: 3px">
                  {{ p.upstream_name }}
                </div>
              </div>
              <button
                class="btn btn-sm"
                :disabled="!!presetBusy || !providerFor(g)"
                @click="usePreset(p, g)"
              >
                {{ presetBusy === p.id ? '创建中…' : added(p) ? '重新添加' : '添加' }}
              </button>
            </div>
          </div>
        </template>
      </div>
    </div>

    <div v-else class="card empty">
      <div class="empty-icon">◇</div>
      <div class="big">没有符合筛选条件的平台</div>
      <div>换个筛选条件，或点右上角「刷新」重试。</div>
    </div>

    <PresetWizard
      v-if="wizardGroup"
      :group="wizardGroup"
      @close="wizardGroup = null"
      @saved="load()"
    />
  </div>
</template>
