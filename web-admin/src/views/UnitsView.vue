<script setup>
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh } from '@element-plus/icons-vue'
import { bulkSetUnitsPublic, createUnit, deleteUnit, listUnits, updateUnit } from '../api'

const rows = ref([])
const loading = ref(false)
const dialogVisible = ref(false)
const saving = ref(false)
const editing = ref(null) // null → 新增
const formRef = ref()
const tableRef = ref()
const selection = ref([])
const form = reactive({ name: '', sortOrder: 0, isPublic: false })

const rules = {
  name: [{ required: true, message: '请输入单位名称', trigger: 'blur' }],
}

async function load() {
  loading.value = true
  try {
    rows.value = await listUnits()
  } catch (e) {
    ElMessage.error(`加载单位失败：${e.message}`)
  } finally {
    loading.value = false
  }
}
onMounted(load)

function openCreate() {
  editing.value = null
  Object.assign(form, { name: '', sortOrder: rows.value.length, isPublic: false })
  dialogVisible.value = true
}
function openEdit(row) {
  editing.value = row
  Object.assign(form, { name: row.name, sortOrder: row.sortOrder, isPublic: !!row.isPublic })
  dialogVisible.value = true
}

async function submit() {
  const valid = await formRef.value.validate().catch(() => false)
  if (!valid) return
  saving.value = true
  try {
    const payload = { name: form.name.trim(), sortOrder: form.sortOrder, isPublic: form.isPublic }
    if (editing.value) {
      await updateUnit(editing.value.id, payload)
      ElMessage.success('已更新单位')
    } else {
      await createUnit(payload)
      ElMessage.success('已新建单位')
    }
    dialogVisible.value = false
    await load()
  } catch (e) {
    ElMessage.error(e.message)
  } finally {
    saving.value = false
  }
}

// onTogglePublic 行内快速切换「公开单位」。此操作会让全体登录用户（含手机只读账号）看到该单位联系人，
// 但单位管理员/只读用户在网页端无法更改，仅超管可切。
async function onTogglePublic(row, val) {
  const prev = row.isPublic
  row.isPublic = !!val
  try {
    await updateUnit(row.id, { name: row.name, sortOrder: row.sortOrder, isPublic: !!val })
    ElMessage.success(val ? '已设为公开单位，全体登录用户（含手机端只读账号）可见' : '已取消公开')
  } catch (e) {
    row.isPublic = prev
    ElMessage.error(e.message)
  }
}

// applyPublic 批量设置勾选单位的公开标记（表头勾选框 = 全选当前全部单位），“最后保存”。
async function applyPublic(isPublic) {
  const sel = selection.value
  if (!sel.length) {
    ElMessage.warning('请先勾选要设置的单位（表头复选框可全选）')
    return
  }
  const label = isPublic ? '设为公开' : '取消公开'
  try {
    await ElMessageBox.confirm(
      `将选中的 ${sel.length} 个单位${isPublic ? '设为公开单位（全体登录用户可见）' : '取消公开'}，确定保存？`,
      `批量${label}`,
      { type: 'warning', confirmButtonText: '保存', cancelButtonText: '取消' },
    )
  } catch (_) {
    return
  }
  try {
    const { count } = await bulkSetUnitsPublic(sel.map((x) => x.id), isPublic)
    ElMessage.success(`已${label} ${count} 个单位`)
    tableRef.value?.clearSelection()
    await load()
  } catch (e) {
    ElMessage.error(e.message)
  }
}

async function onRemove(row) {
  try {
    await ElMessageBox.confirm(
      `确定删除单位「${row.name}」吗？\n其下还有 ${row.count} 位联系人时将被拒绝。`,
      '删除单位',
      { type: 'warning', confirmButtonText: '删除', confirmButtonClass: 'el-button--danger', cancelButtonText: '取消' },
    )
  } catch (_) {
    return
  }
  try {
    await deleteUnit(row.id)
    ElMessage.success('已删除单位')
    await load()
  } catch (e) {
    ElMessage.error(e.message)
  }
}
</script>

<template>
  <div>
    <div class="toolbar">
      <div class="tip">
        联系人「单位」取自这里；CSV 导入自动创建的单位默认「公开单位」。勾选后可批量设公开/取消公开（表头复选框全选）。
      </div>
      <template v-if="selection.length">
        <el-button type="success" @click="applyPublic(true)">设为公开（{{ selection.length }}）</el-button>
        <el-button type="info" @click="applyPublic(false)">取消公开（{{ selection.length }}）</el-button>
      </template>
      <el-button :icon="Refresh" @click="load">刷新</el-button>
      <el-button type="primary" :icon="Plus" @click="openCreate">新建单位</el-button>
    </div>

    <el-table ref="tableRef" :data="rows" v-loading="loading" border stripe class="table" @selection-change="(sel) => (selection = sel)">
      <el-table-column type="selection" width="46" align="center" />
      <el-table-column prop="name" label="单位名称" min-width="200" />
      <el-table-column prop="sortOrder" label="排序" width="100" align="center" />
      <el-table-column prop="count" label="联系人" width="100" align="center" />
      <el-table-column label="公开单位" width="120" align="center">
        <template #default="{ row }">
          <el-switch :model-value="!!row.isPublic" @change="(v) => onTogglePublic(row, v)" />
        </template>
      </el-table-column>
      <el-table-column label="操作" width="140" align="right">
        <template #default="{ row }">
          <el-button link type="primary" @click="openEdit(row)">编辑</el-button>
          <el-button link type="danger" @click="onRemove(row)">删除</el-button>
        </template>
      </el-table-column>
      <template #empty>
        <el-empty description="还没有单位。先新建单位，再在联系人页按单位添加联系人。" :image-size="80" />
      </template>
    </el-table>

    <el-dialog
      v-model="dialogVisible"
      :title="editing ? '编辑单位' : '新建单位'"
      width="420px"
      :close-on-click-modal="false"
    >
      <el-form ref="formRef" :model="form" :rules="rules" label-width="80px">
        <el-form-item label="单位名称" prop="name">
          <el-input v-model="form.name" maxlength="50" placeholder="如：信息中心、人力资源部" />
        </el-form-item>
        <el-form-item label="排序" prop="sortOrder">
          <el-input-number v-model="form.sortOrder" :min="0" :max="9999" controls-position="right" />
          <span class="order-hint">数字越小越靠前</span>
        </el-form-item>
        <el-form-item label="公开单位" prop="isPublic">
          <el-switch v-model="form.isPublic" />
          <span class="order-hint">开启后该单位联系人对全体登录用户（含手机只读账号）可见</span>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="submit">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.toolbar { display: flex; gap: 10px; align-items: center; margin-bottom: 10px; flex-wrap: wrap; }
.tip { flex: 1; color: #64748b; font-size: 13px; }
.table { border-radius: 8px; overflow: hidden; }
.order-hint { margin-left: 10px; color: #94a3b8; font-size: 12px; }
</style>
