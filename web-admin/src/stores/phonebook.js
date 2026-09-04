import { reactive } from 'vue'
import { ElMessage } from 'element-plus'
import { fetchSnapshot } from '../api'

// 通讯录数据（全局单例），供侧边栏头部版本徽标与各页面共享。
export const pb = reactive({
  loading: false,
  contacts: [],
  version: 0,
  updatedAt: null,
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

// 清空联系人缓存：退出登录 / 切换账号时调用，避免残留上一账号（不同数据范围）的数据。
export function clearPhonebook() {
  Object.assign(pb, { loading: false, contacts: [], version: 0, updatedAt: null })
}
