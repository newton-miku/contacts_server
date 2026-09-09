<script setup>
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { session } from '../stores/session'
import { pb, loadPhonebook } from '../stores/phonebook'
import { getUserVisibility, listAdminUsers, listUnits, updateUserVisibility } from '../api'

const users = ref([])
const units = ref([])
const selectedId = ref(null)
const unitIds = ref([])
const contactIds = ref([])
const loading = ref(false)
const saving = ref(false)

const contactOptions = computed(() =>
  pb.contacts.map((c) => ({ id: c.id, label: `${c.displayName}${c.unit ? '（' + c.unit + '）' : ''}` })),
)
const selectedUser = computed(() => users.value.find((u) => u.id === selectedId.value))
const selectableUsers = computed(() => users.value.filter((u) => u.role !== 'super' && u.role !== 'root'))

onMounted(async () => {
  await loadPhonebook()
  try {
    units.value = await listUnits()
    users.value = await listAdminUsers()
  } catch (e) {
    ElMessage.error(`加载失败：${e.message}`)
  }
})

async function onSelectUser(id) {
  unitIds.value = []
  contactIds.value = []
  if (!id) return
  loading.value = true
  try {
    const res = await getUserVisibility(id)
    unitIds.value = res.unitIds || []
    contactIds.value = (res.contacts || []).map((c) => c.id)
  } catch (e) {
    ElMessage.error(`读取可见范围失败：${e.message}`)
  } finally {
    loading.value = false
  }
}

async function onSave() {
  if (!selectedId.value) {
    ElMessage.warning('请先选择用户')
    return
  }
  saving.value = true
  try {
    await updateUserVisibility(selectedId.value, { unitIds: unitIds.value, contactIds: contactIds.value })
    ElMessage.success('已保存可见范围')
  } catch (e) {
    ElMessage.error(e.message)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div>
    <el-card shadow="never" class="mb">
      <template #header><span class="card-title">可见性配置</span></template>
      <p class="hint">
        公开单位对所有人可见；这里的「可见单位」用于把<b>隐藏单位（非公开）</b>单独授权给某个人，
        「单点可见」可再单独放开隐藏单位里的个别人。
      </p>
      <el-form label-width="90px" @submit.prevent>
        <el-form-item label="用户">
          <el-select
            v-model="selectedId"
            filterable
            clearable
            placeholder="选择要配置的用户（只读用户 / 单位管理员）"
            style="width: 340px"
            @change="onSelectUser"
          >
            <el-option
              v-for="u in selectableUsers"
              :key="u.id"
              :label="`${u.username}${u.displayName ? ' · ' + u.displayName : ''}（${u.role === 'unit' ? '单位管理员' : '只读用户'}）`"
              :value="u.id"
            />
          </el-select>
        </el-form-item>
        <el-form-item label="可见单位">
          <el-select v-model="unitIds" multiple filterable placeholder="额外可见的隐藏单位" style="width: 100%">
            <el-option
              v-for="u in units"
              :key="u.id"
              :label="u.isPublic ? `${u.name}（公开）` : `${u.name}（隐藏）`"
              :value="u.id"
            />
          </el-select>
        </el-form-item>
        <el-form-item label="单点可见">
          <el-select
            v-model="contactIds"
            multiple
            filterable
            placeholder="单独授权可见的联系人（可来自隐藏单位）"
            style="width: 100%"
          >
            <el-option v-for="c in contactOptions" :key="c.id" :label="c.label" :value="c.id" />
          </el-select>
        </el-form-item>
        <el-form-item>
          <el-button type="primary" :loading="saving" :disabled="!selectedId" @click="onSave">保存</el-button>
          <span v-if="selectedUser" class="hint">当前用户：{{ selectedUser.username }}</span>
        </el-form-item>
      </el-form>
    </el-card>
  </div>
</template>

<style scoped>
.mb { margin-bottom: 12px; }
.card-title { font-weight: 600; }
.hint { color: #64748b; font-size: 13px; line-height: 1.7; }
</style>
