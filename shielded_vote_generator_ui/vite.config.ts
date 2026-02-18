import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    proxy: {
      '/zally': {
        target: 'http://localhost:1318',
        changeOrigin: true,
      },
    },
  },
})
