import { imagetools } from 'vite-imagetools'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// vite-imagetools resizes/re-encodes imported images at build time from query
// directives (e.g. `?width=64&format=webp`); the full-res original in
// src/assets is never served.
const plugins = [react(), imagetools()]

export default defineConfig(({ command }) => {
  // The dev server proxies /api and /ws to the hub, whose address comes from
  // the environment (devenv derives FLEETDECK_HUB_ADDR from .env); nothing is
  // hardcoded here. A production build emits only the static bundle, so it
  // needs no hub address — the SPA calls its own origin at runtime.
  const hubAddr = process.env.FLEETDECK_HUB_ADDR
  if (command === 'serve' && !hubAddr) {
    throw new Error('FLEETDECK_HUB_ADDR is not set (run inside devenv shell, see .env.example)')
  }

  return {
    plugins,
    server: hubAddr
      ? {
          proxy: {
            '/api': `http://${hubAddr}`,
            '/ws': { target: `ws://${hubAddr}`, ws: true },
          },
        }
      : undefined,
  }
})
