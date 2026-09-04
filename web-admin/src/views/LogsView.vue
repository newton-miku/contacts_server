<script setup>
import { onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { Search, Refresh } from '@element-plus/icons-vue'
import { queryLogs, fmtTime } from '../api'

const rows = ref([])
const total = ref(0)
const loading = ref(false)
const filters = reactive({ category: '', user: '', from: '', to: '' })
const page = ref(1)
const pageSize = ref(20)

const categoryOptions = [
  { value: '', label: '全部分类' },
  { value: 'contact', label: '联系人数据' },
  { value: 'auth', label: '登录 / 改密' },
  { value: 'account', label: '管理员账号' },
  { value: 'unit', label: '单位维护' },
]

const categoryLabel = (c) => categoryOptions.find((o) => o.value === c)?.label || c
const actionLabel = (a) => actionMap[a] || a
const actionMap = {
  create: '新增', update: '修改', delete: '删除', import: '导入',
  login: '登录', login_fail: '登录失败', logout: '退出', change_password: '改密',
  create_user: '新建账号', update_user: '修改账号', reset_password: '重置密码',
}

async function load() {
  loading.value = true
  try {
    const res = await queryLogs({
      category: filters.category || undefined,
      user: filters.user || undefined,
      from: filters.from || undefined,
      to: filters.to || undefined,
      page: page.value,
      pageSize: pageSize.value,
    })
    rows.value = res.items || []
    total.value = res.total || 0
  } catch (e) {
    ElMessage.error(`读取日志失败：${e.message}`)
  } finally {
    loading.value = false
  }
}

function search() {
  page.value = 1
  load()
}
function reset() {
  Object.assign(filters, { category: '', user: '', from: '', to: '' })
  search()
}

onMounted(load)
</script>

<template>
  <div>
    <div class="toolbar">
      <el-select v-model="filters.category" style="width: 150px" @change="search">
        <el-option v-for="o in categoryOptions" :key="o.value" :label="o.label" :value="o.value" />
      </el-select>
      <el-input v-model="filters.user" placeholder="操作者用户名" clearable style="width: 160px" @keyup.enter="search" @clear="search" />
      <el-date-picker
        v-model="filters.from"
        type="date"
        placeholder="开始日期"
        value-format="YYYY-MM-DD"
        style="width: 150px"
        @change="search"
      />
      <span class="sep">—</span>
      <el-date-picker
        v-model="filters.to"
        type="date"
        placeholder="结束日期"
        value-format="YYYY-MM-DD"
        style="width: 150px"
        @change="search"
      />
      <el-button type="primary" :icon="Search" @click="search">查询</el-button>
      <el-button :icon="Refresh" @click="reset">重置</el-button>
    </div>

    <div class="summary">共 <b>{{ total }}</b> 条日志</div>

    <el-table :data="rows" v-loading="loading" border stripe size="small" class="table">
      <el-table-column label="时间" width="170">
        <template #default="{ row }">{{ fmtTime(row.createdAt) }}</template>
      </el-table-column>
      <el-table-column prop="username" label="操作者" width="130">
        <template #default="{ row }">{{ row.username || '（匿名）' }}</template>
      </el-table-column>
      <el-table-column label="分类" width="120">
        <template #default="{ row }">
          <el-tag size="small" effect="plain">{{ categoryLabel(row.category) }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="动作" width="110">
        <template #default="{ row }">{{ actionLabel(row.action) }}</template>
      </el-table-column>
      <el-table-column prop="detail" label="详情" min-width="260" show-overflow-tooltip />
      <el-table-column prop="ip" label="来源 IP" width="140" />
    </el-table>

    <div class="pager">
      <el-pagination
        v-model:current-page="page"
        v-model:page-size="pageSize"
        :total="total"
        :page-sizes="[20, 50, 100]"
        layout="total, sizes, prev, pager, next"
        background
        @current-change="load"
        @size-change="search"
      />
    </div>
  </div>
</template>

<style scoped>
.toolbar { display: flex; gap: 10px; align-items: center; margin-bottom: 8px; flex-wrap: wrap; }
.sep { color: #94a3b8; }
.summary { color: #64748b; font-size: 13px; margin: 0 0 8px 2px; }
.table { border-radius: 8px; overflow: hidden; }
.pager { display: flex; justify-content: flex-end; margin-top: 10px; }
</style>
