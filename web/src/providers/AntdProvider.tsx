import { useEffect, useState } from 'react'
import type { ReactNode } from 'react'
import { ConfigProvider } from 'antd'
import { fleetdeckTheme } from './theme.ts'
import type { ThemeMode } from './theme.ts'

const DARK_MEDIA_QUERY = '(prefers-color-scheme: dark)'

function currentMode(): ThemeMode {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return 'dark'
  return window.matchMedia(DARK_MEDIA_QUERY).matches ? 'dark' : 'light'
}

/**
 * Global antd configuration: the dark or light algorithm + the matching
 * FleetDeck status palette. Mounted once at the app root.
 *
 * Tracks the OS `prefers-color-scheme` live: the `change` listener re-renders
 * with the new algorithm, and the CSS vars in `index.css` flip in lock-step via
 * their own media query.
 */
export function AntdProvider({ children }: { children: ReactNode }) {
  const [mode, setMode] = useState<ThemeMode>(currentMode)

  useEffect(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return
    const mql = window.matchMedia(DARK_MEDIA_QUERY)
    const onChange = (event: MediaQueryListEvent) => setMode(event.matches ? 'dark' : 'light')
    mql.addEventListener('change', onChange)
    return () => mql.removeEventListener('change', onChange)
  }, [])

  return <ConfigProvider theme={fleetdeckTheme(mode)}>{children}</ConfigProvider>
}
