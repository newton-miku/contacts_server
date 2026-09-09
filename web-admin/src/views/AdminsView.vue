<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh, Key } from '@element-plus/icons-vue'
import { listAdminUsers, createAdminUser, updateAdminUser, resetAdminPassword, deleteAdminUser, listUnits } from '../api'
import { session } from '../stores/session'

const router = useRouter()
const isRoot = computed(() => session.user?.role === 'root')

const rows = ref([])
const units = ref([]) // 全部单位（超管维护管理员用）
const loading = ref(false)
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
const roleName = (r) => ({ root: '系统管理员', super: '超级管理员', unit: '单位管理员', enduser: '只读用户' })[r] || r
const roleTag = (r) => ({ root: 'danger', super: 'warning', unit: 'primary', enduser: 'success' })[r] || 'info'
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

function openCreate() {
  editing.value = null
  Object.assign(form, { username: '', displayName: '', password: '', role: 'unit', isActive: true, unitIds: [] })
  dialogVisible.value = true
  formRef.value?.clearValidate()
}
function openEdit(row) {
  if (isSelf(row)) {
    ElMessage.info('自己的账号请在「修改密码」页操作，不能用此功能改自己')
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

const rules = {
  username: editing.value
    ? {}
    : [{ required: true, message: '请输入用户名', trigger: 'blur' }],
  password: editing.value
    ? {}
    : [
        {
          validator: (_r, v, cb) => {
            if (form.role === 'enduser') return cb() // 只读用户可留空 = 暂不设置密码
            if (!v) return cb(new Error('请输入初始密码'))
            if (String(v).length < 6) return cb(new Error('初始密码至少 6 位'))
            cb()
          },
          trigger: 'blur',
        },
      ],
  unitIds: [
    {
      validator: (_r, v, cb) => {
        if (form.role === 'unit' && (!v || v.length === 0)) return cb(new Error('单位管理员至少绑定一个单位'))
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
  saving.value = true
  try {
    const payload = {
      displayName: form.displayName.trim(),
      role: form.role,
      isActive: form.isActive,
      unitIds: ['super', 'root'].includes(form.role) ? [] : form.unitIds,
    }
    if (editing.value) {
      await updateAdminUser(editing.value.id, payload)
      ElMessage.success('已保存账号设置')
    } else {
      await createAdminUser({ ...payload, username: form.username.trim(), password: form.password })
      ElMessage.success('已创建账号（首次登录需修改初始密码）')
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
      `确定删除账号「${row.username}」吗？
将同时删除其设备绑定、登录令牌、单位绑定与单点授权，且不可恢复。`,
      '删除账号',
      { type: 'warning', confirmButtonText: '删除', confirmButtonClass: 'el-button--danger', cancelButtonText: '取消' },
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
      { inputType: 'password', inputPattern: /^.{6,}$/, inputErrorMessage: '密码至少 6 位', confirmButtonText: '重置', cancelButtonText: '取消' },
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
    <div class="toolbar">
      <div class="tip">超级管理员可创建管理员/只读账号并分配其负责的单位；单位管理员只能看到/修改这些单位的联系人，只读账号仅可在手机端浏览绑定单位与公开单位。</div>
      <el-button :icon="Refresh" @click="load">刷新</el-button>
      <el-button type="primary" :icon="Plus" @click="openCreate">新建账号</el-button>
    </div>

    <el-table :data="rows" v-loading="loading" border stripe class="table">
      <el-table-column prop="username" label="用户名" min-width="120" />
      <el-table-column prop="displayName" label="显示名" min-width="110">
        <template #default="{ row }">{{ row.displayName || '—' }}</template>
      </el-table-column>
      <el-table-column label="角色" width="120">
        <template #default="{ row }">
          <el-tag :type="roleTag(row.role)" effect="light" size="small">{{ roleName(row.role) }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="负责单位" min-width="170">
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
      <el-table-column label="操作" width="300" align="right">
        <template #default="{ row }">
          <el-button v-if="!isSelf(row)" link type="primary" @click="openEdit(row)">编辑</el-button>
          <el-button v-if="!isSelf(row)" link type="warning" @click="onResetPassword(row)">重置密码</el-button>
          <el-button v-if="!['super', 'root'].includes(row.role)" link @click="goVisibility">可见范围</el-button>
          <el-button v-if="!isSelf(row)" link type="danger" @click="onDelete(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>

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
        <el-form-item label="角色" prop="role">
          <el-radio-group v-model="form.role">
            <el-radio v-if="isRoot" value="root">系统管理员</el-radio>
            <el-radio value="super">超级管理员</el-radio>
            <el-radio value="unit">单位管理员</el-radio>
            <el-radio value="enduser">只读用户</el-radio>
          </el-radio-group>
          <span v-if="form.role === 'enduser'" class="hint">仅可在手机端登录浏览，不可登录本后台</span>
        </el-form-item>
        <el-form-item v-if="form.role === 'unit' || form.role === 'enduser'" label="负责单位" prop="unitIds">
          <el-select
            v-model="form.unitIds"
            multiple
            :placeholder="form.role === 'unit' ? '可多选（至少一个）' : '可多选（可不选，仅浏览公开单位）'"
            style="width: 100%"
          >
            <el-option v-for="u in units" :key="u.id" :label="u.name" :value="u.id" />
          </el-select>
        </el-form-item>
        <template v-if="editing">
          <el-form-item label="启用状态" prop="isActive">
            <el-switch v-model="form.isActive" active-text="启用" inactive-text="停用" />
            <span class="hint">停用后该账号的所有会话将立即失效</span>
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
.toolbar { display: flex; gap: 10px; align-items: center; margin-bottom: 10px; flex-wrap: wrap; }
.tip { flex: 1; color: #64748b; font-size: 13px; }
.table { border-radius: 8px; overflow: hidden; }
.all { color: #059669; }
.hint { margin-left: 10px; color: #94a3b8; font-size: 12px; }
</style>
