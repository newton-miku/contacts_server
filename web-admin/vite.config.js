import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath, URL } from 'node:url'

// 构建产物输出到 ../web，由 Go 端 go:embed 打包进可执行文件。
// dev 时将 /api 代理到本地 Go 服务，实现热更新。
export default defineConfig({
  plugins: [vue()],
  // 版权年份用「构建时」的年份，而不是运行时（打包一次固定）
  define: {
    __BUILD_YEAR__: JSON.stringify(String(new Date().getFullYear())),
  },
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  base: './',
  build: {
    outDir: '../web',
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      },
    },
  },
})
