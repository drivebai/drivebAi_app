import { defineConfig, loadEnv } from 'vite'
import vue from '@vitejs/plugin-vue'
import path from 'node:path'

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')
  const apiBase = env.VITE_API_BASE_URL || 'http://localhost:8080'
  return {
    plugins: [vue()],
    resolve: {
      alias: { '@': path.resolve(__dirname, 'src') },
    },
    server: {
      port: 5173,
      // Proxy /api and /uploads to the Go backend so we don't touch CORS in dev.
      proxy: {
        '/api': { target: apiBase, changeOrigin: true },
        '/uploads': { target: apiBase, changeOrigin: true },
      },
    },
  }
})
