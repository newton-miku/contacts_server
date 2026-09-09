<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Refresh, Download, Delete, Printer, Ticket } from '@element-plus/icons-vue'
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
  updateSettings,
} from '../api'

const isSuper = computed(() => ['super', 'root'].includes(session.user?.role))
const isRoot = computed(() => session.user?.role === 'root')

const units = ref([])
const codes = ref([])
const loading = ref(false)
const dailyLimit = ref(100)
const todayCreated = ref(0)
const limitInput = ref(100)
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
    todayCreated.value = res.todayCreated
    form.phones = ''
    await reload()
  } catch (e) {
    ElMessage.error(e.message)
  } finally {
    creating.value = false
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

async function saveLimit() {
  savingLimit.value = true
  try {
    const res = await updateSettings({ activationDailyLimit: Number(limitInput.value) })
    dailyLimit.value = res.activationDailyLimit
    ElMessage.success(`每日上限已设为 ${res.activationDailyLimit}`)
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
      <el-button :icon="Printer" @click="printPending">打印未使用二维码</el-button>
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
          <el-input v-model="form.displayName" placeholder="仅单个手机号时填写（便于管理员核对）" style="max-width: 280px" />
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
        <el-button type="primary" plain :loading="savingLimit" @click="saveLimit">保存</el-button>
        <span class="hint">今日已生成 {{ todayCreated }} 个</span>
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
.limit-row { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
.code { font-family: monospace; font-weight: 700; letter-spacing: 1px; }
.code.big { font-size: 22px; margin-top: 10px; }
.qr-box { text-align: center; }
.qr-img { width: 260px; height: 260px; }
.table { border-radius: 8px; overflow: hidden; }
</style>
