import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The SPA is embedded in the Go binary and served under /app, so assets must
// resolve relative to that base. In dev (`npm run dev`) proxy API + the export
// download route to the running Go server.
export default defineConfig({
  base: '/app/',
  plugins: [react()],
  build: { outDir: 'dist', emptyOutDir: true },
  server: {
    proxy: {
      '/api': 'http://localhost:8080',
      '/c': 'http://localhost:8080',
    },
  },
})
