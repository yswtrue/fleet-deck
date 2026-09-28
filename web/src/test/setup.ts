import { cleanup } from '@testing-library/react'
import { afterEach } from 'vitest'

// Initialize i18next before any component renders (default language: en, the
// copy the assertions below pin).
import '../i18n/index.ts'

// Vitest runs without globals, so React Testing Library cannot register its
// auto-cleanup; do it explicitly to keep renders from leaking across tests.
afterEach(cleanup)

// jsdom lacks matchMedia; antd components consult it for responsive behavior.
if (typeof window !== 'undefined' && !window.matchMedia) {
  window.matchMedia = (query: string) =>
    ({
      matches: false,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    }) as MediaQueryList
}
