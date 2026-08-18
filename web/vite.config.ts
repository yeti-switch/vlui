import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],

  // Relative asset URLs. The Go server injects <base href> at serve time, so
  // one build works whether the app is mounted at / or at /logs — the mount
  // point is deployment configuration, not a build flag.
  base: './',

  build: {
    // Embedded by go:embed from here.
    outDir: 'dist',
    emptyOutDir: true,
    // The whole UI is one view; a chunk graph would only add round trips.
    chunkSizeWarningLimit: 800,
  },

  server: {
    port: 5173,
    // `make dev` runs the Go process on :8080; this proxies the API to it so
    // the SPA gets HMR without a second config for CORS or cookies.
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: false,
      },
    },
  },
})
