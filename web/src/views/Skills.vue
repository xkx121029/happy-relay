<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import Modal from '../components/Modal.vue'
import { api, type Skill, type SkillParam } from '../api'
import { skills, state, toast } from '../store'
import { capLabel } from '../utils'

const loading = ref(true)
const editing = ref(false)
const saving = ref(false)

interface SkillForm {
  id: number
  slug: string
  name: string
  description: string
  category: string
  icon: string
  capability: string
  prompt: string
  params: SkillParam[]
  enabled: boolean
  sort_order: number
}

function blank(): SkillForm {
  return {
    id: 0,
    slug: '',
    name: '',
    description: '',
    category: '通用',
    icon: '',
    capability: 'chat',
    prompt: '',
    params: [],
    enabled: true,
    sort_order: 200,
  }
}

const form = reactive<SkillForm>(blank())

const capabilities = computed(() => {
  const caps = state.meta?.capabilities || [
    { value: 'chat', label: '文本对话' },
    { value: 'image', label: '图片生成' },
    { value: 'video', label: '视频生成' },
  ]
  return caps.filter((c) => c.value !== 'embedding')
})

const paramTypes = [
  { value: 'text', label: '单行文本' },
  { value: 'textarea', label: '多行文本' },
  { value: 'number', label: '数字' },
  { value: 'select', label: '下拉选择' },
]

const categories = computed(() => {
  const set = new Set(skills.value.map((s) => s.category).filter(Boolean))
  const base = ['文本', '图片', '视频', '通用']
  return Array.from(new Set([...base, ...set]))
})

async function load() {
  loading.value = true
  try {
    skills.value = await api.listSkills()
  } catch (e: any) {
    toast(e.message || '加载失败', 'error')
  } finally {
    loading.value = false
  }
}

onMounted(load)

function openCreate() {
  Object.assign(form, blank())
  editing.value = true
}

function openEdit(sk: Skill) {
  Object.assign(form, {
    id: sk.id,
    slug: sk.slug,
    name: sk.name,
    description: sk.description,
    category: sk.category,
    icon: sk.icon,
    capability: sk.capability,
    prompt: sk.prompt,
    params: JSON.parse(JSON.stringify(sk.params || [])),
    enabled: sk.enabled,
    sort_order: sk.sort_order,
  })
  editing.value = true
}

function addParam() {
  form.params.push({ key: '', label: '', type: 'text' })
}

function optionsText(p: SkillParam): string {
  return (p.options || []).join(', ')
}

function onOptionsInput(p: SkillParam, e: Event) {
  const v = (e.target as HTMLInputElement).value
  p.options = v.split(',').map((s) => s.trim()).filter(Boolean)
}

async function save() {
  if (!form.name.trim()) {
    toast('请填写技能名称', 'error')
    return
  }
  const params = form.params.filter((p) => p.key.trim())
  saving.value = true
  try {
    await api.saveSkill({
      id: form.id || undefined,
      slug: form.slug.trim(),
      name: form.name.trim(),
      description: form.description.trim(),
      category: form.category.trim() || '通用',
      icon: form.icon.trim(),
      capability: form.capability,
      prompt: form.prompt,
      params,
      enabled: form.enabled,
      sort_order: Number(form.sort_order) || 0,
    })
    toast('技能已保存')
    editing.value = false
    await load()
  } catch (e: any) {
    toast(e.message || '保存失败', 'error')
  } finally {
    saving.value = false
  }
}

async function remove(sk: Skill) {
  if (!confirm(`确定删除技能「${sk.name}」？`)) return
  try {
    await api.deleteSkill(sk.id)
    toast('已删除')
    await load()
  } catch (e: any) {
    toast(e.message || '删除失败', 'error')
  }
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h2>站内技能</h2>
        <p>带参数的提示词模板，在调试台一键套用。预置技能不可删除，可另存为自定义技能。</p>
      </div>
      <div class="row">
        <button class="btn btn-sm" :disabled="loading" @click="load">刷新</button>
        <button class="btn btn-primary btn-sm" @click="openCreate">新建技能</button>
      </div>
    </div>

    <div v-if="loading" class="card"><div class="skeleton" style="width: 40%" /></div>

    <div v-else-if="skills.length" class="table-wrap">
      <table class="table">
        <thead>
          <tr>
            <th>名称</th>
            <th>slug</th>
            <th>分类</th>
            <th>能力</th>
            <th>参数</th>
            <th>来源</th>
            <th>状态</th>
            <th style="text-align: right">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="sk in skills" :key="sk.id">
            <td>
              <div style="font-weight: 600">{{ sk.name }}</div>
              <div class="muted clamp-1" style="font-size: 12px; max-width: 320px">{{ sk.description }}</div>
            </td>
            <td class="mono dim">{{ sk.slug }}</td>
            <td><span class="badge">{{ sk.category }}</span></td>
            <td class="dim">{{ capLabel(sk.capability) }}</td>
            <td class="dim">{{ sk.params?.length || 0 }}</td>
            <td>
              <span class="badge" :class="sk.builtin ? 'brand' : ''">{{ sk.builtin ? '预置' : '自定义' }}</span>
            </td>
            <td>
              <span class="badge" :class="sk.enabled ? 'ok' : ''">
                <span class="dot" />{{ sk.enabled ? '启用' : '停用' }}
              </span>
            </td>
            <td>
              <div class="row" style="justify-content: flex-end; gap: 6px">
                <button class="btn btn-sm btn-ghost" @click="openEdit(sk)">编辑</button>
                <button
                  v-if="!sk.builtin"
                  class="btn btn-sm btn-ghost"
                  style="color: var(--danger)"
                  @click="remove(sk)"
                >
                  删除
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-else class="card empty">
      <div class="big">还没有技能</div>
    </div>

    <Modal
      v-if="editing"
      wide
      :title="form.id ? '编辑技能' : '新建技能'"
      subtitle="技能模板用 {{参数 key}} 占位，调试台套用时替换成实际输入。"
      @close="editing = false"
    >
      <div class="grid cols-2" style="gap: 0 18px">
        <div class="field">
          <label>技能名称</label>
          <input v-model="form.name" class="input" placeholder="例如：周报生成" />
        </div>
        <div class="field">
          <label>slug（唯一标识）</label>
          <input v-model="form.slug" class="input code" placeholder="留空自动生成" />
        </div>
        <div class="field">
          <label>分类</label>
          <input v-model="form.category" class="input" list="skill-cats" placeholder="文本 / 图片 / 视频" />
          <datalist id="skill-cats">
            <option v-for="c in categories" :key="c" :value="c" />
          </datalist>
        </div>
        <div class="field">
          <label>适用能力</label>
          <select v-model="form.capability" class="select">
            <option v-for="c in capabilities" :key="c.value" :value="c.value">{{ c.label }}</option>
          </select>
        </div>
      </div>

      <div class="field">
        <label>描述</label>
        <input v-model="form.description" class="input" placeholder="一句话说明这个技能做什么" />
      </div>

      <div class="field">
        <label>提示词模板</label>
        <textarea
          v-model="form.prompt"
          class="textarea code"
          rows="6"
          placeholder="你是……请处理下面的内容：&#10;{{input}}"
        />
        <div class="hint">在模板里写入 {{ '{{参数 key}}' }}，例如 {{ '{{input}}' }}、{{ '{{target}}' }}。</div>
      </div>

      <div class="field">
        <label>参数</label>
        <div v-for="(p, i) in form.params" :key="i" class="card" style="padding: 14px; margin-bottom: 10px">
          <div class="grid cols-3" style="gap: 0 12px">
            <div class="field" style="margin-bottom: 10px">
              <label>参数 key</label>
              <input v-model="p.key" class="input code" placeholder="input" />
            </div>
            <div class="field" style="margin-bottom: 10px">
              <label>显示名</label>
              <input v-model="p.label" class="input" placeholder="提问内容" />
            </div>
            <div class="field" style="margin-bottom: 10px">
              <label>类型</label>
              <select v-model="p.type" class="select">
                <option v-for="t in paramTypes" :key="t.value" :value="t.value">{{ t.label }}</option>
              </select>
            </div>
          </div>
          <div class="grid cols-2" style="gap: 0 12px">
            <div class="field" style="margin-bottom: 0">
              <label>默认值</label>
              <input v-model="p.default" class="input" />
            </div>
            <div class="field" style="margin-bottom: 0" v-if="p.type === 'select'">
              <label>选项（逗号分隔）</label>
              <input :value="optionsText(p)" class="input" @input="onOptionsInput(p, $event)" />
            </div>
          </div>
          <div class="row" style="justify-content: flex-end; margin-top: 10px">
            <button class="btn btn-sm btn-ghost" style="color: var(--danger)" @click="form.params.splice(i, 1)">
              删除该参数
            </button>
          </div>
        </div>
        <button class="btn btn-sm" @click="addParam">+ 添加参数</button>
      </div>

      <div class="grid cols-2" style="gap: 0 18px">
        <div class="field">
          <label>排序（越小越靠前）</label>
          <input v-model.number="form.sort_order" type="number" class="input" />
        </div>
        <div class="field">
          <label>图标标识（可选）</label>
          <input v-model="form.icon" class="input code" placeholder="chat / wand / film" />
        </div>
      </div>

      <label class="switch">
        <input v-model="form.enabled" type="checkbox" />
        <span class="track" />
        <span>启用该技能</span>
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
