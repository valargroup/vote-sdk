import { defineConfig, loadEnv } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')
  const chainUrl = env.VITE_CHAIN_URL || 'http://localhost:1318'

  return {
    plugins: [react(), tailwindcss()],
    server: {
      proxy: {
        '/zally': {
          target: chainUrl,
          changeOrigin: true,
        },
      },
    },
  }
})
