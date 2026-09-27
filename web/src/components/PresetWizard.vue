<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import Modal from './Modal.vue'
import { api, type ModelPreset, type ProbeResult } from '../api'
import { providers, toast } from '../store'
import { kindLabel } from '../utils'

// PresetWizard 免费模型配置向导：申请密钥 → 创建/更新渠道并测连通 → 勾选模型入库。
// 三个步骤都围绕一个平台（preset group）展开，走完即可直接调用。
const props = defineProps<{
  group: {
    name: string
    kind: string
    baseURL: string
    signupURL: string
    note: string
    /** 开通该平台是否需要实名认证 */
    realname: boolean
    items: ModelPreset[]
  }
}>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'saved'): void }>()

const step = ref(1)
const apiKey = ref('')
const busy = ref(false)
const savedId = ref(0)
const probe = ref<ProbeResult | null>(null)
// 勾选状态默认全选：走向导的用户大概率想把这个平台的免费模型都加上。
const chosen = ref<Record<string, boolean>>(Object.fromEntries(props.group.items.map((i) => [i.id, true])))
const done = ref(false)
const results = reactive({ created: [] as string[], existed: [] as string[], failed: [] as string[] })

// 已有同名渠道就更新它，否则创建。GitHub Models 等无 /v1 前缀的标记随预设带过去。
const existing = computed(() => providers.value.find((p) => p.name === props.group.name))
const hasKey = computed(() => !!existing.value?.api_keys?.length)
const noVersion = computed(() => props.group.items.some((i) => (i as any).no_version_path))
const chosenItems = computed(() => props.group.items.filter((i) => chosen.value[i.id]))

function goStep(n: number) {
  if (n === 2 && !apiKey.value.trim() && !hasKey.value) {
    toast('请先粘贴 API 密钥，或回上一步申请', 'error')
    return
  }
  step.value = n
}

// saveAndTest 保存渠道（新建或更新）后立刻探测连通性。
async function saveAndTest() {
  const key = apiKey.value.trim()
  if (!key && !hasKey.value) {
    toast('请先粘贴 API 密钥', 'error')
    return
  }
  busy.value = true
  probe.value = null
  try {
    const ex = existing.value
    // 更新时必须把已有密钥原样带回，否则后端会把旧密钥清掉。
    const keys = key ? [key] : ex!.api_keys
    const payload: any = ex
      ? { ...ex, api_keys: keys }
      : {
          name: props.group.name,
          kind: props.group.kind,
          base_urls: props.group.baseURL ? [props.group.baseURL] : [],
          api_keys: keys,
          headers: {},
          strategy: 'failover',
          timeout_sec: 0,
          enabled: true,
          remark: '由免费模型配置向导创建',
        }
    if (noVersion.value) payload.no_version_path = true
    const saved = await api.saveProvider(payload)
    savedId.value = saved.id
    // 同步进本地 store，第三步与「渠道已就绪」徽标立刻可用。
    const idx = providers.value.findIndex((p) => p.id === saved.id)
    if (idx >= 0) providers.value[idx] = saved
    else providers.value.push(saved)

    // 探测按渠道 ID 走：后端会自动用刚保存的密钥与入口。
    const t = await api.testProvider({ id: saved.id })
    probe.value = t.results?.[0] || null
  } catch (e: any) {
    toast(e.message || '保存或测试失败', 'error')
  } finally {
    busy.value = false
  }
}

// addModels 把勾选的预设逐个创建为对外模型。
async function addModels() {
  const pid = savedId.value || existing.value?.id
  if (!pid) {
    toast('请先完成渠道配置', 'error')
    return
  }
  if (!chosenItems.value.length) {
    toast('请至少勾选一个模型', 'error')
    return
  }
  busy.value = true
  results.created = []
  results.existed = []
  results.failed = []
  try {
    for (const p of chosenItems.value) {
      try {
        const r = await api.createModelFromPreset({ preset_id: p.id, provider_id: pid, name: p.name })
        if ((r as any).existed) results.existed.push(p.name)
        else results.created.push(p.name)
      } catch {
        results.failed.push(p.name)
      }
    }
    done.value = true
    emit('saved')
  } finally {
    busy.value = false
  }
}

function finish() {
  emit('saved')
  emit('close')
}
</script>

<template>
  <Modal
    :title="`配置「${group.name}」免费模型`"
    subtitle="三步走完：申请密钥 → 渠道连通 → 模型入库。"
    wide
    @close="emit('close')"
  >
    <!-- 步骤指示 -->
    <div class="row" style="gap: 8px; margin-bottom: 16px; flex-wrap: wrap">
      <span class="badge" :class="step === 1 ? 'brand' : step > 1 ? 'ok' : ''">1 · 申请密钥</span>
      <span class="muted">→</span>
      <span class="badge" :class="step === 2 ? 'brand' : step > 2 ? 'ok' : ''">2 · 渠道与连通</span>
      <span class="muted">→</span>
      <span class="badge" :class="step === 3 ? 'brand' : ''">3 · 添加模型</span>
    </div>

    <!-- 第 1 步：申请密钥 -->
    <div v-if="step === 1">
      <div class="row" style="gap: 8px; margin-bottom: 12px">
        <span class="badge" :class="group.realname ? 'warn' : 'ok'">
          {{ group.realname ? '需要实名认证' : '无需实名认证' }}
        </span>
        <span class="muted" style="font-size: 12.5px">
          {{ group.realname ? '注册后需先完成平台实名认证才能申请 API Key' : '注册账号即可申请 API Key，无需实名认证' }}
        </span>
      </div>
      <div v-if="group.note" class="hint notice-inline" style="margin: 0 0 12px">{{ group.note }}</div>
      <div class="field">
        <label>第一步：打开平台页面，注册 / 登录后申请 API Key</label>
        <div class="row" style="gap: 8px">
          <a v-if="group.signupURL" class="btn btn-primary" :href="group.signupURL" target="_blank" rel="noreferrer">
            打开 {{ group.name }} 申请页面 ↗
          </a>
          <span v-else class="badge warn">该平台未提供申请链接，请自行到官网获取</span>
          <span class="muted" style="font-size: 12.5px">在新标签页操作，拿到密钥后回到这里</span>
        </div>
      </div>
      <div class="field">
        <label>第二步：把拿到的 API Key 粘贴到这里</label>
        <input
          v-model="apiKey"
          class="input code"
          type="password"
          autocomplete="off"
          :placeholder="hasKey ? '渠道已有密钥，可留空直接下一步；粘贴则覆盖' : 'sk-...'"
        />
        <div class="hint">密钥只保存在你自己的中转站数据库里，不会上传到任何第三方。</div>
      </div>
    </div>

    <!-- 第 2 步：创建渠道并测试 -->
    <div v-else-if="step === 2">
      <div class="meta-list" style="margin-bottom: 14px">
        <dt>渠道</dt>
        <dd>{{ existing ? `更新已有渠道「${group.name}」` : `新建渠道「${group.name}」` }}</dd>
        <dt>类型</dt>
        <dd>{{ kindLabel(group.kind) }}</dd>
        <dt>实名要求</dt>
        <dd>{{ group.realname ? '需要实名认证' : '无需实名认证' }}</dd>
        <template v-if="group.baseURL">
          <dt>入口地址</dt>
          <dd class="mono">{{ group.baseURL }}</dd>
        </template>
      </div>
      <div class="row" style="gap: 8px">
        <button class="btn btn-primary" :class="{ 'is-loading': busy }" :disabled="busy" @click="saveAndTest">
          {{ busy ? '保存并测试中…' : savedId ? '重新测试' : '保存渠道并测试连通' }}
        </button>
        <span v-if="probe" class="badge" :class="probe.ok ? 'ok' : 'danger'">
          {{ probe.ok ? `连通正常 · ${probe.latency_ms}ms` : '连通失败' }}
        </span>
      </div>
      <div v-if="probe" class="hint" :style="{ marginTop: '10px', color: probe.ok ? undefined : 'var(--danger)' }">
        <template v-if="probe.ok">
          连通测试通过，探测到 {{ probe.models?.length || 0 }} 个上游模型，可以进入下一步把免费模型加进来。
        </template>
        <template v-else>
          {{ probe.detail || '连接上游失败' }}。常见原因：密钥没配对、平台要求实名认证未完成、或本机网络无法直连。
          修正后点「重新测试」——测试通过后才能把模型纳入对外列表。
        </template>
      </div>
      <div v-else class="hint" style="margin-top: 10px">
        会自动保存渠道（已有同名渠道则更新密钥），并用它发起一次轻量请求验证密钥是否可用。
        只有测试通过，下一步才能把该平台的模型纳入对外模型列表。
      </div>
    </div>

    <!-- 第 3 步：勾选模型入库 -->
    <div v-else>
      <div class="field">
        <label>勾选要添加的模型（{{ chosenItems.length }}/{{ group.items.length }}）</label>
        <div
          v-for="p in group.items"
          :key="p.id"
          class="row preset-row"
          style="align-items: center; gap: 10px"
        >
          <label class="switch" style="flex: 1; min-width: 0">
            <input v-model="chosen[p.id]" type="checkbox" />
            <span class="track" />
            <span class="mono" style="font-size: 12.5px">{{ p.name }}</span>
          </label>
          <span class="badge ok">免费</span>
          <span class="muted" style="font-size: 12px; flex: 1; min-width: 0" :title="p.description">
            {{ p.description }}
          </span>
        </div>
      </div>

      <div v-if="done" class="hint notice-inline">
        <template v-if="results.created.length">已创建 {{ results.created.length }} 个模型。</template>
        <template v-if="results.existed.length"> {{ results.existed.length }} 个已存在（跳过）。</template>
        <template v-if="results.failed.length">
          <span style="color: var(--danger)">{{ results.failed.length }} 个失败：{{ results.failed.join('、') }}</span>
        </template>
        全部就绪——现在就可以在「调试台」里试一发。
      </div>
    </div>

    <template #footer>
      <button v-if="step > 1" class="btn" :disabled="busy" @click="goStep(step - 1)">上一步</button>
      <span style="flex: 1" />
      <button class="btn" @click="emit('close')">{{ done ? '关闭' : '取消' }}</button>
      <button v-if="step === 1" class="btn btn-primary" @click="goStep(2)">下一步</button>
      <button
        v-else-if="step === 2"
        class="btn btn-primary"
        :disabled="busy || !savedId || !probe?.ok"
        :title="probe?.ok ? '' : '连通测试通过后才能继续'"
        @click="goStep(3)"
      >
        下一步
      </button>
      <button v-else class="btn btn-primary" :class="{ 'is-loading': busy }" :disabled="busy" @click="addModels">
        {{ busy ? '创建中…' : done ? '再添加一次（重新勾选）' : `添加所选 ${chosenItems.length} 个模型` }}
      </button>
    </template>
  </Modal>
</template>
