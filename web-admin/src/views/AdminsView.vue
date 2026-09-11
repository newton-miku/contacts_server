<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh, Search } from '@element-plus/icons-vue'
import {
  listAdminUsers,
  createAdminUser,
  updateAdminUser,
  resetAdminPassword,
  deleteAdminUser,
  listUnits,
} from '../api'
import { session } from '../stores/session'

const router = useRouter()
const isRoot = computed(() => session.user?.role === 'root')

const rows = ref([])
const units = ref([]) // 全部单位（超管维护管理员用）
const loading = ref(false)
const activeTab = ref('admin') // admin=管理员管理；user=用户管理

// ---- 过滤条件 ----
const kw = ref('')
const adminRole = ref('')
const statusFilter = ref('')
const pwdFilter = ref('')

// ---- 新建/编辑对话框 ----
const dialogVisible = ref(false)
const saving = ref(false)
const editing = ref(null) // null → 新增
const formRef = ref()
const form = reactive({
  username: '',
  displayName: '',
  password: '',
  role: 'unit',
  isActive: true,
  unitIds: [],
})

const isSelf = (row) => session.user?.id === row.id
// canManage 能否对目标账号执行编辑/改密/删除：自己的账号走「修改密码」页；
// root 账号只有 root 能动（与服务端同一规则）。
const canManage = (row) => !isSelf(row) && (row.role !== 'root' || isRoot.value)
const roleName = (r) =>
  ({ root: '系统管理员', super: '超级管理员', unit: '单位管理员', enduser: '终端用户' })[r] || r
const roleTag = (r) =>
  ({ root: 'danger', super: 'warning', unit: 'primary', enduser: 'success' })[r] || 'info'
const goVisibility = () => router.push('/visibility')

async function load() {
  loading.value = true
  try {
    const [users, unitRows] = await Promise.all([listAdminUsers(), listUnits()])
    rows.value = users
    units.value = unitRows
  } catch (e) {
    ElMessage.error(`加载失败：${e.message}`)
  } finally {
    loading.value = false
  }
}
onMounted(load)

// ---- 分组与过滤 ----
const admins = computed(() => rows.value.filter((r) => r.role !== 'enduser'))
const endusers = computed(() => rows.value.filter((r) => r.role === 'enduser'))

const matchKeyword = (r) => {
  const k = kw.value.trim().toLowerCase()
  if (!k) return true
  return [r.username, r.displayName, ...(r.unitNames || [])].some((v) =>
    String(v ?? '').toLowerCase().includes(k),
  )
}
const matchStatus = (r) =>
  statusFilter.value === '' ||
  (statusFilter.value === 'active' ? r.isActive : !r.isActive)
const matchPassword = (r) =>
  pwdFilter.value === '' ||
  (pwdFilter.value === 'set' ? r.hasPassword !== false : r.hasPassword === false)

const filteredAdmins = computed(() =>
  admins.value.filter(
    (r) =>
      matchKeyword(r) &&
      matchStatus(r) &&
      (adminRole.value === '' || r.role === adminRole.value),
  ),
)
const filteredUsers = computed(() =>
  endusers.value.filter((r) => matchKeyword(r) && matchStatus(r) && matchPassword(r)),
)

function resetFilters() {
  kw.value = ''
  adminRole.value = ''
  statusFilter.value = ''
  pwdFilter.value = ''
}

function openCreate() {
  editing.value = null
  Object.assign(form, {
    username: '',
    displayName: '',
    password: '',
    role: activeTab.value === 'user' ? 'enduser' : 'unit',
    isActive: true,
    unitIds: [],
  })
  dialogVisible.value = true
  formRef.value?.clearValidate()
}
function openEdit(row) {
  if (isSelf(row)) {
    ElMessage.info('自己的账号请在「修改密码」页操作，不能用此功能改自己')
    return
  }
  if (row.role === 'root' && !isRoot.value) {
    ElMessage.info('只有系统最高管理员（root）可修改该账号')
    return
  }
  editing.value = row
  Object.assign(form, {
    username: row.username,
    displayName: row.displayName || '',
    password: '',
    role: row.role,
    isActive: row.isActive,
    unitIds: [...(row.unitIds || [])],
  })
  dialogVisible.value = true
  formRef.value?.clearValidate()
}

const usernameDisabled = computed(() => editing.value != null)
const title = computed(() => (editing.value ? `编辑账号：${editing.value.username}` : '新建账号'))

// MANAGED_ROLES 需要密码才能登录网页后台的角色（只读的 enduser 可无密码）。
const MANAGED_ROLES = ['root', 'super', 'unit']
// 编辑态：目标账号当前无法密码登录（激活码创建，存的是哨兵值），且要提升为管理角色 → 必须设置密码。
const editPasswordRequired = computed(
  () =>
    editing.value != null &&
    editing.value.hasPassword === false &&
    MANAGED_ROLES.includes(form.role),
)

const rules = {
  username: editing.value
    ? {}
    : [{ required: true, message: '请输入用户名', trigger: 'blur' }],
  password: [
    {
      validator: (_r, v, cb) => {
        const val = String(v ?? '')
        if (val) {
          if (val.length < 6) return cb(new Error('密码至少 6 位'))
          return cb()
        }
        // 留空：编辑态仅在“提升无密码账号为管理角色”时必填，其余留空 = 不修改密码。
        if (editPasswordRequired.value) return cb(new Error('该账号尚未设置密码，请设置初始密码'))
        // 新建态：只读用户可留空（= 暂不设置密码），管理角色必须给初始密码。
        if (!editing.value && form.role !== 'enduser') return cb(new Error('请输入初始密码'))
        cb()
      },
      trigger: 'blur',
    },
  ],
  unitIds: [
    {
      validator: (_r, v, cb) => {
        if (form.role === 'unit' && (!v || v.length === 0))
          return cb(new Error('单位管理员至少绑定一个单位'))
        cb()
      },
      trigger: 'change',
    },
  ],
}

async function submit() {
  const valid = await formRef.value.validate().catch(() => false)
  if (!valid) return
  if (form.role === 'unit' && form.unitIds.length === 0) {
    ElMessage.warning('单位管理员至少需绑定一个单位')
    return
  }
  if (editPasswordRequired.value && !form.password) {
    ElMessage.warning('该账号尚未设置密码，提升为管理角色前请先设置初始密码')
    return
  }
  saving.value = true
  try {
    const payload = {
      displayName: form.displayName.trim(),
      role: form.role,
      isActive: form.isActive,
      unitIds: ['super', 'root'].includes(form.role) ? [] : form.unitIds,
    }
    if (editing.value) {
      const roleChanged = editing.value.role !== form.role
      if (form.password) payload.password = form.password
      await updateAdminUser(editing.value.id, payload)
      ElMessage.success(
        roleChanged
          ? `已改为「${roleName(form.role)}」，该账号已移至${form.role === 'enduser' ? '「用户管理」' : '「管理员管理」'}标签页`
          : '已保存账号设置',
      )
    } else {
      await createAdminUser({ ...payload, username: form.username.trim(), password: form.password })
      ElMessage.success(
        form.password ? '已创建账号（首次登录需修改初始密码）' : '已创建账号（尚未设置密码）',
      )
    }
    dialogVisible.value = false
    await load()
  } catch (e) {
    ElMessage.error(e.message)
  } finally {
    saving.value = false
  }
}

// 删除账号（super/root）：级联删除其设备、令牌、会话、单位绑定与单点授权，不可恢复。
async function onDelete(row) {
  if (isSelf(row)) {
    ElMessage.info('不能删除自己的账号')
    return
  }
  try {
    await ElMessageBox.confirm(
      `确定删除账号「${row.username}」吗？\n将同时删除其设备绑定、登录令牌、单位绑定与单点授权，且不可恢复。`,
      '删除账号',
      {
        type: 'warning',
        confirmButtonText: '删除',
        confirmButtonClass: 'el-button--danger',
        cancelButtonText: '取消',
      },
    )
  } catch (_) {
    return
  }
  try {
    await deleteAdminUser(row.id)
    ElMessage.success('已删除账号')
    await load()
  } catch (e) {
    ElMessage.error(e.message)
  }
}

async function onResetPassword(row) {
  if (isSelf(row)) {
    ElMessage.info('自己的密码请在「修改密码」页修改')
    return
  }
  let val
  try {
    ;({ value: val } = await ElMessageBox.prompt(
      `为「${row.username}」设置新密码。重置后该账号必须修改密码，其所有已登录会话将立即失效。`,
      '重置密码',
      {
        inputType: 'password',
        inputPattern: /^.{6,}$/,
        inputErrorMessage: '密码至少 6 位',
        confirmButtonText: '重置',
        cancelButtonText: '取消',
      },
    ))
  } catch (_) {
    return
  }
  try {
    await resetAdminPassword(row.id, { newPassword: val })
    ElMessage.success('密码已重置')
    await load()
  } catch (e) {
    ElMessage.error(e.message)
  }
}
</script>

<template>
  <div>
    <el-tabs v-model="activeTab" class="tabs">
      <!-- ==================== 管理员管理 ==================== -->
      <el-tab-pane name="admin">
        <template #label>
          管理员管理
          <el-badge :value="admins.length" :max="999" type="info" class="tab-badge" />
        </template>

        <div class="toolbar">
          <el-input
            v-model="kw"
            placeholder="搜索用户名 / 显示名 / 单位"
            :prefix-icon="Search"
            clearable
            class="search"
          />
          <el-select v-model="adminRole" placeholder="全部角色" clearable style="width: 150px">
            <el-option label="系统管理员（root）" value="root" />
            <el-option label="超级管理员" value="super" />
            <el-option label="单位管理员" value="unit" />
          </el-select>
          <el-select v-model="statusFilter" placeholder="全部状态" clearable style="width: 120px">
            <el-option label="启用" value="active" />
            <el-option label="停用" value="inactive" />
          </el-select>
          <el-button :icon="Refresh" @click="resetFilters">重置</el-button>
          <div class="spacer" />
          <el-button type="primary" :icon="Plus" @click="openCreate">新建管理员</el-button>
        </div>

        <div class="tip">
          超级管理员可创建管理员账号并分配其负责的单位；单位管理员只能看到/修改这些单位的联系人。
        </div>

        <el-table :data="filteredAdmins" v-loading="loading" border stripe class="table">
          <el-table-column prop="username" label="用户名" min-width="120" />
          <el-table-column prop="displayName" label="显示名" min-width="110">
            <template #default="{ row }">{{ row.displayName || '—' }}</template>
          </el-table-column>
          <el-table-column label="角色" width="130">
            <template #default="{ row }">
              <el-tag :type="roleTag(row.role)" effect="light" size="small">{{ roleName(row.role) }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="负责单位" min-width="180">
            <template #default="{ row }">
              <span v-if="!['super', 'root'].includes(row.role)">{{ (row.unitNames || []).join('、') || '（未绑定）' }}</span>
              <span v-else class="all">全部</span>
            </template>
          </el-table-column>
          <el-table-column label="状态" width="90" align="center">
            <template #default="{ row }">
              <el-tag :type="row.isActive ? 'success' : 'info'" effect="plain" size="small">
                {{ row.isActive ? '启用' : '停用' }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column label="密码" width="100" align="center">
            <template #default="{ row }">
              <el-tag v-if="row.hasPassword === false" type="info" effect="plain" size="small">未设置</el-tag>
              <el-tag v-else-if="row.mustChangePassword" type="warning" effect="plain" size="small">待修改</el-tag>
              <el-tag v-else type="success" effect="plain" size="small">已设置</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="操作" width="220" align="right">
            <template #default="{ row }">
              <el-button v-if="canManage(row)" link type="primary" @click="openEdit(row)">编辑</el-button>
              <el-button v-if="canManage(row)" link type="warning" @click="onResetPassword(row)">重置密码</el-button>
              <el-button v-if="canManage(row)" link type="danger" @click="onDelete(row)">删除</el-button>
              <span v-if="isSelf(row)" class="self-hint">当前登录账号</span>
              <span v-else-if="!canManage(row)" class="self-hint">仅 root 可操作</span>
            </template>
          </el-table-column>
          <template #empty>
            <el-empty description="没有匹配的管理员账号" :image-size="80" />
          </template>
        </el-table>
      </el-tab-pane>

      <!-- ==================== 用户管理（终端用户） ==================== -->
      <el-tab-pane name="user">
        <template #label>
          用户管理
          <el-badge :value="endusers.length" :max="999" type="info" class="tab-badge" />
        </template>

        <div class="toolbar">
          <el-input
            v-model="kw"
            placeholder="搜索手机号 / 姓名 / 可见单位"
            :prefix-icon="Search"
            clearable
            class="search"
          />
          <el-select v-model="statusFilter" placeholder="全部状态" clearable style="width: 120px">
            <el-option label="启用" value="active" />
            <el-option label="停用" value="inactive" />
          </el-select>
          <el-select v-model="pwdFilter" placeholder="全部密码状态" clearable style="width: 160px">
            <el-option label="已设置密码" value="set" />
            <el-option label="未设置密码" value="unset" />
          </el-select>
          <el-button :icon="Refresh" @click="resetFilters">重置</el-button>
          <div class="spacer" />
          <el-button type="primary" :icon="Plus" @click="openCreate">新建用户</el-button>
        </div>

        <div class="tip">
          终端用户仅可在手机端（激活码）或桌面端（账号密码）登录浏览，可见范围 = 绑定单位 ∪ 公开单位；
          账号可由「激活码」页从通讯录批量导入。
        </div>

        <el-table :data="filteredUsers" v-loading="loading" border stripe class="table">
          <el-table-column prop="username" label="账号（手机号/座机）" min-width="150" />
          <el-table-column prop="displayName" label="姓名" min-width="110">
            <template #default="{ row }">{{ row.displayName || '—' }}</template>
          </el-table-column>
          <el-table-column label="可见单位" min-width="200">
            <template #default="{ row }">
              {{ (row.unitNames || []).join('、') || '（仅公开单位）' }}
            </template>
          </el-table-column>
          <el-table-column label="状态" width="90" align="center">
            <template #default="{ row }">
              <el-tag :type="row.isActive ? 'success' : 'info'" effect="plain" size="small">
                {{ row.isActive ? '启用' : '停用' }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column label="密码" width="100" align="center">
            <template #default="{ row }">
              <el-tag v-if="row.hasPassword === false" type="info" effect="plain" size="small">未设置</el-tag>
              <el-tag v-else-if="row.mustChangePassword" type="warning" effect="plain" size="small">待修改</el-tag>
              <el-tag v-else type="success" effect="plain" size="small">已设置</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="操作" width="240" align="right">
            <template #default="{ row }">
              <el-button link type="primary" @click="openEdit(row)">编辑</el-button>
              <el-button link type="warning" @click="onResetPassword(row)">重置密码</el-button>
              <el-button link @click="goVisibility">可见范围</el-button>
              <el-button link type="danger" @click="onDelete(row)">删除</el-button>
            </template>
          </el-table-column>
          <template #empty>
            <el-empty description="没有匹配的终端用户" :image-size="80" />
          </template>
        </el-table>
      </el-tab-pane>
    </el-tabs>

    <el-dialog v-model="dialogVisible" :title="title" width="520px" :close-on-click-modal="false">
      <el-form ref="formRef" :model="form" :rules="rules" label-width="90px">
        <el-row :gutter="12">
          <el-col :span="12">
            <el-form-item label="用户名" prop="username">
              <el-input v-model="form.username" :disabled="usernameDisabled" maxlength="32" placeholder="3~32 位字母/数字/_/-/." />
            </el-form-item>
          </el-col>
          <el-col :span="12">
            <el-form-item label="显示名" prop="displayName">
              <el-input v-model="form.displayName" maxlength="50" placeholder="如：张三" />
            </el-form-item>
          </el-col>
        </el-row>
        <el-form-item v-if="editing || activeTab === 'admin'" label="角色" prop="role">
          <el-radio-group v-model="form.role">
            <el-radio v-if="isRoot" value="root">系统管理员</el-radio>
            <el-radio value="super">超级管理员</el-radio>
            <el-radio value="unit">单位管理员</el-radio>
            <el-radio v-if="editing" value="enduser">终端用户</el-radio>
          </el-radio-group>
          <span v-if="editing" class="hint">
            改为「终端用户」= 只读账号，仅可浏览绑定/公开单位，无法进入后台
          </span>
        </el-form-item>
        <el-form-item
          v-if="form.role === 'unit' || form.role === 'enduser'"
          :label="form.role === 'unit' ? '负责单位' : '可见单位'"
          prop="unitIds"
        >
          <el-select
            v-model="form.unitIds"
            multiple
            filterable
            :placeholder="form.role === 'unit' ? '可多选（至少一个）' : '可多选（可不选，仅浏览公开单位）'"
            style="width: 100%"
          >
            <el-option v-for="u in units" :key="u.id" :label="u.name" :value="u.id" />
          </el-select>
          <span v-if="editing && form.role === 'enduser'" class="hint">
            由管理员降级而来时，原绑定单位会保留，可在此增减
          </span>
        </el-form-item>
        <template v-if="editing">
          <el-form-item label="启用状态" prop="isActive">
            <el-switch v-model="form.isActive" active-text="启用" inactive-text="停用" />
            <span class="hint">停用后该账号的所有会话与设备令牌将立即失效</span>
          </el-form-item>
          <el-form-item label="设置新密码" prop="password">
            <el-input
              v-model="form.password"
              type="password"
              show-password
              maxlength="64"
              :placeholder="editPasswordRequired ? '该账号尚未设置密码，必填（至少 6 位）' : '留空则不修改密码'"
            />
            <span v-if="editPasswordRequired" class="hint">
              该账号当前无法密码登录，提升为管理角色前必须先设置初始密码
            </span>
            <span v-else-if="form.password" class="hint">保存后该账号需重新改密，其已登录会话将失效</span>
          </el-form-item>
        </template>
        <el-form-item v-else label="初始密码" prop="password">
          <el-input
            v-model="form.password"
            type="password"
            show-password
            maxlength="64"
            :placeholder="form.role === 'enduser' ? '可留空：暂不设置密码' : '至少 6 位（首次登录需修改）'"
          />
          <span v-if="form.role === 'enduser'" class="hint">
            留空 = 该账号「尚未设置密码」：现在不能密码登录，后续可由管理员重置，或客户端激活后自行设置
          </span>
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
.tabs :deep(.el-tabs__header) { margin-bottom: 12px; }
.tab-badge { margin-left: 6px; }
.tab-badge :deep(.el-badge__content) { transform: translateY(-1px); }
.toolbar { display: flex; gap: 10px; align-items: center; margin-bottom: 8px; flex-wrap: wrap; }
.toolbar .search { width: 280px; }
.spacer { flex: 1; }
.tip { color: #64748b; font-size: 13px; margin: 0 0 10px 2px; }
.table { border-radius: 8px; overflow: hidden; }
.all { color: #059669; }
.self-hint { color: #94a3b8; font-size: 12px; }
.hint { margin-left: 10px; color: #94a3b8; font-size: 12px; }
</style>
