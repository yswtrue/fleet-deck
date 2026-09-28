// i18n bootstrap for the FleetDeck web UI. English and Simplified Chinese are
// bundled; the active language follows, in order: a saved choice
// (localStorage), then the browser language, then English.
//
// Locale files are namespaced per surface (common, nav, auth, pages, board,
// session) so feature areas can add copy without colliding. `common` holds
// shared atoms (status labels, action verbs, relative time) — extend it only
// for genuinely shared strings.
import i18n from 'i18next'
import { initReactI18next, useTranslation } from 'react-i18next'

import enCommon from './locales/en/common.json'
import enNav from './locales/en/nav.json'
import enAuth from './locales/en/auth.json'
import enPages from './locales/en/pages.json'
import enBoard from './locales/en/board.json'
import enSession from './locales/en/session.json'
import enStats from './locales/en/stats.json'
import enNotify from './locales/en/notify.json'
import zhCommon from './locales/zh-CN/common.json'
import zhNav from './locales/zh-CN/nav.json'
import zhAuth from './locales/zh-CN/auth.json'
import zhPages from './locales/zh-CN/pages.json'
import zhBoard from './locales/zh-CN/board.json'
import zhSession from './locales/zh-CN/session.json'
import zhStats from './locales/zh-CN/stats.json'
import zhNotify from './locales/zh-CN/notify.json'

export const LANGS = ['en', 'zh-CN'] as const
export type Lang = (typeof LANGS)[number]

const STORAGE_KEY = 'fleetdeck.lang'

function detectLang(): Lang {
  if (typeof window !== 'undefined') {
    const saved = window.localStorage.getItem(STORAGE_KEY)
    if (saved === 'en' || saved === 'zh-CN') return saved
    if (navigator.language.toLowerCase().startsWith('zh')) return 'zh-CN'
  }
  return 'en'
}

void i18n.use(initReactI18next).init({
  resources: {
    en: {
      common: enCommon,
      nav: enNav,
      auth: enAuth,
      pages: enPages,
      board: enBoard,
      session: enSession,
      stats: enStats,
      notify: enNotify,
    },
    'zh-CN': {
      common: zhCommon,
      nav: zhNav,
      auth: zhAuth,
      pages: zhPages,
      board: zhBoard,
      session: zhSession,
      stats: zhStats,
      notify: zhNotify,
    },
  },
  lng: detectLang(),
  fallbackLng: 'en',
  defaultNS: 'common',
  interpolation: { escapeValue: false }, // React escapes for us
})

// Keep <html lang> in step with the active language (screen readers, CSS
// :lang() selectors).
i18n.on('languageChanged', (lng) => {
  if (typeof document !== 'undefined') document.documentElement.lang = lng
})
if (typeof document !== 'undefined') document.documentElement.lang = i18n.language

/** Persist the choice and switch the active language. */
export function setLanguage(lang: Lang): void {
  if (typeof window !== 'undefined') window.localStorage.setItem(STORAGE_KEY, lang)
  void i18n.changeLanguage(lang)
}

export { i18n, useTranslation }
