<script setup>
import { computed, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Search, Refresh, Plus, Phone, Download, Upload, Delete } from '@element-plus/icons-vue'
import ContactFormDialog from '../components/ContactFormDialog.vue'
import { session } from '../stores/session'
import {
  clearDirectory,
  deleteContact,
  exportCSV,
  getContactLinkedUser,
  getUserVisibility,
  importCSV,
  listContactsPaged,
  listUnits,
  updateAdminUser,
  updateUserVisibility,
} from '../api'

const isSuper = computed(() => session.user?.role === 'super')

// 可选单位：super=全部；单位管理员=服务端按其绑定范围返回。
const units = ref([])

// ---- 分页与查询（服务端分页，避免一次拉全量）----
const rows = ref([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const keyword = ref('')
const unitFilter = ref(null)
const loading = ref(false)

async function load() {
  loading.value = true
  try {
    const res = await listContactsPaged({
      page: page.value,
      pageSize: pageSize.value,
      keyword: keyword.value.trim() || undefined,
      unitId: unitFilter.value || undefined,
    })
    rows.value = res.items || []
    total.value = res.total || 0
  } catch (e) {
    ElMessage.error(`加载联系人失败：${e.message}`)
  } finally {
    loading.value = false
  }
}

function search() {
  page.value = 1
  load()
}

onMounted(async () => {
  try {
    units.value = await listUnits()
  } catch (e) {
    ElMessage.error(`加载单位列表失败：${e.message}`)
  }
  await load()
})

async function copyPhone(phone) {
  try {
    await navigator.clipboard.writeText(phone)
    ElMessage.success(`已复制：${phone}`)
  } catch (_) {
    ElMessage.error('复制失败')
  }
}

// 删除联系人：若该号码已关联账号，询问是否同时停用账号（可恢复）。
async function onRemove(row) {
  let linked = null
  try {
    linked = await getContactLinkedUser(row.id)
  } catch (_) {}
  let disableAccount = false
  if (linked?.linked) {
    if (['super', 'root'].includes(session.user?.role)) {
      try {
        await ElMessageBox.confirm(
          `该号码已关联账号「${linked.username}」（${linked.isActive ? '启用中' : '已停用'}）。\n是否同时停用该账号？停用后其设备令牌立即失效，但账号可恢复。`,
          '删除联系人',
          {
            type: 'warning',
            confirmButtonText: '停用账号并删除',
            cancelButtonText: '仅删除联系人',
            distinguishCancelAndClose: true,
          },
        )
        disableAccount = true
      } catch (action) {
        if (action === 'close') return // 关闭弹窗 = 取消
      }
    } else {
      ElMessage.info(`该号码已关联账号「${linked.username}」，账号处理需超级管理员`)
    }
  } else {
    try {
      await ElMessageBox.confirm(`确定删除联系人「${row.displayName}」吗？此操作不可撤销。`, '删除联系人', {
        type: 'warning',
        confirmButtonText: '删除',
        confirmButtonClass: 'el-button--danger',
        cancelButtonText: '取消',
      })
    } catch (_) {
      return
    }
  }
  try {
    if (disableAccount && linked?.linked) {
      const vis = await getUserVisibility(linked.userId)
      await updateAdminUser(linked.userId, {
        displayName: linked.displayName || '',
        role: linked.role,
        isActive: false,
        unitIds: vis.unitIds || [],
      })
    }
    await deleteContact(row.id)
    ElMessage.success(disableAccount ? '已删除联系人并停用账号' : '已删除联系人')
    await load()
  } catch (e) {
    ElMessage.error(`删除失败：${e.message}`)
  }
}

// 联系人换单位后，询问是否把关联账号的可见单位同步为新单位（仅 super/root）。
async function onUnitChanged({ contactId, oldUnitId, newUnitId }) {
  try {
    const linked = await getContactLinkedUser(contactId)
    if (!linked?.linked) return
    if (!['super', 'root'].includes(session.user?.role)) {
      ElMessage.info(`该号码已关联账号「${linked.username}」，可见单位需由超级管理员调整`)
      return
    }
    try {
      await ElMessageBox.confirm(
        `「${linked.username}」已关联账号。是否把该账号的可见单位从原单位改为新单位？`,
        '同步账号可见范围',
        { confirmButtonText: '同步', cancelButtonText: '不同步' },
      )
    } catch (_) {
      return
    }
    const vis = await getUserVisibility(linked.userId)
    const unitIds = (vis.unitIds || []).filter((id) => id !== oldUnitId)
    if (!unitIds.includes(newUnitId)) unitIds.push(newUnitId)
    await updateUserVisibility(linked.userId, {
      unitIds,
      contactIds: (vis.contacts || []).map((c) => c.id),
    })
    ElMessage.success('账号可见单位已同步')
  } catch (e) {
    ElMessage.error(e.message)
  }
}

const dialogVisible = ref(false)
const editingContact = ref(null)
const importing = ref(false)
const clearing = ref(false)
const fileInput = ref()

function openCreate() {
  editingContact.value = null
  dialogVisible.value = true
}
function openEdit(row) {
  editingContact.value = row
  dialogVisible.value = true
}

function onExport() {
  exportCSV() // 空库时导出的表头即为可填写的空模板
  ElMessage.info('已开始下载 contacts.csv（数据为空时即为导入模板）')
}

// 清空目录数据（仅超管）：删除全部联系人与未绑定的单位，用于「整本重新导入」前清场。
async function onClearDirectory() {
  try {
    await ElMessageBox.confirm(
      '确定清空全部联系人吗？\n将删除所有联系人和未被单位管理员绑定的单位，且不可恢复。建议先「导出 CSV」留档。',
      '清空目录数据',
      { type: 'warning', confirmButtonText: '清空', confirmButtonClass: 'el-button--danger', cancelButtonText: '取消' },
    )
  } catch (_) {
    return
  }
  clearing.value = true
  try {
    const res = await clearDirectory()
    ElMessage.success(`已清空：删除联系人 ${res.contactsDeleted}，删除单位 ${res.unitsDeleted}，保留被绑定单位 ${res.unitsKept}`)
    units.value = await listUnits()
    page.value = 1
    await load()
  } catch (e) {
    ElMessage.error(`清空失败：${e.message}`)
  } finally {
    clearing.value = false
  }
}

function onPickFile() {
  fileInput.value?.click()
}

async function onFileChange(ev) {
  const file = ev.target.files?.[0]
  ev.target.value = '' // 允许重复选择同一文件
  if (!file) return
  try {
    const tip = isSuper.value
      ? '导入以「单位 + 姓名」为关键字：命中唯一则覆盖（含手机/手机2/座机/传真），其余新增。\n号码列：手机号、手机2、座机、传真；座机留 7 位短号保存时自动补 0915。\n超级管理员可一次性导入整本通讯录：CSV 里尚未建好的单位会自动新建。继续吗？'
      : '导入以「单位 + 姓名」为关键字：命中唯一则覆盖，其余新增。\n注意：CSV 中的单位须已在「单位管理」中建好（单位管理员只能导入其负责的单位）。继续吗？'
    await ElMessageBox.confirm(tip, '导入 CSV', {
      type: 'warning',
      confirmButtonText: '导入',
      cancelButtonText: '取消',
    })
  } catch (_) {
    return
  }
  importing.value = true
  try {
    const res = await importCSV(file)
    const base = `导入完成：新增 ${res.created} 条，覆盖更新 ${res.updated} 条`
    ElMessage.success(res.unitsCreated ? `${base}，自动新建单位 ${res.unitsCreated} 个` : base)
    units.value = await listUnits() // 若自动新建了单位，刷新单位下拉
    page.value = 1
    await load()
  } catch (e) {
    ElMessage.error(e.message)
  } finally {
    importing.value = false
  }
}
</script>

<template>
  <div>
    <div class="toolbar">
      <el-input
        v-model="keyword"
        placeholder="搜索姓名 / 手机号 / 手机2 / 座机 / 传真 / 职务 / 单位 / 邮箱…"
        :prefix-icon="Search"
        clearable
        class="search"
        @keyup.enter="search"
        @clear="search"
      />
      <el-select
        v-model="unitFilter"
        placeholder="全部单位"
        clearable
        filterable
        default-first-option
        no-data-text="没有匹配的单位"
        class="unit-select"
        @change="search"
      >
        <el-option v-for="u in units" :key="u.id" :label="u.name" :value="u.id" />
      </el-select>
      <el-button type="primary" :icon="Search" @click="search">查询</el-button>
      <el-button :icon="Refresh" @click="load">刷新</el-button>
      <el-button :icon="Download" @click="onExport">导出 CSV</el-button>
      <el-button :icon="Upload" :loading="importing" @click="onPickFile">导入 CSV</el-button>
      <input ref="fileInput" type="file" accept=".csv,text/csv" style="display: none" @change="onFileChange" />
      <el-button
        v-if="isSuper"
        type="danger"
        plain
        :icon="Delete"
        :loading="clearing"
        @click="onClearDirectory"
      >清空目录</el-button>
      <el-button type="primary" :icon="Plus" @click="openCreate">新增联系人</el-button>
    </div>

    <div class="summary" v-if="total">共 <b>{{ total }}</b> 位联系人（第 {{ page }} 页）</div>

    <el-table :data="rows" v-loading="loading" border stripe class="table">
      <el-table-column prop="displayName" label="姓名" min-width="110">
        <template #default="{ row }">
          <span class="name">{{ row.displayName }}</span>
        </template>
      </el-table-column>
      <el-table-column label="手机号" min-width="140">
        <template #default="{ row }">
          <span class="phone">
            {{ row.phoneNumber }}
            <el-button link type="primary" size="small" @click="copyPhone(row.phoneNumber)">
              <el-icon><Phone /></el-icon>
            </el-button>
          </span>
        </template>
      </el-table-column>
      <el-table-column label="手机2" min-width="140">
        <template #default="{ row }">
          <span v-if="row.mobile2" class="phone">
            {{ row.mobile2 }}
            <el-button link type="primary" size="small" @click="copyPhone(row.mobile2)">
              <el-icon><Phone /></el-icon>
            </el-button>
          </span>
          <span v-else class="muted">—</span>
        </template>
      </el-table-column>
      <el-table-column label="座机" min-width="140">
        <template #default="{ row }">
          <span v-if="row.landline" class="phone">
            {{ row.landline }}
            <el-button link type="primary" size="small" @click="copyPhone(row.landline)">
              <el-icon><Phone /></el-icon>
            </el-button>
          </span>
          <span v-else class="muted">—</span>
        </template>
      </el-table-column>
      <el-table-column label="传真" min-width="140">
        <template #default="{ row }">
          <span v-if="row.fax" class="phone">
            {{ row.fax }}
            <el-button link type="primary" size="small" @click="copyPhone(row.fax)">
              <el-icon><Phone /></el-icon>
            </el-button>
          </span>
          <span v-else class="muted">—</span>
        </template>
      </el-table-column>
      <el-table-column prop="unit" label="单位" min-width="120">
        <template #default="{ row }">{{ row.unit || '—' }}</template>
      </el-table-column>
      <el-table-column prop="jobTitle" label="职务" min-width="110">
        <template #default="{ row }">{{ row.jobTitle || '—' }}</template>
      </el-table-column>
      <el-table-column prop="email" label="邮箱" min-width="170">
        <template #default="{ row }">{{ row.email || '—' }}</template>
      </el-table-column>
      <el-table-column label="操作" width="130" align="right" fixed="right">
        <template #default="{ row }">
          <el-button link type="primary" @click="openEdit(row)">编辑</el-button>
          <el-button link type="danger" @click="onRemove(row)">删除</el-button>
        </template>
      </el-table-column>
      <template #empty>
        <el-empty description="暂无联系人，点击右上角「新增联系人」添加" :image-size="80" />
      </template>
    </el-table>

    <div class="pager">
      <el-pagination
        v-model:current-page="page"
        v-model:page-size="pageSize"
        :total="total"
        :page-sizes="[20, 50, 100, 200]"
        layout="total, sizes, prev, pager, next, jumper"
        background
        @current-change="load"
        @size-change="search"
      />
    </div>

    <ContactFormDialog
      v-model="dialogVisible"
      :contact="editingContact"
      :units="units"
      @saved="load"
      @unit-changed="onUnitChanged"
    />
  </div>
</template>

<style scoped>
.toolbar { display: flex; gap: 10px; flex-wrap: wrap; margin-bottom: 8px; }
.toolbar .search { flex: 1; min-width: 240px; max-width: 360px; }
.toolbar .unit-select { width: 160px; }
.summary { color: #64748b; font-size: 13px; margin: 0 0 8px 2px; }
.table { border-radius: 8px; overflow: hidden; }
.pager { display: flex; justify-content: flex-end; margin-top: 12px; }
.name { font-weight: 600; }
.phone { color: #2563eb; font-variant-numeric: tabular-nums; }
.muted { color: #c0c4cc; }
</style>
