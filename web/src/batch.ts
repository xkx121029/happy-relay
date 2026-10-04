import { computed, ref } from 'vue'
import { confirmDialog, toast } from './store'

// ---------- 列表页批量选择/批量操作（模型、渠道、秘钥共用） ----------
//
// 用法：
//   const bat = useBatch(() => shown.value.map(x => x.id), api.batchModels, load)
//   表格里：<input type="checkbox" :checked="bat.sel.value.has(m.id)" @change="bat.toggleSel(m.id)">
//   工具条：<BatchBar :count="bat.selCount.value" :busy="bat.batchBusy.value" @act="bat.batch" @clear="bat.clearSel" />

export type BatchAction = 'enable' | 'disable' | 'delete'

export function useBatch(
  idsOf: () => number[],
  run: (ids: number[], action: BatchAction) => Promise<{ affected: number; failed?: number[] }>,
  reload: () => Promise<void>,
) {
  const sel = ref<Set<number>>(new Set())
  const batchBusy = ref(false)

  const selCount = computed(() => sel.value.size)
  const allSelected = computed(() => {
    const ids = idsOf()
    return ids.length > 0 && ids.every((id) => sel.value.has(id))
  })
  const someSelected = computed(() => {
    const ids = idsOf()
    return ids.some((id) => sel.value.has(id)) && !allSelected.value
  })

  function toggleSel(id: number) {
    const s = new Set(sel.value)
    if (s.has(id)) s.delete(id)
    else s.add(id)
    sel.value = s
  }
  function toggleAll() {
    const s = new Set<number>()
    if (!allSelected.value) idsOf().forEach((id) => s.add(id))
    sel.value = s
  }
  function clearSel() {
    sel.value = new Set()
  }

  const actionLabel: Record<BatchAction, string> = { enable: '启用', disable: '禁用', delete: '删除' }

  async function batch(action: BatchAction) {
    const ids = [...sel.value]
    if (!ids.length) return
    if (action === 'delete') {
      const ok = await confirmDialog({
        title: '批量删除',
        message: `确定删除选中的 ${ids.length} 项？此操作立即生效且不可恢复。`,
        confirmText: '删除',
      })
      if (!ok) return
    }
    batchBusy.value = true
    try {
      const r = await run(ids, action)
      const fail = r.failed?.length || 0
      if (fail) toast(`成功 ${r.affected} 项、失败 ${fail} 项`, 'error')
      else toast(`已${actionLabel[action]} ${r.affected} 项`, 'success')
      clearSel()
      await reload()
    } catch (e: any) {
      toast(e.message || '批量操作失败', 'error')
    } finally {
      batchBusy.value = false
    }
  }

  return { sel, selCount, allSelected, someSelected, batchBusy, toggleSel, toggleAll, clearSel, batch }
}
