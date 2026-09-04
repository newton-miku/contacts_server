<script setup>
import { computed, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { Lock } from '@element-plus/icons-vue'
import { changePassword, me } from '../api'
import { session, updateUser } from '../stores/session'

const router = useRouter()

// 是否处于「首次登录强制改密」状态（由后端 mustChangePassword 标记驱动）。
const forced = computed(() => session.user?.mustChangePassword === true)

const formRef = ref()
const saving = ref(false)
const form = reactive({ oldPassword: '', newPassword: '', confirm: '' })

const rules = {
  oldPassword: [{ required: true, message: '请输入当前密码', trigger: 'blur' }],
  newPassword: [
    { required: true, message: '请输入新密码', trigger: 'blur' },
    { min: 6, message: '新密码至少 6 位', trigger: 'blur' },
    {
      validator: (_, v, cb) =>
        v !== form.oldPassword ? cb() : cb(new Error('新密码不能与当前密码相同')),
      trigger: 'blur',
    },
  ],
  confirm: [
    { required: true, message: '请再次输入新密码', trigger: 'blur' },
    {
      validator: (_, v, cb) =>
        v === form.newPassword ? cb() : cb(new Error('两次输入的新密码不一致')),
      trigger: 'blur',
    },
  ],
}

async function submit() {
  const valid = await formRef.value.validate().catch(() => false)
  if (!valid) return
  saving.value = true
  try {
    const wasForced = forced.value
    await changePassword(form.oldPassword, form.newPassword)
    // 后端在改密成功时清除了 mustChangePassword 标记；拉最新用户信息同步本地态。
    updateUser(await me())
    ElMessage.success('密码已修改，其它设备上的登录会自动失效')
    formRef.value.resetFields()
    if (wasForced) {
      router.replace('/dashboard')
    }
  } catch (e) {
    ElMessage.error(e.message)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <el-card shadow="never" class="panel">
    <template #header>修改密码</template>
    <el-alert
      v-if="forced"
      type="warning"
      :closable="false"
      show-icon
      title="首次登录，请先修改初始密码"
      description="当前账号仍在使用初始密码，修改密码后才能使用其它功能。"
      class="forced-alert"
    />
    <p v-else class="hint">修改后除当前登录外的其它会话将自动失效，需要重新登录。</p>
    <el-form
      ref="formRef"
      :model="form"
      :rules="rules"
      label-width="110px"
      style="max-width: 460px"
    >
      <el-form-item label="当前密码" prop="oldPassword">
        <el-input v-model="form.oldPassword" type="password" show-password :prefix-icon="Lock" autocomplete="current-password" />
      </el-form-item>
      <el-form-item label="新密码" prop="newPassword">
        <el-input v-model="form.newPassword" type="password" show-password autocomplete="new-password" />
      </el-form-item>
      <el-form-item label="确认新密码" prop="confirm">
        <el-input v-model="form.confirm" type="password" show-password autocomplete="new-password" />
      </el-form-item>
      <el-form-item>
        <el-button type="primary" :loading="saving" @click="submit">保存</el-button>
      </el-form-item>
    </el-form>
  </el-card>
</template>

<style scoped>
.panel { max-width: 640px; }
.hint { color: #64748b; font-size: 13px; margin: 0 0 12px; }
.forced-alert { margin-bottom: 14px; }
</style>
