import { createRouter, createWebHashHistory } from 'vue-router'
import { ensureSession, session } from '../stores/session'

const routes = [
  {
    path: '/login',
    name: 'login',
    component: () => import('../views/LoginView.vue'),
    meta: { public: true },
  },
  {
    path: '/',
    component: () => import('../layout/AdminLayout.vue'),
    redirect: '/dashboard',
    children: [
      { path: 'dashboard', name: 'dashboard', component: () => import('../views/DashboardView.vue'), meta: { title: '概览' } },
      { path: 'contacts', name: 'contacts', component: () => import('../views/ContactsView.vue'), meta: { title: '联系人管理' } },
      { path: 'duplicates', name: 'duplicates', component: () => import('../views/DuplicatesView.vue'), meta: { title: '号码查重' } },
      { path: 'units', name: 'units', component: () => import('../views/UnitsView.vue'), meta: { title: '单位管理', role: 'super' } },
      { path: 'admins', name: 'admins', component: () => import('../views/AdminsView.vue'), meta: { title: '管理员管理', role: 'super' } },
      { path: 'logs', name: 'logs', component: () => import('../views/LogsView.vue'), meta: { title: '系统日志', role: 'super' } },
      { path: 'password', name: 'password', component: () => import('../views/PasswordView.vue'), meta: { title: '修改密码' } },
      { path: 'about', name: 'about', component: () => import('../views/AboutView.vue'), meta: { title: '关于' } },
    ],
  },
  { path: '/:pathMatch(.*)*', redirect: '/dashboard' },
]

const router = createRouter({
  history: createWebHashHistory(),
  routes,
})

router.beforeEach(async (to) => {
  await ensureSession()
  if (to.meta?.public) {
    // 已登录访问登录页 → 回主界面
    if (to.name === 'login' && session.user) return { name: 'dashboard' }
    return true
  }
  if (!session.user) {
    return { name: 'login', query: to.fullPath !== '/' ? { redirect: to.fullPath } : undefined }
  }
  // 首次登录（仍在用初始密码）：强制先到改密页，其它页面一律不放行。
  if (session.user.mustChangePassword && to.name !== 'password') {
    return { name: 'password' }
  }
  // 超管专属页面：单位管理员访问一律回概览。
  if (to.meta?.role === 'super' && session.user.role !== 'super') {
    return { name: 'dashboard' }
  }
  return true
})

export default router
