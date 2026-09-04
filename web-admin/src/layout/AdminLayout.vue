<script setup>
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { DataBoard, Phone, Lock, SwitchButton, User, OfficeBuilding, Avatar, Tickets, Search } from '@element-plus/icons-vue'
import { session, signOut } from '../stores/session'
import { pb, loadPhonebook } from '../stores/phonebook'
import { fmtTime, getVersion } from '../api'

const route = useRoute()
const router = useRouter()

// 程序/发布版本（GET /api/version 的 appVersion，构建期 -ldflags 注入）。
// 与「数据版本」（meta.data_version，每次写 +1，客户端缓存同步用）是两个概念：
// 顶栏主文案显示程序版本，悬停提示展示 数据版本/结构版本/更新时间。
const appMeta = ref({ appVersion: 'dev', version: 0, updatedAt: '', schemaVersion: 0 })
const versionTitle = computed(
  () => `数据版本 ${appMeta.value.version} · 结构 v${appMeta.value.schemaVersion} · 更新于 ${fmtTime(appMeta.value.updatedAt)}`,
)
const userName = computed(() => {
  const u = session.user
  return u?.displayName ? `${u.displayName}（${u.username}）` : u?.username ?? ''
})
// 首次登录强制改密期间，侧边栏只放开「修改密码」，其余菜单项禁用。
const mustChange = computed(() => session.user?.mustChangePassword === true)
const isSuper = computed(() => session.user?.role === 'super')
const roleText = computed(() =>
  ({ super: '超级管理员', unit: '单位管理员', enduser: '只读用户（仅手机端）' })[session.user?.role] ?? '',
)

async function logout() {
  await signOut()
  router.replace('/login')
}

onMounted(() => {
  getVersion()
    .then((m) => {
      if (m?.appVersion) appMeta.value = m
    })
    .catch(() => {})
  if (!pb.contacts.length && !pb.loading) loadPhonebook({ silent: true })
})
</script>

<template>
  <el-container class="layout">
    <!-- 侧边栏 -->
    <el-aside width="200px" class="aside">
      <div class="logo">
        <el-icon :size="22" color="#60a5fa"><Phone /></el-icon>
        <span>通讯录管理</span>
      </div>
      <el-menu :default-active="route.path" router class="menu">
        <el-menu-item index="/dashboard" :disabled="mustChange">
          <el-icon><DataBoard /></el-icon>
          <span>概览</span>
        </el-menu-item>
        <el-menu-item index="/contacts" :disabled="mustChange">
          <el-icon><Phone /></el-icon>
          <span>联系人</span>
        </el-menu-item>
        <el-menu-item index="/duplicates" :disabled="mustChange">
          <el-icon><Search /></el-icon>
          <span>号码查重</span>
        </el-menu-item>
        <template v-if="isSuper">
          <el-menu-item index="/units" :disabled="mustChange">
            <el-icon><OfficeBuilding /></el-icon>
            <span>单位管理</span>
          </el-menu-item>
          <el-menu-item index="/admins" :disabled="mustChange">
            <el-icon><Avatar /></el-icon>
            <span>管理员管理</span>
          </el-menu-item>
          <el-menu-item index="/logs" :disabled="mustChange">
            <el-icon><Tickets /></el-icon>
            <span>系统日志</span>
          </el-menu-item>
        </template>
        <el-menu-item index="/password">
          <el-icon><Lock /></el-icon>
          <span>修改密码</span>
          <el-badge v-if="mustChange" value="!" class="menu-badge" />
        </el-menu-item>
      </el-menu>
    </el-aside>

    <el-container class="body">
      <!-- 顶栏 -->
      <el-header class="topbar">
        <div class="crumb">{{ route.meta.title || '' }}</div>
        <div class="actions">
          <el-tag type="primary" effect="light" :title="versionTitle">
            版本 {{ appMeta.appVersion || 'dev' }}
          </el-tag>
          <el-tooltip :content="roleText || ''" placement="bottom">
            <el-tag :type="isSuper ? 'danger' : 'warning'" effect="light" size="small">
              {{ roleText }}
            </el-tag>
          </el-tooltip>
          <el-tooltip content="当前登录用户" placement="bottom">
            <span class="who"><el-icon><User /></el-icon> {{ userName }}</span>
          </el-tooltip>
          <el-button text @click="logout">
            <el-icon><SwitchButton /></el-icon>&nbsp;退出
          </el-button>
        </div>
      </el-header>

      <!-- 内容区 -->
      <el-main class="main">
        <router-view />
      </el-main>
    </el-container>
  </el-container>
</template>

<style scoped>
.layout { height: 100vh; }
.aside {
  background: #0f172a;
  color: #cbd5e1;
  display: flex;
  flex-direction: column;
}
.logo {
  height: 60px;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  font-weight: 600;
  color: #f1f5f9;
  font-size: 15px;
  border-bottom: 1px solid #1e293b;
  flex-shrink: 0;
}
.menu {
  border-right: none;
  background: transparent;
  flex: 1;
}
.menu :deep(.el-menu-item) { color: #cbd5e1; }
.menu :deep(.el-menu-item:hover) { background: #1e293b; color: #fff; }
.menu :deep(.el-menu-item.is-active) {
  background: #2563eb;
  color: #fff;
}
.menu-badge { margin-left: 6px; }
.menu-badge :deep(.el-badge__content) { background: #f59e0b; }
.body { flex-direction: column; min-width: 0; }
.topbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  background: #fff;
  border-bottom: 1px solid #e5e7eb;
}
.crumb { font-size: 16px; font-weight: 600; }
.actions { display: flex; align-items: center; gap: 10px; }
.who { display: inline-flex; align-items: center; gap: 4px; color: #475569; font-size: 13px; }
.main { background: #f1f5f9; overflow: auto; }
</style>
