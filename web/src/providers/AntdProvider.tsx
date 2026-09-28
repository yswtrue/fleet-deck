import { useEffect, useState } from 'react'
import type { ReactNode } from 'react'
import { ConfigProvider } from 'antd'
import enUS from 'antd/locale/en_US'
import zhCN from 'antd/locale/zh_CN'
import { i18n } from '../i18n/index.ts'
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
  // antd ships its own strings (pagination, empty, modal buttons); track the
  // i18next language so ConfigProvider can hand it the matching pack.
  const [lang, setLang] = useState<string>(() => i18n.resolvedLanguage ?? i18n.language)

  useEffect(() => {
    const onLanguageChanged = (lng: string) => setLang(lng)
    i18n.on('languageChanged', onLanguageChanged)
    return () => {
      i18n.off('languageChanged', onLanguageChanged)
    }
  }, [])

  useEffect(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return
    const mql = window.matchMedia(DARK_MEDIA_QUERY)
    const onChange = (event: MediaQueryListEvent) => setMode(event.matches ? 'dark' : 'light')
    mql.addEventListener('change', onChange)
    return () => mql.removeEventListener('change', onChange)
  }, [])

  return (
    <ConfigProvider locale={lang.startsWith('zh') ? zhCN : enUS} theme={fleetdeckTheme(mode)}>
      {children}
    </ConfigProvider>
  )
}
