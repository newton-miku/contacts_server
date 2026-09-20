<script setup>
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { DataBoard, Phone, Lock, SwitchButton, User, OfficeBuilding, Avatar, Tickets, Search, InfoFilled, Key } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import { session, signOut } from '../stores/session'
import { pb, loadPhonebook, refreshVersion, publishVersionNow } from '../stores/phonebook'
import { fmtTime } from '../api'

const route = useRoute()
const router = useRouter()

// 程序/发布版本、数据版本与结构版本（GET /api/version，构建期 -ldflags 注入）。
// 「数据版本」与「程序版本」是两个概念：数据版本是客户端缓存同步号——写操作只打脏标记，
// 后台按「静默窗口」（默认 15 分钟，root 可在系统设置里改）合并成一次 +1，
// 管理员也可以点顶栏「立即发布」把改动马上同步到手机端。
const versionTitle = computed(
  () =>
    `程序版本 ${pb.appVersion || 'dev'} · 构建 ${fmtTime(pb.buildTime)}\n` +
    `数据版本 ${pb.version} · 结构 v${pb.schemaVersion} · 更新于 ${fmtTime(pb.updatedAt)}\n` +
    (pb.pending
      ? `有改动待发布${pb.publishDueAt ? `（预计 ${fmtTime(pb.publishDueAt)} 自动发布）` : ''}`
      : '当前没有待发布的改动'),
)
const publishing = ref(false)
async function onPublishVersion() {
  publishing.value = true
  try {
    const res = await publishVersionNow()
    if (res?.published) {
      ElMessage.success(`已发布数据版本 ${res.version}，手机端下拉刷新即可看到最新数据`)
    } else {
      ElMessage.info('当前没有待发布的改动，数据版本未变')
    }
  } catch (e) {
    ElMessage.error(`发布失败：${e.message}`)
  } finally {
    publishing.value = false
  }
}

const userName = computed(() => {
  const u = session.user
  return u?.displayName ? `${u.displayName}（${u.username}）` : u?.username ?? ''
})
// 首次登录强制改密期间，侧边栏只放开「修改密码」，其余菜单项禁用。
const mustChange = computed(() => session.user?.mustChangePassword === true)
const isSuper = computed(() => ['super', 'root'].includes(session.user?.role))
const roleText = computed(() =>
  ({ root: '系统管理员', super: '超级管理员', unit: '单位管理员', enduser: '只读用户（仅手机端）' })[session.user?.role] ??
    '',
)

async function logout() {
  await signOut()
  router.replace('/login')
}

// 版本信息定时刷新（纯读的几十字节接口）：让顶栏的「有改动待发布」及时出现。
let versionTimer = null
onMounted(() => {
  refreshVersion().catch(() => {})
  versionTimer = window.setInterval(() => refreshVersion().catch(() => {}), 60000)
  if (!pb.contacts.length && !pb.loading) loadPhonebook({ silent: true })
})
onUnmounted(() => {
  if (versionTimer) window.clearInterval(versionTimer)
})
</script>

<template>
  <div class="app">
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
        <el-menu-item index="/activation-codes" :disabled="mustChange">
          <el-icon><Tickets /></el-icon>
          <span>激活码</span>
        </el-menu-item>
        <template v-if="isSuper">
          <el-menu-item index="/visibility" :disabled="mustChange">
            <el-icon><Key /></el-icon>
            <span>可见性配置</span>
          </el-menu-item>
          <el-menu-item index="/units" :disabled="mustChange">
            <el-icon><OfficeBuilding /></el-icon>
            <span>单位管理</span>
          </el-menu-item>
          <el-menu-item index="/admins" :disabled="mustChange">
            <el-icon><Avatar /></el-icon>
            <span>账号管理</span>
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
        <el-menu-item index="/about">
          <el-icon><InfoFilled /></el-icon>
          <span>关于</span>
        </el-menu-item>
      </el-menu>
    </el-aside>

    <el-container class="body">
      <!-- 顶栏 -->
      <el-header class="topbar">
        <div class="crumb">{{ route.meta.title || '' }}</div>
        <div class="actions">
          <!-- 有改动待发布：提醒管理员「手机端还没同步」，并提供立即发布 -->
          <el-tooltip v-if="pb.pending" placement="bottom" :content="versionTitle">
            <el-tag type="warning" effect="dark" class="pending-tag" @click="onPublishVersion">
              有改动待发布{{ pb.publishDueAt ? ` · ${fmtTime(pb.publishDueAt)} 自动同步` : '' }}
            </el-tag>
          </el-tooltip>
          <el-button
            v-if="pb.pending"
            type="primary"
            size="small"
            :loading="publishing"
            @click="onPublishVersion"
          >立即发布</el-button>
          <el-tag type="primary" effect="light" :title="versionTitle">
            版本 {{ pb.appVersion || 'dev' }}
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

  <!-- 页脚：常驻显示 版本号 + 构建时间（各角色、各页面始终可见） -->
  <footer class="app-footer">
    <div class="f-inner">
      <span class="f-left">石泉县通讯录管理系统 · 版本 {{ pb.appVersion || 'dev' }} · 构建 {{ fmtTime(pb.buildTime) }}</span>
      <span class="f-right">
        数据版本 {{ pb.version }} · 结构 v{{ pb.schemaVersion }} · 更新于 {{ fmtTime(pb.updatedAt) }}
        <template v-if="pb.pending">
          · <b class="pending-text">有改动待发布</b>
          <template v-if="pb.publishDueAt">（{{ fmtTime(pb.publishDueAt) }} 自动同步）</template>
        </template>
      </span>
    </div>
  </footer>
  </div>
</template>

<style scoped>
.app { height: 100vh; display: flex; flex-direction: column; }
.layout { flex: 1; min-height: 0; }
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
.pending-tag { cursor: pointer; }
.pending-text { color: #fbbf24; }
.who { display: inline-flex; align-items: center; gap: 4px; color: #475569; font-size: 13px; }
.main { background: #f1f5f9; overflow: auto; }
.app-footer {
  flex-shrink: 0;
  height: 34px;
  background: #0f172a;
  color: #94a3b8;
  font-size: 12px;
  display: flex;
  align-items: center;
}
.app-footer .f-inner {
  width: 100%;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 0 16px;
  min-width: 0;
}
.app-footer .f-right { text-align: right; }
</style>
