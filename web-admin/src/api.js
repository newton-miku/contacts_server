// 统一 API 请求封装：始终携带同源 Cookie（也兼容 Vite dev 下的 /api 代理）。
export async function api(method, path, body) {
  const res = await fetch(path, {
    method,
    credentials: 'include',
    headers: body !== undefined ? { 'Content-Type': 'application/json' } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })

  let data = null
  try {
    data = await res.json()
  } catch (_) {
    /* 非 JSON 响应 */
  }

  if (!res.ok) {
    const err = new Error(data?.error || `请求失败（HTTP ${res.status}）`)
    err.status = res.status
    throw err
  }
  return data
}

export const login = (username, password) => api('POST', '/api/auth/login', { username, password })
export const logout = () => api('POST', '/api/auth/logout')
export const me = () => api('GET', '/api/auth/me')
export const changePassword = (oldPassword, newPassword) =>
  api('POST', '/api/auth/password', { oldPassword, newPassword })
export const fetchSnapshot = () => api('GET', '/api/contacts')
export const getVersion = () => api('GET', '/api/version')
export const createContact = (payload) => api('POST', '/api/contacts', payload)
export const updateContact = (id, payload) => api('PUT', `/api/contacts/${id}`, payload)
export const deleteContact = (id) => api('DELETE', `/api/contacts/${id}`)

// ---- 单位（列表对任意管理员可见；写操作由后端限定 super）----
export const listUnits = () => api('GET', '/api/units')
export const createUnit = (payload) => api('POST', '/api/units', payload)
export const updateUnit = (id, payload) => api('PUT', `/api/units/${id}`, payload)
export const deleteUnit = (id) => api('DELETE', `/api/units/${id}`)
export const bulkSetUnitsPublic = (unitIds, isPublic) => api('POST', '/api/units/bulk-public', { unitIds, public: isPublic })

// ---- 管理员账号管理（仅 super）----
export const listAdminUsers = () => api('GET', '/api/admin/users')
export const createAdminUser = (payload) => api('POST', '/api/admin/users', payload)
export const updateAdminUser = (id, payload) => api('PUT', `/api/admin/users/${id}`, payload)
export const resetAdminPassword = (id, payload) =>
  api('POST', `/api/admin/users/${id}/reset-password`, payload)

// ---- 系统日志（仅 super）----
export const queryLogs = (params = {}) => {
  const qs = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== null && v !== '') qs.set(k, v)
  return api('GET', `/api/logs?${qs.toString()}`)
}

// ---- 目录数据运维（仅 super）：清空旧数据，供整本重新导入前清场 ----
export const clearDirectory = () => api('POST', '/api/admin/clear-directory')

// ---- 号码查重（super 全库 / unit 仅其绑定单位）----
export const fetchDuplicates = () => api('GET', '/api/duplicates')

// 导出 CSV：走浏览器原生下载（带 Cookie 的同源 GET）。
export function exportCSV() {
  window.location.href = '/api/contacts/export'
}

// 导入 CSV：multipart 上传，返回 {imported, created, updated}。
export async function importCSV(file) {
  const fd = new FormData()
  fd.append('file', file)
  const res = await fetch('/api/contacts/import', {
    method: 'POST',
    credentials: 'include',
    body: fd,
  })
  let data = null
  try {
    data = await res.json()
  } catch (_) {
    /* 非 JSON */
  }
  if (!res.ok) {
    throw new Error(data?.error || `导入失败（HTTP ${res.status}）`)
  }
  return data
}

// ---- 激活码（super/root 全部；unit 仅本单位且不能设单点授权）----
export const listActivationCodes = (params = {}) => {
  const qs = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== null && v !== '') qs.set(k, v)
  return api('GET', `/api/activation-codes?${qs.toString()}`)
}
export const createActivationCodes = (payload) => api('POST', '/api/activation-codes', payload)
export const revokeActivationCode = (id) => api('POST', `/api/activation-codes/${id}/revoke`)
export function exportActivationCodesCSV() {
  window.location.href = '/api/activation-codes/export'
}

// ---- 系统设置（仅 root）----
export const getSettings = () => api('GET', '/api/admin/settings')
export const updateSettings = (payload) => api('PUT', '/api/admin/settings', payload)

// ---- 可见性授权（super/root）：隐藏组/成员对某用户可见 ----
export const getUserVisibility = (id) => api('GET', `/api/admin/users/${id}/visibility`)
export const updateUserVisibility = (id, payload) => api('PUT', `/api/admin/users/${id}/visibility`, payload)

export function fmtTime(iso) {
  if (!iso) return '–'
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString('zh-CN', { hour12: false })
}
