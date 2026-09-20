import { reactive } from 'vue'
import { ElMessage } from 'element-plus'
import { fetchSnapshot, getVersion, publishVersion } from '../api'

// 通讯录数据（全局单例），供侧边栏头部版本徽标与各页面共享。
export const pb = reactive({
  loading: false,
  contacts: [],
  version: 0,
  updatedAt: null,
  // 数据版本发布状态：改动要等「静默窗口」到期才发布（默认 15 分钟），
  // pending=true 时管理页顶栏提示「有改动待发布」并提供「立即发布」。
  pending: false,
  publishDueAt: null,
  quietMinutes: 15,
  // 程序/发布版本与库结构版本（GET /api/version，构建期 -ldflags 注入）
  appVersion: 'dev',
  buildTime: '',
  schemaVersion: 0,
})

export async function loadPhonebook({ silent = false } = {}) {
  pb.loading = true
  try {
    const snap = await fetchSnapshot()
    pb.contacts = snap.contacts || []
    pb.version = snap.version
    pb.updatedAt = snap.updatedAt
  } catch (e) {
    if (!silent) ElMessage.error(`加载联系人失败：${e.message}`)
  } finally {
    pb.loading = false
  }
}

// refreshVersion 拉取 GET /api/version（几十字节的纯读接口）：刷新数据版本、待发布状态
// 与程序版本信息。改动联系人后调用它，顶栏才会立刻提示「有改动待发布」。
export async function refreshVersion() {
  const m = await getVersion()
  pb.version = m.version ?? pb.version
  pb.updatedAt = m.updatedAt ?? pb.updatedAt
  pb.pending = m.pending === true
  pb.publishDueAt = m.publishDueAt || null
  pb.quietMinutes = m.quietMinutes ?? pb.quietMinutes
  if (m.appVersion) pb.appVersion = m.appVersion
  if (m.buildTime !== undefined) pb.buildTime = m.buildTime
  if (m.schemaVersion !== undefined) pb.schemaVersion = m.schemaVersion
  return m
}

// publishVersionNow 管理员手动发布：把待发布的改动合并成一次版本递增，手机端下次探测即同步。
// 返回 { version, published }；没有待发布改动时 published=false（幂等）。
export async function publishVersionNow() {
  const res = await publishVersion()
  await refreshVersion()
  return res
}

// 清空联系人缓存：退出登录 / 切换账号时调用，避免残留上一账号（不同数据范围）的数据。
export function clearPhonebook() {
  Object.assign(pb, {
    loading: false,
    contacts: [],
    version: 0,
    updatedAt: null,
    pending: false,
    publishDueAt: null,
  })
}
