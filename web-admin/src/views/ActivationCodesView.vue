<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Refresh, Download, Delete, Printer, Ticket, Upload } from '@element-plus/icons-vue'
import QRCode from 'qrcode'
import { session } from '../stores/session'
import { pb, loadPhonebook } from '../stores/phonebook'
import {
  createActivationCodes,
  exportActivationCodesCSV,
  getSettings,
  listActivationCodes,
  listUnits,
  revokeActivationCode,
  cleanupActivationCodes,
  updateSettings,
  vacuumDatabase,
  importCodesFromContacts,
} from '../api'

const isSuper = computed(() => ['super', 'root'].includes(session.user?.role))
const isRoot = computed(() => session.user?.role === 'root')

const units = ref([])
const codes = ref([])
const loading = ref(false)
const dailyLimit = ref(100)
const todayCreated = ref(0)
const limitInput = ref(100)
const defaultPwInput = ref('')
const retentionInput = ref(30)
const logRetentionInput = ref(90)
const maintenanceTime = ref('03:30')
const autoVacuum = ref(true)
const dbSize = ref(0)
const vacuuming = ref(false)
const cleaning = ref(false)
const savingLimit = ref(false)
const statusFilter = ref('')

const form = reactive({
  phones: '',
  displayName: '',
  unitIds: [],
  contactIds: [],
  expiresInDays: 7,
})
const creating = ref(false)

const importVisible = ref(false)
const importing = ref(false)
const importForm = reactive({ unitIds: [], contactIds: [], grantOwnUnit: true, expiresInDays: 7, initialPassword: '' })
const importResult = ref(null)

const contactOptionsInUnits = computed(() => {
  const set = new Set(importForm.unitIds)
  return pb.contacts
    .filter((c) => importForm.unitIds.length === 0 || set.has(c.unitId))
    .map((c) => ({ id: c.id, label: `${c.displayName}（${c.unit || '未分组'}）` }))
})

function openImport() {
  importForm.unitIds = []
  importForm.contactIds = []
  importForm.grantOwnUnit = true
  importForm.expiresInDays = 7
  importForm.initialPassword = ''
  importResult.value = null
  importVisible.value = true
}

async function onImport() {
  if (!importForm.unitIds.length && !importForm.contactIds.length) {
    ElMessage.warning('请至少选择一个单位或若干联系人')
    return
  }
  importing.value = true
  try {
    const res = await importCodesFromContacts({
      unitIds: importForm.unitIds,
      contactIds: importForm.contactIds,
      grantOwnUnit: importForm.grantOwnUnit,
      expiresInDays: Number(importForm.expiresInDays) || 7,
      initialPassword: importForm.initialPassword.trim(),
    })
    importResult.value = res
    ElMessage.success(
      `生成激活码 ${(res.codes || []).length} 个、账号 ${(res.accounts || []).length} 个、跳过 ${(res.skipped || []).length} 条`,
    )
    await reload()
  } catch (e) {
    ElMessage.error(e.message)
  } finally {
    importing.value = false
  }
}

const importRows = computed(() => {
  const r = importResult.value
  if (!r) return []
  const unitNames = (ids) => (ids || []).map((id) => units.value.find((u) => u.id === id)?.name || id).join(' / ')
  return [
    ...(r.codes || []).map((c) => ({ kind: '激活码', key: c.code, name: c.displayName, extra: unitNames(c.unitIds) })),
    ...(r.accounts || []).map((a) => ({ kind: '账号', key: a.username, name: a.name, extra: `密码 ${a.password} · ${unitNames(a.unitIds)}` })),
    ...(r.skipped || []).map((s) => ({ kind: '跳过', key: s.phone || '', name: s.name || '', extra: s.reason || '' })),
  ]
})

function exportImportResult() {
  const rows = [['类型', '码 / 账号', '姓名', '密码 / 可见单位 / 原因']]
  for (const r of importRows.value) rows.push([r.kind, r.key, r.name || '', r.extra || ''])
  const LF = String.fromCharCode(10)
  const BOM = String.fromCharCode(0xFEFF)
  const quote = (v) => '"' + String(v == null ? '' : v).replace(/"/g, '""') + '"'
  const csv = BOM + rows.map((r) => r.map(quote).join(',')).join(LF)
  const url = URL.createObjectURL(new Blob([csv], { type: 'text/csv;charset=utf-8' }))
  const a = document.createElement('a')
  a.href = url
  a.download = 'import_result.csv'
  a.click()
  URL.revokeObjectURL(url)
}

const qrVisible = ref(false)
const qrDataUrl = ref('')
const qrCodeText = ref('')

const statusText = (s) => ({ pending: '未使用', used: '已使用', expired: '已过期', revoked: '已作废' })[s] || s
const statusTag = (s) => ({ pending: 'success', used: 'info', expired: 'warning', revoked: 'danger' })[s] || 'info'

const filteredCodes = computed(() =>
  statusFilter.value ? codes.value.filter((c) => c.status === statusFilter.value) : codes.value,
)
const contactOptions = computed(() =>
  pb.contacts.map((c) => ({ id: c.id, label: `${c.displayName}${c.unit ? '（' + c.unit + '）' : ''}` })),
)

onMounted(async () => {
  await loadPhonebook()
  try {
    units.value = await listUnits()
  } catch (e) {
    ElMessage.error(`加载单位失败：${e.message}`)
  }
  await reload()
  if (isRoot.value) await loadSettings()
})

async function loadSettings() {
  try {
    const s = await getSettings()
    dailyLimit.value = s.activationDailyLimit
    todayCreated.value = s.activationToday
    limitInput.value = s.activationDailyLimit
    defaultPwInput.value = s.defaultAccountPassword || ''
    retentionInput.value = s.codeRetentionDays ?? 30
    logRetentionInput.value = s.logRetentionDays ?? 90
    maintenanceTime.value = s.maintenanceTime || '03:30'
    autoVacuum.value = s.autoVacuum !== false
    dbSize.value = s.dbSizeBytes || 0
  } catch (_) {}
}

async function reload() {
  loading.value = true
  try {
    const res = await listActivationCodes({ limit: 200 })
    codes.value = res.codes || []
    dailyLimit.value = res.dailyLimit ?? dailyLimit.value
    todayCreated.value = res.todayCreated ?? todayCreated.value
  } catch (e) {
    ElMessage.error(`读取激活码失败：${e.message}`)
  } finally {
    loading.value = false
  }
}

async function onCreate() {
  const phones = String(form.phones)
    .split(/[\s,，;；]+/)
    .map((s) => s.trim())
    .filter(Boolean)
  if (!phones.length) {
    ElMessage.warning('请填写至少一个手机号（每行一个）')
    return
  }
  creating.value = true
  try {
    const res = await createActivationCodes({
      items: phones.map((p) => ({
        phone: p,
        displayName: phones.length === 1 ? form.displayName.trim() : '',
        unitIds: form.unitIds,
        contactIds: isSuper.value ? form.contactIds : [],
      })),
      expiresInDays: Number(form.expiresInDays) || 7,
    })
    ElMessage.success(`已生成 ${res.codes.length} 个激活码（今日 ${res.todayCreated}/${res.dailyLimit}）`)
    // 姓名留空时由服务端补全（通讯录 → 账号管理），这里把补到的名字回显出来便于核对。
    const typedName = phones.length === 1 ? form.displayName.trim() : ''
    const autoNamed = (res.codes || []).filter((c) => c.displayName && c.displayName !== typedName)
    if (autoNamed.length) {
      const preview = autoNamed
        .slice(0, 5)
        .map((c) => `${c.phone} ${c.displayName}`)
        .join('、')
      ElMessage.info(
        `已自动补全 ${autoNamed.length} 个姓名：${preview}${autoNamed.length > 5 ? ' …' : ''}`,
      )
    }
    todayCreated.value = res.todayCreated
    form.phones = ''
    form.displayName = ''
    await reload()
  } catch (e) {
    ElMessage.error(e.message)
  } finally {
    creating.value = false
  }
}

// 立即清理失效（已用/已作废/已过期）且超过保留期的激活码。
async function onCleanup() {
  try {
    await ElMessageBox.confirm(
      `将删除「已使用 / 已作废 / 已过期」且创建时间超过 ${retentionInput.value} 天的激活码。
（不影响每日额度统计；删除后无法再查看这些码的历史记录）`,
      '清理失效激活码',
      { type: 'warning', confirmButtonText: '清理', cancelButtonText: '取消' },
    )
  } catch (_) {
    return
  }
  cleaning.value = true
  try {
    const res = await cleanupActivationCodes({ retentionDays: Number(retentionInput.value) })
    ElMessage.success(`已清理 ${res.deleted} 条失效激活码`)
    await reload()
  } catch (e) {
    ElMessage.error(e.message)
  } finally {
    cleaning.value = false
  }
}

async function onRevoke(row) {
  try {
    await ElMessageBox.confirm(`确定作废激活码 ${row.code} 吗？作废后无法再激活。`, '作废激活码', {
      type: 'warning',
      confirmButtonText: '作废',
      cancelButtonText: '取消',
    })
  } catch (_) {
    return
  }
  try {
    await revokeActivationCode(row.id)
    ElMessage.success('已作废')
    await reload()
  } catch (e) {
    ElMessage.error(e.message)
  }
}

async function showQR(row) {
  qrCodeText.value = row.code
  qrDataUrl.value = await QRCode.toDataURL(row.code, { width: 320, margin: 1 })
  qrVisible.value = true
}

async function printPending() {
  const pending = codes.value.filter((c) => c.status === 'pending')
  if (!pending.length) {
    ElMessage.info('没有可打印的未使用激活码')
    return
  }
  const cards = []
  for (const c of pending) {
    const url = await QRCode.toDataURL(c.code, { width: 260, margin: 1 })
    cards.push(
      `<div class="card"><img src="${url}"/><div class="code">${c.code}</div>` +
        `<div class="meta">${c.phone}${c.displayName ? ' · ' + c.displayName : ''}</div></div>`,
    )
  }
  const w = window.open('', '_blank')
  if (!w) {
    ElMessage.warning('浏览器拦截了弹窗，请允许后重试')
    return
  }
  w.document.write(
    `<html><head><title>激活码打印</title><style>` +
      `body{font-family:system-ui,sans-serif;margin:16px}` +
      `.card{display:inline-block;border:1px solid #ddd;border-radius:12px;padding:12px;margin:8px;text-align:center;width:300px;page-break-inside:avoid}` +
      `.code{font-size:20px;font-weight:700;letter-spacing:2px;margin-top:8px}` +
      `.meta{color:#666;font-size:12px;margin-top:4px}` +
      `</style></head><body>${cards.join('')}</body></html>`,
  )
  w.document.close()
  w.focus()
  setTimeout(() => w.print(), 400)
}

const fmtBytes = (n) => {
  if (!n) return '0 B'
  const u = ['B', 'KB', 'MB', 'GB']
  let i = 0
  let v = n
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${u[i]}`
}

// 立即整理数据库：把删除行释放的页归还文件（会短暂锁库）。
async function onVacuum() {
  try {
    await ElMessageBox.confirm(
      `将整理数据库（当前 ${fmtBytes(dbSize.value)}）。整理会短暂锁库、需要约 2 倍临时磁盘空间，建议在低谷期执行。继续吗？`,
      '立即整理数据库',
      { type: 'warning', confirmButtonText: '整理', cancelButtonText: '取消' },
    )
  } catch (_) {
    return
  }
  vacuuming.value = true
  try {
    const res = await vacuumDatabase()
    ElMessage.success(`整理完成：${fmtBytes(res.beforeBytes)} → ${fmtBytes(res.afterBytes)}`)
    await loadSettings()
  } catch (e) {
    ElMessage.error(e.message)
  } finally {
    vacuuming.value = false
  }
}

async function saveSettings() {
  savingLimit.value = true
  try {
    const res = await updateSettings({
      activationDailyLimit: Number(limitInput.value),
      defaultAccountPassword: defaultPwInput.value.trim(),
      codeRetentionDays: Number(retentionInput.value),
      logRetentionDays: Number(logRetentionInput.value),
      maintenanceTime: maintenanceTime.value,
      autoVacuum: autoVacuum.value,
    })
    dailyLimit.value = res.activationDailyLimit
    ElMessage.success('设置已保存')
  } catch (e) {
    ElMessage.error(e.message)
  } finally {
    savingLimit.value = false
  }
}
</script>

<template>
  <div>
    <div class="toolbar">
      <div class="summary">
        今日已生成 <b>{{ todayCreated }}</b> / 上限 <b>{{ dailyLimit }}</b>
      </div>
      <el-select v-model="statusFilter" placeholder="全部状态" clearable style="width: 130px">
        <el-option label="未使用" value="pending" />
        <el-option label="已使用" value="used" />
        <el-option label="已过期" value="expired" />
        <el-option label="已作废" value="revoked" />
      </el-select>
      <el-button :icon="Refresh" @click="reload">刷新</el-button>
      <el-button :icon="Upload" @click="openImport">从通讯录导入</el-button>
      <el-button :icon="Printer" @click="printPending">打印未使用二维码</el-button>
      <el-button v-if="isSuper" :icon="Delete" :loading="cleaning" @click="onCleanup">清理已失效</el-button>
      <el-button :icon="Download" @click="exportActivationCodesCSV">导出 CSV</el-button>
    </div>

    <el-card shadow="never" class="mb">
      <template #header>
        <span class="card-title"><el-icon><Ticket /></el-icon> 生成激活码</span>
      </template>
      <el-form label-width="96px" @submit.prevent>
        <el-form-item label="手机号">
          <el-input
            v-model="form.phones"
            type="textarea"
            :rows="3"
            placeholder="一行一个手机号；多行则批量生成，每个号码一个激活码"
          />
        </el-form-item>
        <el-form-item label="姓名">
          <el-input v-model="form.displayName" placeholder="留空则自动按号码补全" style="max-width: 280px" />
          <div class="hint block">
            仅单个手机号时生效。留空时服务端按号码到通讯录里找姓名；通讯录没有，则用「账号管理」中同号码账号的姓名。
          </div>
        </el-form-item>
        <el-form-item label="可见单位">
          <div style="width: 100%">
            <el-select v-model="form.unitIds" multiple filterable placeholder="仅需勾选要额外授权的隐藏单位（公开单位所有人默认可见）" style="width: 100%">
              <el-option
                v-for="u in units"
                :key="u.id"
                :label="u.isPublic ? `${u.name}（公开，默认可见）` : `${u.name}（隐藏）`"
                :value="u.id"
              />
            </el-select>
            <div class="hint block">
              公开单位对所有激活用户默认可见；这里留空即「只能看公开单位」。勾选隐藏单位后，该用户才额外看得到它们。
            </div>
          </div>
        </el-form-item>
        <el-form-item v-if="isSuper" label="单点可见">
          <el-select
            v-model="form.contactIds"
            multiple
            filterable
            placeholder="额外单独授权可见的联系人（隐藏组内的个别人）"
            style="width: 100%"
          >
            <el-option v-for="c in contactOptions" :key="c.id" :label="c.label" :value="c.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="有效期">
          <el-input-number v-model="form.expiresInDays" :min="1" :max="30" />
          <span class="hint">天（默认 7 天，一次性使用）</span>
        </el-form-item>
        <el-form-item>
          <el-button type="primary" :loading="creating" @click="onCreate">生成</el-button>
        </el-form-item>
      </el-form>
    </el-card>

    <el-card v-if="isRoot" shadow="never" class="mb">
      <template #header><span class="card-title">系统设置（仅 root）</span></template>
      <div class="limit-row">
        <span>每日激活码上限</span>
        <el-input-number v-model="limitInput" :min="1" :max="10000" />
        <span class="hint">今日已用 {{ todayCreated }} 个（含座机账号）</span>
      </div>
      <div class="limit-row" style="margin-top: 10px">
        <span>失效码保留天数</span>
        <el-input-number v-model="retentionInput" :min="0" :max="3650" />
        <span class="hint">0 = 不自动清理；每天自动清理一次，也可用上方「清理已失效」手动执行</span>
      </div>
      <div class="limit-row" style="margin-top: 10px">
        <span>系统日志保留天数</span>
        <el-input-number v-model="logRetentionInput" :min="0" :max="3650" />
        <span class="hint">0 = 不自动清理；每天自动清理一次，也可在「系统日志」页手动清理</span>
      </div>
      <div class="limit-row" style="margin-top: 10px">
        <span>每日维护时间</span>
        <el-input v-model="maintenanceTime" placeholder="03:30" style="width: 100px" />
        <el-switch v-model="autoVacuum" active-text="清理后自动整理数据库" />
        <span class="hint">低谷期自动执行清理（激活码/日志），有删除时再整理数据库</span>
      </div>
      <div class="limit-row" style="margin-top: 10px">
        <span>数据库大小</span>
        <span class="hint" style="margin-left: 0">{{ fmtBytes(dbSize) }}</span>
        <el-button size="small" :loading="vacuuming" @click="onVacuum">立即整理数据库</el-button>
      </div>
      <div class="limit-row" style="margin-top: 10px">
        <span>单位账号默认密码</span>
        <el-input v-model="defaultPwInput" placeholder="至少 6 位" style="width: 220px" />
        <el-button type="primary" plain :loading="savingLimit" @click="saveSettings">保存</el-button>
        <span class="hint">从通讯录导入创建的办公室/值班室账号用它作初始密码，首次登录强制修改</span>
      </div>
    </el-card>

    <el-table :data="filteredCodes" v-loading="loading" border stripe class="table">
      <el-table-column prop="code" label="激活码" width="150">
        <template #default="{ row }">
          <span class="code">{{ row.code }}</span>
        </template>
      </el-table-column>
      <el-table-column prop="phone" label="手机号" width="130" />
      <el-table-column prop="displayName" label="姓名" width="110">
        <template #default="{ row }">{{ row.displayName || '—' }}</template>
      </el-table-column>
      <el-table-column label="可见范围" min-width="200">
        <template #default="{ row }">
          <span>{{ (row.unitIds || []).map((id) => units.find((u) => u.id === id)?.name || id).join('、') || '（仅公开单位）' }}</span>
          <span v-if="(row.contactIds || []).length" class="hint"> + {{ row.contactIds.length }} 个单点</span>
        </template>
      </el-table-column>
      <el-table-column label="状态" width="100" align="center">
        <template #default="{ row }">
          <el-tag :type="statusTag(row.status)" effect="light" size="small">{{ statusText(row.status) }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="有效期至" min-width="170">
        <template #default="{ row }">{{ row.expiresAt?.replace('T', ' ').slice(0, 16) }}</template>
      </el-table-column>
      <el-table-column label="操作" width="150" align="right">
        <template #default="{ row }">
          <el-button link type="primary" @click="showQR(row)">二维码</el-button>
          <el-button v-if="row.status === 'pending'" link type="danger" @click="onRevoke(row)">作废</el-button>
        </template>
      </el-table-column>
      <template #empty>
        <el-empty description="暂无激活码" :image-size="80" />
      </template>
    </el-table>

    <el-dialog v-model="importVisible" title="从通讯录导入（批量发码）" width="640px" :close-on-click-modal="false">
      <el-form label-width="110px" @submit.prevent>
        <el-form-item label="按单位导入">
          <el-select v-model="importForm.unitIds" multiple filterable clearable placeholder="选单位 = 该单位全部联系人" style="width: 100%">
            <el-option
              v-for="u in units"
              :key="u.id"
              :label="u.isPublic ? `${u.name}（公开）` : `${u.name}（隐藏）`"
              :value="u.id"
            />
          </el-select>
        </el-form-item>
        <el-form-item label="再选联系人">
          <el-select v-model="importForm.contactIds" multiple filterable clearable placeholder="可另选若干联系人（可搜索）" style="width: 100%">
            <el-option v-for="c in contactOptionsInUnits" :key="c.id" :label="c.label" :value="c.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="自动授予本单位">
          <el-switch v-model="importForm.grantOwnUnit" />
          <span class="hint">每个账号可见「所在单位」+ 公开单位</span>
        </el-form-item>
        <el-form-item label="激活码有效期">
          <el-input-number v-model="importForm.expiresInDays" :min="1" :max="30" />
          <span class="hint">天</span>
        </el-form-item>
        <el-form-item label="座机账号密码">
          <el-input v-model="importForm.initialPassword" placeholder="留空 = 用系统默认密码（见下方「系统设置」）" style="max-width: 340px" />
        </el-form-item>
      </el-form>
      <el-alert
        type="info"
        :closable="false"
        show-icon
        title="手机号联系人 → 一次性激活码（App 输码激活）；只有座机且名称含「办公室/值班室」的联系人 → 桌面端账号密码账号（初始密码用系统默认，首登强制改）。两者都占用每日额度。"
      />
      <div v-if="importResult" class="import-result">
        <el-divider />
        <div class="summary">
          生成激活码 {{ (importResult.codes || []).length }} 个 · 账号 {{ (importResult.accounts || []).length }} 个 ·
          跳过 {{ (importResult.skipped || []).length }} 条
        </div>
        <el-table :data="importRows" size="small" border max-height="260">
          <el-table-column prop="kind" label="类型" width="80" />
          <el-table-column prop="key" label="码 / 账号" width="150" />
          <el-table-column prop="name" label="姓名" width="110" />
          <el-table-column prop="extra" label="密码 / 可见单位 / 跳过原因" min-width="240" />
        </el-table>
        <el-button size="small" :icon="Download" style="margin-top: 8px" @click="exportImportResult">导出结果 CSV</el-button>
      </div>
      <template #footer>
        <el-button @click="importVisible = false">关闭</el-button>
        <el-button type="primary" :loading="importing" @click="onImport">生成</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="qrVisible" title="激活码二维码" width="380" align-center>
      <div class="qr-box">
        <img v-if="qrDataUrl" :src="qrDataUrl" alt="QR" class="qr-img" />
        <div class="code big">{{ qrCodeText }}</div>
        <div class="hint">用户打开 App 输入此码（不区分大小写）完成激活</div>
      </div>
    </el-dialog>
  </div>
</template>

<style scoped>
.toolbar { display: flex; gap: 10px; flex-wrap: wrap; align-items: center; margin-bottom: 10px; }
.toolbar .summary { color: #475569; font-size: 13px; margin-right: auto; }
.mb { margin-bottom: 12px; }
.card-title { display: inline-flex; align-items: center; gap: 6px; font-weight: 600; }
.hint { color: #94a3b8; font-size: 12px; margin-left: 8px; }
.hint.block { display: block; margin: 6px 0 0; }
.import-result { margin-top: 12px; }
.import-result .summary { font-size: 13px; color: #475569; margin-bottom: 8px; }
.limit-row { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
.code { font-family: monospace; font-weight: 700; letter-spacing: 1px; }
.code.big { font-size: 22px; margin-top: 10px; }
.qr-box { text-align: center; }
.qr-img { width: 260px; height: 260px; }
.table { border-radius: 8px; overflow: hidden; }
</style>
