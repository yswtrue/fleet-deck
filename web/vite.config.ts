import { imagetools } from 'vite-imagetools'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// The hub address comes from the environment (devenv derives
// FLEETDECK_HUB_ADDR from .env); nothing is hardcoded here.
const hubAddr = process.env.FLEETDECK_HUB_ADDR
if (!hubAddr) {
  throw new Error('FLEETDECK_HUB_ADDR is not set (run inside devenv shell, see .env.example)')
}

// https://vite.dev/config/
// vite-imagetools resizes/re-encodes imported images at build time from
// query directives (e.g. `?width=64&format=webp`); the full-res original in
// src/assets is never served.
export default defineConfig({
  plugins: [react(), imagetools()],
  // The SPA defaults to same-origin API calls; in development the dev server
  // proxies them to the hub so no CORS or VITE_HUB_URL is needed.
  server: {
    proxy: {
      '/api': `http://${hubAddr}`,
      '/ws': { target: `ws://${hubAddr}`, ws: true },
    },
  },
})
