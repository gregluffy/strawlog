import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'

export default defineConfig({
  plugins: [svelte()],
  build: { outDir: '../internal/hub/dist', emptyOutDir: true, assetsInlineLimit: 0 },
  server: { proxy: { '/api': { target: process.env.STRAWLOG_API || 'http://localhost:8080', changeOrigin: false } } },
})
