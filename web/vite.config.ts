import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  // The SPA defaults to same-origin API calls; in development the dev server
  // proxies them to the hub so no CORS or VITE_HUB_URL is needed.
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:7710',
      '/ws': { target: 'ws://127.0.0.1:7710', ws: true },
    },
  },
})
