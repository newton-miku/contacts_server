<script setup>
import { reactive, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { createContact, updateContact } from '../api'

const props = defineProps({
  modelValue: { type: Boolean, default: false },
  contact: { type: Object, default: null }, // null → 新增
  // 可选单位（[{id, name}]）：super=全部，unit=服务端已按角色返回其绑定单位。
  units: { type: Array, default: () => [] },
})
const emit = defineEmits(['update:modelValue', 'saved', 'unitChanged'])

const formRef = ref()
const saving = ref(false)
const form = reactive({
  displayName: '',
  phoneNumber: '',
  mobile2: '',
  landline: '',
  fax: '',
  unitId: undefined,
  jobTitle: '',
  email: '',
})

watch(
  () => props.modelValue,
  (open) => {
    if (!open) return
    const c = props.contact || {}
    Object.assign(form, {
      displayName: c.displayName ?? '',
      phoneNumber: c.phoneNumber ?? '',
      mobile2: c.mobile2 ?? '',
      landline: c.landline ?? '',
      fax: c.fax ?? '',
      unitId: c.unitId ?? undefined,
      jobTitle: c.jobTitle ?? '',
      email: c.email ?? '',
    })
    formRef.value?.clearValidate()
  },
)

const rules = {
  displayName: [{ required: true, message: '请输入姓名', trigger: 'blur' }],
  // 手机号=主叫号（手机优先，无手机则填座机）；短固话保存时会自动补 0915 区号。
  phoneNumber: [
    { required: true, message: '请输入手机号或座机', trigger: 'blur' },
    { pattern: /^[0-9+\-\s()]{3,20}$/, message: '号码格式不正确', trigger: 'blur' },
  ],
  mobile2: [{ pattern: /^[0-9+\-\s()]{3,20}$/, message: '手机2格式不正确', trigger: 'blur' }],
  landline: [{ pattern: /^[0-9+\-\s()]{3,20}$/, message: '座机格式不正确', trigger: 'blur' }],
  fax: [{ pattern: /^[0-9+\-\s()]{3,20}$/, message: '传真格式不正确', trigger: 'blur' }],
  unitId: [{ required: true, message: '请选择单位', trigger: 'change' }],
  email: [{ type: 'email', message: '邮箱格式不正确', trigger: 'blur' }],
}

function close() {
  emit('update:modelValue', false)
}

const emptyToNull = (v) => {
  const s = String(v ?? '').trim()
  return s === '' ? null : s
}

async function submit() {
  const valid = await formRef.value.validate().catch(() => false)
  if (!valid) return
  saving.value = true
  try {
    const payload = {
      displayName: form.displayName.trim(),
      phoneNumber: form.phoneNumber.trim(),
      mobile2: emptyToNull(form.mobile2),
      landline: emptyToNull(form.landline),
      fax: emptyToNull(form.fax),
      unitId: form.unitId,
      jobTitle: emptyToNull(form.jobTitle),
      email: emptyToNull(form.email),
    }
    if (props.contact) {
      const oldUnitId = props.contact.unitId
      await updateContact(props.contact.id, payload)
      ElMessage.success('已更新联系人')
      // 单位变更时通知外层：可能需要同步关联账号的可见单位
      if (oldUnitId !== payload.unitId) {
        emit('unitChanged', { contactId: props.contact.id, oldUnitId, newUnitId: payload.unitId })
      }
    } else {
      await createContact(payload)
      ElMessage.success('已新增联系人')
    }
    emit('saved')
    close()
  } catch (e) {
    ElMessage.error(e.message)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    :title="contact ? '编辑联系人' : '新增联系人'"
    width="520px"
    :close-on-click-modal="false"
    @update:model-value="emit('update:modelValue', $event)"
    @closed="formRef?.clearValidate()"
  >
    <el-form ref="formRef" :model="form" :rules="rules" label-width="70px">
      <el-row :gutter="12">
        <el-col :span="12">
          <el-form-item label="姓名" prop="displayName">
            <el-input v-model="form.displayName" maxlength="50" placeholder="姓名" />
          </el-form-item>
        </el-col>
        <el-col :span="12">
          <el-form-item label="手机号" prop="phoneNumber">
            <el-input v-model="form.phoneNumber" maxlength="20" placeholder="13800138000" />
          </el-form-item>
        </el-col>
      </el-row>
      <el-row :gutter="12">
        <el-col :span="12">
          <el-form-item label="手机2" prop="mobile2">
            <el-input v-model="form.mobile2" maxlength="20" placeholder="第二个手机号（选填）" />
          </el-form-item>
        </el-col>
        <el-col :span="12">
          <el-form-item label="座机" prop="landline">
            <el-input v-model="form.landline" maxlength="20" placeholder="座机（选填，短号自动补0915）" />
          </el-form-item>
        </el-col>
      </el-row>
      <el-row :gutter="12">
        <el-col :span="12">
          <el-form-item label="传真" prop="fax">
            <el-input v-model="form.fax" maxlength="20" placeholder="传真（选填）" />
          </el-form-item>
        </el-col>
        <el-col :span="12">
          <el-form-item label="职务" prop="jobTitle">
            <el-input v-model="form.jobTitle" maxlength="50" placeholder="职务（选填）" />
          </el-form-item>
        </el-col>
      </el-row>
      <el-form-item label="单位" prop="unitId">
        <el-select
          v-model="form.unitId"
          placeholder="请选择所属单位（可输入名称搜索）"
          filterable
          default-first-option
          no-data-text="没有匹配的单位，可在「单位管理」新建"
          style="width: 100%"
        >
          <el-option v-for="u in units" :key="u.id" :label="u.name" :value="u.id" />
        </el-select>
      </el-form-item>
      <el-form-item label="邮箱" prop="email">
        <el-input v-model="form.email" maxlength="100" placeholder="邮箱（选填）" />
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="close">取消</el-button>
      <el-button type="primary" :loading="saving" @click="submit">保存</el-button>
    </template>
  </el-dialog>
</template>
