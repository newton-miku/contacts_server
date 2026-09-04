<script setup>
// 顶层只是路由出口；具体由 AdminLayout（登录后）与 LoginView 渲染。
</script>

<template>
  <router-view />
</template>

<style>
html, body, #app { height: 100%; }
body { margin: 0; font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", "Microsoft YaHei", sans-serif; }
</style>
