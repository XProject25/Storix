import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Storix web build. Developed by X Project.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    strictPort: false,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8686',
        changeOrigin: false,
        ws: false,
      },
    },
  },
  build: {
    outDir: '../internal/web/dist',
    emptyOutDir: true,
    assetsDir: 'assets',
    sourcemap: false,
    chunkSizeWarningLimit: 2000,
    rollupOptions: {
      output: {
        manualChunks(id: string) {
          if (id.includes('monaco-editor')) return 'monaco'
          if (id.includes('node_modules/react') || id.includes('node_modules/react-dom')) return 'react'
          if (id.includes('node_modules/@tanstack')) return 'query'
          if (id.includes('node_modules/tus-js-client')) return 'tus'
          return undefined
        },
      },
    },
  },
})
