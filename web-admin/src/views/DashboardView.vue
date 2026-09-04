<script setup>
import { computed, onMounted } from 'vue'
import { Phone, OfficeBuilding, DataLine, Timer } from '@element-plus/icons-vue'
import { pb, loadPhonebook } from '../stores/phonebook'
import { fmtTime } from '../api'

onMounted(() => {
  if (!pb.contacts.length && !pb.loading) loadPhonebook({ silent: true })
})

const unitCount = computed(() => new Set(pb.contacts.map((c) => c.unit).filter(Boolean)).size)

const byUnit = computed(() => {
  const map = new Map()
  for (const c of pb.contacts) {
    const k = c.unit || '(未分组)'
    map.set(k, (map.get(k) || 0) + 1)
  }
  return [...map.entries()].sort((a, b) => String(a[0]).localeCompare(String(b[0]), 'zh'))
})

const cards = computed(() => [
  { label: '联系人总数', value: pb.contacts.length, icon: Phone, color: '#2563eb' },
  { label: '单位数', value: unitCount.value, icon: OfficeBuilding, color: '#7c3aed' },
  { label: '数据版本', value: pb.version, icon: DataLine, color: '#059669' },
  { label: '最近更新', value: pb.updatedAt ? fmtTime(pb.updatedAt) : '–', icon: Timer, color: '#d97706' },
])
</script>

<template>
  <div>
    <div class="cards">
      <el-card v-for="c in cards" :key="c.label" shadow="hover" class="card">
        <div class="cell">
          <el-icon :size="30" :color="c.color"><component :is="c.icon" /></el-icon>
          <div class="info">
            <div class="num" :title="String(c.value)">{{ c.value }}</div>
            <div class="label">{{ c.label }}</div>
          </div>
        </div>
      </el-card>
    </div>

    <el-card shadow="never" class="panel">
      <template #header>
        <div class="panel-head">
          <span>单位分布</span>
          <el-button type="primary" size="small" @click="$router.push('/contacts')">前往联系人管理</el-button>
        </div>
      </template>
      <el-table :data="byUnit.map(([name, count]) => ({ name, count }))" size="small" border>
        <el-table-column prop="name" label="单位" />
        <el-table-column prop="count" label="人数" width="120" align="center" />
      </el-table>
      <el-empty v-if="!pb.contacts.length" description="暂无联系人，去“联系人”页新增吧" :image-size="80" />
    </el-card>
  </div>
</template>

<style scoped>
.cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 14px; margin-bottom: 14px; }
.cell { display: flex; align-items: center; gap: 14px; }
.num { font-size: 24px; font-weight: 700; line-height: 1.2; }
.label { color: #64748b; font-size: 13px; }
.panel-head { display: flex; align-items: center; justify-content: space-between; }
</style>
