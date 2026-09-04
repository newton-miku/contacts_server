import { reactive } from 'vue'
import { login as apiLogin, logout as apiLogout, me } from '../api'
import { clearPhonebook } from './phonebook'

// 全局会话状态（单例）。ready 表示已向后端确认过登录态。
// mustChangePassword：账号仍在用初始密码，登录后强制先改密。
export const session = reactive({ user: null, ready: false })

/** 页面/路由守卫调用：返回当前用户或 null（幂等）。 */
export async function ensureSession() {
  if (session.ready) return session.user
  session.ready = true
  try {
    session.user = await me()
  } catch (_) {
    session.user = null
  }
  return session.user
}

export async function signIn(username, password) {
  const res = await apiLogin(username, password)
  session.user = res.user
  session.ready = true
  clearPhonebook() // 切换账号后重新按新范围拉取
  return session.user
}

/** 改密成功后用最新用户信息刷新本地态（清除强制改密标记）。 */
export function updateUser(user) {
  session.user = user
}

export async function signOut() {
  try {
    await apiLogout()
  } catch (_) {
    /* 忽略 */
  }
  session.user = null
  clearPhonebook()
}
