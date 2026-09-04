<script setup>
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { Refresh, Search, Phone } from '@element-plus/icons-vue'
import { fetchDuplicates } from '../api'

const loading = ref(false)
const groups = ref([])
const keyword = ref('')
// 勾选后：只显示「不同联系人之间共用」的号码；同一个人两个字段写了同号（录入重复）不再显示。
const crossOnly = ref(false)

async function load() {
  loading.value = true
  try {
    const res = await fetchDuplicates()
    groups.value = res?.groups ?? []
  } catch (e) {
    ElMessage.error(`号码查重失败：${e.message}`)
  } finally {
    loading.value = false
  }
}

const filtered = computed(() => {
  const kw = (keyword.value || '').trim().toLowerCase()
  return groups.value.filter((g) => {
    if (crossOnly.value) {
      // 一个号码必须被 ≥2 个不同联系人使用才保留
      const ids = new Set(g.refs.map((r) => r.contactId))
      if (ids.size < 2) return false
    }
    if (!kw) return true
    if (g.number.includes(kw)) return true
    return g.refs.some((r) => `${r.name}${r.unit}${r.field}`.toLowerCase().includes(kw))
  })
})

const statText = computed(() => {
  const cross = groups.value.filter((g) => new Set(g.refs.map((r) => r.contactId)).size >= 2).length
  const single = groups.value.length - cross
  return `共 ${groups.value.length} 个号码被重复使用：不同联系人共用 ${cross} 个，同一人两字段同号 ${single} 个。`
})

async function copyNumber(g) {
  try {
    await navigator.clipboard.writeText(g.number)
    ElMessage.success(`已复制：${g.number}`)
  } catch (_) {
    ElMessage.error('复制失败')
  }
}

onMounted(load)
</script>

<template>
  <div>
    <el-alert type="info" :closable="false" show-icon class="hint">
      <div>
        把每个联系人的 手机 / 手机2 / 座机 / 传真 按纯数字归类，列出<strong>被使用 ≥2 次</strong>的号码。
        同一办公室线路被多人共用（如办公室主任与办公室同号）属正常；同一人两个字段写了同号则是录入重复，可到「联系人」里改掉。
      </div>
    </el-alert>

    <div class="toolbar">
      <el-input
        v-model="keyword"
        placeholder="按号码 / 姓名 / 单位筛选…"
        :prefix-icon="Search"
        clearable
        class="search"
      />
      <el-checkbox v-model="crossOnly">只看不同联系人之间共用的号码</el-checkbox>
      <el-button :icon="Refresh" :loading="loading" @click="load">刷新</el-button>
    </div>

    <div class="summary">{{ statText }}</div>

    <el-table :data="filtered" v-loading="loading" border stripe class="table">
      <el-table-column label="号码" min-width="170">
        <template #default="{ row }">
          <span class="num">{{ row.number }}</span>
          <el-button link type="primary" size="small" @click="copyNumber(row)">
            <el-icon><Phone /></el-icon>
          </el-button>
        </template>
      </el-table-column>
      <el-table-column label="使用处数" width="110" align="center">
        <template #default="{ row }">
          <el-tag :type="row.count > 2 ? 'danger' : 'warning'" effect="light">{{ row.count }} 处</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="使用条目" min-width="340">
        <template #default="{ row }">
          <div class="refs">
            <el-tag
              v-for="(r, i) in row.refs"
              :key="i"
              type="info"
              effect="plain"
              class="ref"
            >{{ r.name }} · {{ r.unit || '—' }} · {{ r.field }}列</el-tag>
          </div>
        </template>
      </el-table-column>
      <template #empty>
        <el-empty description="没有重复使用的号码" :image-size="90" />
      </template>
    </el-table>
  </div>
</template>

<style scoped>
.hint { margin-bottom: 12px; }
.toolbar { display: flex; gap: 12px; flex-wrap: wrap; align-items: center; margin-bottom: 8px; }
.toolbar .search { flex: 1; min-width: 220px; max-width: 320px; }
.summary { color: #64748b; font-size: 13px; margin: 0 0 8px 2px; }
.table { border-radius: 8px; overflow: hidden; }
.num { color: #2563eb; font-variant-numeric: tabular-nums; font-weight: 500; }
.refs { display: flex; flex-wrap: wrap; gap: 4px; }
.ref { margin: 0; }
</style>
