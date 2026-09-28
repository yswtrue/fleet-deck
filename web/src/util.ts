import { i18n } from './i18n/index.ts'

/**
 * Compact number display ("12.4k") for stat tiles; the exact value rides
 * along as a tooltip in SummaryCard.
 */
export function formatCompact(value: number): string {
  if (value < 1000) return String(value)
  const units = ['k', 'M', 'B'] as const
  let scaled = value / 1000
  let unit = 0
  while (scaled >= 1000 && unit < units.length - 1) {
    scaled /= 1000
    unit += 1
  }
  let text = scaled.toFixed(1)
  if (text === '1000.0' && unit < units.length - 1) {
    // 999 999 rounds past the unit: promote ("1M", never "1000.0k").
    text = (scaled / 1000).toFixed(1)
    unit += 1
  }
  return `${text.replace(/\.0$/, '')}${units[unit]}`
}

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
