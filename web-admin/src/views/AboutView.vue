<script setup>
import { onMounted, ref } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { fmtTime, getVersion } from '../api'
import { session } from '../stores/session'

const meta = ref(null) // /api/version 返回：版本/构建时间(程序) + 数据版本/结构(库)
const loading = ref(false)

async function load() {
  loading.value = true
  try {
    meta.value = await getVersion()
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <div class="toolbar">
      <div class="tip">系统版本与运行信息。页脚常驻显示版本号与构建时间。</div>
      <el-button :icon="Refresh" :loading="loading" @click="load">刷新</el-button>
    </div>

    <el-card shadow="never" v-loading="loading" class="card">
      <template #header><b>系统信息</b></template>
      <el-descriptions :column="1" border>
        <el-descriptions-item label="系统名称">石泉县通讯录管理系统</el-descriptions-item>
        <el-descriptions-item label="用途">
          全县/单位通讯录的统一录入、导入导出、号码查重、单位公开与按角色可见（super / 单位管理员 / 手机只读）。
        </el-descriptions-item>
        <el-descriptions-item label="技术栈">
          Go + SQLite 后端（内嵌本管理页）· Vue 3 + Element Plus · Flutter 手机客户端（浏览/搜索/呼叫/来电显示）
        </el-descriptions-item>
        <el-descriptions-item label="当前登录">{{ session.user?.displayName || session.user?.username || '—' }}</el-descriptions-item>
      </el-descriptions>
    </el-card>

    <el-card shadow="never" class="card">
      <template #header><b>版本信息</b></template>
      <el-descriptions :column="2" border v-if="meta">
        <el-descriptions-item label="服务端版本">{{ meta.appVersion || 'dev' }}</el-descriptions-item>
        <el-descriptions-item label="构建时间">{{ fmtTime(meta.buildTime) }}</el-descriptions-item>
        <el-descriptions-item label="数据库结构版本">v{{ meta.schemaVersion }}</el-descriptions-item>
        <el-descriptions-item label="数据版本">{{ meta.version }}</el-descriptions-item>
        <el-descriptions-item label="数据更新时间">{{ fmtTime(meta.updatedAt) }}</el-descriptions-item>
      </el-descriptions>
      <div class="hint">
        说明：<b>服务端版本/构建时间</b>标识运行的程序本身；<b>数据版本</b>在每次写入时 +1，
        手机客户端据此判断缓存是否需要重拉全量；<b>数据库结构版本</b>由服务端启动迁移维护。
      </div>
    </el-card>
  </div>
</template>

<style scoped>
.toolbar { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 12px; }
.tip { color: #64748b; font-size: 13px; }
.card { margin-bottom: 12px; }
.hint { margin-top: 14px; color: #64748b; font-size: 13px; line-height: 1.7; }
</style>
