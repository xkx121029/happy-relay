<script setup lang="ts">
import { onMounted, ref } from 'vue'
import Modal from './Modal.vue'
import { api, type SetupStatus } from '../api'
import { toast } from '../store'

const open = ref(false)
const saving = ref(false)
const status = ref<SetupStatus | null>(null)
const apiKey = ref('')
const createAccessKey = ref(true)
// generatedKey 非空时表示已生成访问秘钥，弹窗切换成「展示并复制」形态。
const generatedKey = ref('')

// DISMISS_KEY 记录用户主动跳过引导，避免每次打开面板都弹。
const DISMISS_KEY = 'relay_setup_dismissed'

async function load() {
  try {
    const st = await api.getSetup()
    status.value = st
    // 只在「确实内置了免费模型」且「还缺 Key 或还没有访问秘钥」时提示。
    const needed = st.seeded && (st.needs_key || st.access_keys === 0)
    if (needed && localStorage.getItem(DISMISS_KEY) !== '1') {
      open.value = true
    }
  } catch {
    // 引导失败不影响面板其它功能
  }
}

function dismiss() {
  localStorage.setItem(DISMISS_KEY, '1')
  open.value = false
}

async function submit() {
  const body: { api_key?: string; create_access_key?: boolean } = {}
  if (apiKey.value.trim()) body.api_key = apiKey.value.trim()
  if (createAccessKey.value && status.value?.access_keys === 0) body.create_access_key = true
  if (!body.api_key && !body.create_access_key) {
    toast('请填写 API Key，或勾选生成访问秘钥', 'error')
    return
  }
  saving.value = true
  try {
    const res = await api.saveSetup(body)
    status.value = res.status
    if (res.access_key) {
      // 生成的秘钥需要用户复制走，先留在弹窗里展示。
      generatedKey.value = res.access_key
      toast('设置完成，请保存好访问秘钥', 'success')
      return
    }
    toast('设置完成，可以开始使用了', 'success')
    dismiss()
  } catch (e: any) {
    toast(e.message || '保存失败', 'error')
  } finally {
    saving.value = false
  }
}

async function copyKey() {
  try {
    await navigator.clipboard.writeText(generatedKey.value)
    toast('已复制到剪贴板', 'success')
  } catch {
    toast('复制失败，请手动选中复制', 'error')
  }
}

onMounted(load)
</script>

<template>
  <Modal v-if="open && status" title="已为你准备好免费模型" @close="dismiss">
    <p style="margin: 0 0 12px">
      部署时已自动创建渠道「{{ status.provider_name }}」和
      <b>{{ status.models.length }}</b> 个免费模型。
    </p>

    <div class="chips" style="margin-bottom: 14px">
      <span v-for="m in status.models" :key="m" class="chip">{{ m }}</span>
    </div>

    <template v-if="!generatedKey">
      <p class="muted" style="margin: 0 0 14px; font-size: 12.5px; line-height: 1.75">
        这些模型由上游免费提供，但仍需要一个该平台的 API Key 才能调用。填入后即可直接使用。
      </p>

      <div class="field">
        <label>
          {{ status.provider_name }} 的 API Key
          <a
            v-if="status.signup_url"
            :href="status.signup_url"
            target="_blank"
            rel="noreferrer"
            style="margin-left: 6px; font-size: 12px"
            >去申请 →</a
          >
        </label>
        <input v-model="apiKey" class="input code" placeholder="粘贴 API Key" />
      </div>

      <label
        v-if="status.access_keys === 0"
        class="row"
        style="gap: 8px; cursor: pointer; margin-top: 6px"
      >
        <input v-model="createAccessKey" type="checkbox" />
        <span style="font-size: 13px">同时生成一个访问秘钥，供客户端调用中转站</span>
      </label>
    </template>

    <template v-else>
      <div class="field">
        <label>你的访问秘钥（请立即保存，之后不再完整显示）</label>
        <div class="list-row">
          <input :value="generatedKey" class="input code" readonly />
          <button class="btn btn-sm" @click="copyKey">复制</button>
        </div>
      </div>
      <p class="muted" style="font-size: 12.5px; margin: 0; line-height: 1.75">
        客户端调用时带上请求头 <code>Authorization: Bearer {{ generatedKey }}</code> 即可。
      </p>
    </template>

    <template #footer>
      <div class="row">
        <button class="btn btn-ghost" @click="dismiss">稍后再说</button>
        <div class="spacer" />
        <button v-if="!generatedKey" class="btn btn-primary" :disabled="saving" @click="submit">
          {{ saving ? '保存中…' : '完成设置' }}
        </button>
        <button v-else class="btn btn-primary" @click="dismiss">开始使用</button>
      </div>
    </template>
  </Modal>
</template>
