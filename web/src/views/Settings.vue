<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { api } from '../api'
import { models, panelTitle, state, toast } from '../store'

const loading = ref(true)
const saving = ref(false)
const form = reactive({ panel_title: '', system_model: '' })

const chatModels = computed(() => models.value.filter((m) => m.enabled && m.capability === 'chat'))

async function load() {
  loading.value = true
  try {
    if (!state.meta) state.meta = await api.getMeta()
    const [settings, ms] = await Promise.all([api.getSettings(), api.listModels()])
    models.value = ms
    form.panel_title = settings.panel_title || ''
    form.system_model = settings.system_model || ''
    if (form.panel_title) {
      panelTitle.value = form.panel_title
      document.title = form.panel_title
    }
  } catch (e: any) {
    toast(e.message || '加载失败', 'error')
  } finally {
    loading.value = false
  }
}

onMounted(load)

async function save() {
  saving.value = true
  try {
    await api.saveSettings({ panel_title: form.panel_title.trim(), system_model: form.system_model })
    panelTitle.value = form.panel_title.trim() || '模型中转站'
    document.title = panelTitle.value
    toast('设置已保存')
  } catch (e: any) {
    toast(e.message || '保存失败', 'error')
  } finally {
    saving.value = false
  }
}

const baseURL = computed(() => `${location.origin}/v1`)

const backupBusy = ref(false)

// exportBackup 把全部配置下载成 JSON 文件。
async function exportBackup() {
  backupBusy.value = true
  try {
    const data = await api.exportBackup()
    const blob = new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `happy-relay-backup-${new Date().toISOString().slice(0, 10)}.json`
    a.click()
    URL.revokeObjectURL(url)
    toast('配置已导出')
  } catch (e: any) {
    toast(e.message || '导出失败', 'error')
  } finally {
    backupBusy.value = false
  }
}

// onImportFile 读取选中的备份文件并合并导入。
async function onImportFile(e: Event) {
  const input = e.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file) return
  try {
    const payload = JSON.parse(await file.text())
    const summary = await api.importBackup(payload)
    toast(
      `导入完成：渠道 ${summary.providers ?? 0}、模型 ${summary.models ?? 0}、秘钥 ${summary.keys ?? 0}、技能 ${summary.skills ?? 0}`,
    )
    await load()
  } catch (err: any) {
    toast(err.message || '导入失败', 'error')
  } finally {
    input.value = ''
  }
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h2>设置</h2>
        <p>面板与系统级 AI 的全局选项。</p>
      </div>
      <button class="btn btn-sm" :disabled="loading" @click="load">刷新</button>
    </div>

    <div class="grid cols-2">
      <div class="card">
        <h3 class="card-title">通用</h3>
        <div class="field">
          <label>面板标题</label>
          <input v-model="form.panel_title" class="input" placeholder="模型中转站" />
          <div class="hint">显示在侧边栏与浏览器标题上。</div>
        </div>
        <div class="field">
          <label>系统级 AI 模型</label>
          <select v-model="form.system_model" class="select">
            <option value="">（未指定）</option>
            <option v-for="m in chatModels" :key="m.id" :value="m.name">{{ m.name }}</option>
          </select>
          <div class="hint">「连接器生成器」会用它把接口描述翻译成模板，请选择一个文本对话模型。</div>
        </div>
        <button class="btn btn-primary" :disabled="saving" @click="save">
          {{ saving ? '保存中…' : '保存设置' }}
        </button>
      </div>

      <div class="card">
        <h3 class="card-title">客户端接入</h3>
        <p class="muted" style="margin-top: 0">任何兼容 OpenAI 的客户端填这两项即可使用：</p>
        <div class="meta-list">
          <dt>Base URL</dt>
          <dd class="mono">{{ baseURL }}</dd>
          <dt>API Key</dt>
          <dd class="mono">在「秘钥」页创建后复制</dd>
        </div>
        <div class="hint" style="margin-top: 16px">
          对外接口包括 /v1/chat/completions、/v1/images/generations、/v1/embeddings 与任务式的 /v1/videos。
        </div>
      </div>

      <div class="card">
        <h3 class="card-title">配置备份</h3>
        <p class="muted" style="margin-top: 0">
          导出全部渠道、模型、秘钥、技能、模型库与设置，便于迁移或留档。
        </p>
        <div class="row">
          <button class="btn" :disabled="backupBusy" @click="exportBackup">导出配置</button>
          <label class="btn" style="cursor: pointer">
            导入配置
            <input type="file" accept="application/json" style="display: none" @change="onImportFile" />
          </label>
        </div>
        <div class="hint" style="margin-top: 12px">
          导入为合并方式：同名的渠道 / 模型 / 秘钥 / 技能会被更新，不会删除现有数据。
        </div>
      </div>
    </div>
  </div>
</template>
