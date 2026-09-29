import { defineConfig, loadEnv } from 'vite'
import vue from '@vitejs/plugin-vue'

// createViteConfig 读取 Vite 环境变量；本地隔离验收可切换 API，生产仍由同源 Nginx 代理。
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, '.', '')
  const apiTarget = env.VITE_API_TARGET || 'http://127.0.0.1:8080'
  return {
    plugins: [vue()],
    server: {
      host: '127.0.0.1',
      strictPort: true,
      proxy: {
        '/api': apiTarget,
        '/healthz': apiTarget,
      },
    },
  }
})
