<script setup>
import { reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { User, Lock, Phone } from '@element-plus/icons-vue'
import { signIn } from '../stores/session'

const route = useRoute()
const router = useRouter()

const formRef = ref()
const loading = ref(false)
const form = reactive({ username: '', password: '' })
const rules = {
  username: [{ required: true, message: '请输入用户名', trigger: 'blur' }],
  password: [{ required: true, message: '请输入密码', trigger: 'blur' }],
}

async function submit() {
  const valid = await formRef.value.validate().catch(() => false)
  if (!valid) return
  loading.value = true
  try {
    await signIn(form.username.trim(), form.password)
    const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : '/'
    router.replace(redirect)
  } catch (e) {
    ElMessage.error(e.message)
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="login-wrap">
    <el-card class="login-card" shadow="always">
      <div class="brand">
        <el-icon :size="30" color="#2563eb"><Phone /></el-icon>
        <h1>通讯录管理</h1>
      </div>
      <p class="sub">请使用管理员账号登录</p>
      <el-form ref="formRef" :model="form" :rules="rules" label-position="top" @submit.prevent="submit">
        <el-form-item label="用户名" prop="username">
          <el-input v-model="form.username" :prefix-icon="User" autocomplete="username" placeholder="用户名" />
        </el-form-item>
        <el-form-item label="密码" prop="password">
          <el-input
            v-model="form.password"
            :prefix-icon="Lock"
            type="password"
            autocomplete="current-password"
            show-password
            placeholder="密码"
            @keyup.enter="submit"
          />
        </el-form-item>
        <el-button type="primary" :loading="loading" class="submit" native-type="submit">登录</el-button>
      </el-form>
      <p class="forgot-tip">忘记密码？请在运行服务器的电脑上执行：<code>phonebook-server.exe recover-admin</code></p>
    </el-card>
  </div>
</template>

<style scoped>
.login-wrap {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  background: #f1f5f9;
  padding: 16px;
}
.login-card { width: 100%; max-width: 380px; border-radius: 14px; }
.brand { display: flex; align-items: center; gap: 10px; margin-bottom: 4px; }
.brand h1 { font-size: 20px; margin: 0; }
.sub { color: #64748b; margin: 0 0 14px; font-size: 13px; }
.submit { width: 100%; }
.forgot-tip { color: #94a3b8; font-size: 12px; margin: 14px 0 0; text-align: center; line-height: 1.6; }
.forgot-tip code { color: #64748b; background: #f1f5f9; padding: 1px 5px; border-radius: 4px; font-size: 11px; }
</style>
