import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'

// Build into the Go embed dir (internal/webapi/static) so the server binary
// carries the finished SPA. emptyOutDir is required: the outDir lives
// outside web/ (vite's default is to refuse emptying it).
export default defineConfig({
  plugins: [vue(), tailwindcss()],
  base: '/',
  build: {
    outDir: '../internal/webapi/static',
    emptyOutDir: true,
  },
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:10086',
    },
  },
})
