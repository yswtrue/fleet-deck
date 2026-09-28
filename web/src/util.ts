import { i18n } from './i18n/index.ts'

/**
 * Compact relative age ("5m ago") localized through the `common.time` keys,
 * falling back to a locale-aware date for anything older than a month.
 */
export function formatRelative(iso: string): string {
  const date = new Date(iso)
  const seconds = Math.floor((Date.now() - date.getTime()) / 1000)
  if (seconds < 60) return i18n.t('common:time.justNow')
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return i18n.t('common:time.minutesAgo', { n: minutes })
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return i18n.t('common:time.hoursAgo', { n: hours })
  const days = Math.floor(hours / 24)
  if (days < 30) return i18n.t('common:time.daysAgo', { n: days })
  return date.toLocaleDateString(i18n.language)
}

/** Localized label for a task/session/node status token. */
export function statusLabel(status: string): string {
  return i18n.t('common:status.' + status, { defaultValue: status })
}
