import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// The hub address comes from the environment (devenv derives
// FLEETDECK_HUB_ADDR from .env); nothing is hardcoded here.
const hubAddr = process.env.FLEETDECK_HUB_ADDR
if (!hubAddr) {
  throw new Error('FLEETDECK_HUB_ADDR is not set (run inside devenv shell, see .env.example)')
}

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  // The SPA defaults to same-origin API calls; in development the dev server
  // proxies them to the hub so no CORS or VITE_HUB_URL is needed.
  server: {
    proxy: {
      '/api': `http://${hubAddr}`,
      '/ws': { target: `ws://${hubAddr}`, ws: true },
    },
  },
})
