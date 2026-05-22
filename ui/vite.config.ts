import { defineConfig, loadEnv } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')
  const chainUrl = env.VITE_CHAIN_URL || 'http://localhost:1317'
  const precomputedUrl =
    env.VITE_PRECOMPUTED_BASE_URL || 'https://shielded-vote.nyc3.cdn.digitaloceanspaces.com'

  return {
    plugins: [react(), tailwindcss()],
    server: {
      proxy: {
        '/shielded-vote': {
          target: chainUrl,
          changeOrigin: true,
        },
        '/cosmos': {
          target: chainUrl,
          changeOrigin: true,
        },
        '/api': {
          target: chainUrl,
          changeOrigin: true,
        },
        '/nullifier': {
          target: 'http://localhost:3000',
          changeOrigin: true,
          rewrite: (path) => path.replace(/^\/nullifier/, ''),
        },
        '/precomputed-snapshots': {
          target: precomputedUrl,
          changeOrigin: true,
          rewrite: (path) => path.replace(/^\/precomputed-snapshots/, ''),
        },
      },
    },
  }
})
