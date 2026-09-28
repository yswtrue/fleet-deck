import { theme } from 'antd'
import type { ThemeConfig } from 'antd'

/** OS appearance. Mirrors `@media (prefers-color-scheme: ...)` in `index.css`. */
export type ThemeMode = 'dark' | 'light'

export interface StatusPalette {
  neutral: string
  blue: string
  green: string
  amber: string
  red: string
}

/**
 * FleetDeck status palette for both appearances, kept in sync with the CSS
 * custom properties in `index.css`:
 *
 *   mode   source                          neutral  blue     green    amber    red
 *   dark   @media (prefers-color-scheme)   #9ca3af  #60a5fa  #4ade80  #fbbf24  #f87171
 *   light  :root                           #6b7280  #3b82f6  #22c55e  #f59e0b  #ef4444
 *
 * The light values are the more saturated `:root` set so they keep contrast on
 * a white surface; the dark values are the softer media-query set. Hand-rolled
 * status pills read the CSS vars directly, so the two layers agree per mode.
 */
export const statusColors: Record<ThemeMode, StatusPalette> = {
  dark: {
    neutral: '#9ca3af',
    blue: '#60a5fa',
    green: '#4ade80',
    amber: '#fbbf24',
    red: '#f87171',
  },
  light: {
    neutral: '#6b7280',
    blue: '#3b82f6',
    green: '#22c55e',
    amber: '#f59e0b',
    red: '#ef4444',
  },
}

/** Shared shape tokens; mirrored by `--radius-*` / `--space-*` in `index.css`. */
const shape = {
  borderRadius: 8,
  borderRadiusSM: 6,
  borderRadiusLG: 14,
  fontFamily: "system-ui, 'Segoe UI', Roboto, sans-serif",
} as const

/**
 * Global antd theme for the given appearance: swaps the algorithm between
 * dark and default (light) and maps the matching status palette onto antd's
 * semantic tokens (primary/info = blue, success = green, warning = amber,
 * error = red). Neutral rides on antd's default text/border tone.
 *
 * Component tokens restyle the primitives of the design language on top of
 * that semantic base: rounded cards, quiet buttons, pill tags, soft menus,
 * framed tables and roomy modals. Surface-ish values reference the CSS custom
 * properties so they flip together with `index.css`'s media query.
 */
export function fleetdeckTheme(mode: ThemeMode): ThemeConfig {
  const palette = statusColors[mode]
  return {
    algorithm: mode === 'dark' ? theme.darkAlgorithm : theme.defaultAlgorithm,
    token: {
      colorPrimary: palette.blue,
      colorInfo: palette.blue,
      colorSuccess: palette.green,
      colorWarning: palette.amber,
      colorError: palette.red,
      ...shape,
    },
    components: {
      Card: {
        borderRadiusLG: shape.borderRadiusLG,
        headerBg: 'transparent',
        paddingLG: 20,
        boxShadowTertiary: 'var(--shadow-sm)',
      },
      Button: {
        borderRadius: shape.borderRadius,
        fontWeight: 500,
        primaryShadow: 'none',
        defaultShadow: 'none',
      },
      Menu: {
        itemBg: 'transparent',
        itemHoverBg: 'var(--code-bg)',
        itemSelectedBg: 'var(--accent-bg)',
        itemSelectedColor: 'var(--accent)',
        itemBorderRadius: shape.borderRadius,
        subMenuItemBg: 'transparent',
      },
      Modal: {
        borderRadiusLG: shape.borderRadiusLG,
        contentBg: 'var(--bg)',
        headerBg: 'var(--bg)',
        titleFontSize: 18,
      },
      Table: {
        headerBg: 'var(--code-bg)',
        headerColor: 'var(--text-h)',
        headerSplitColor: 'transparent',
        borderColor: 'var(--border)',
        rowHoverBg: 'var(--code-bg)',
      },
      Tag: {
        defaultBg: 'var(--status-pending-bg)',
        defaultColor: 'var(--text-h)',
        borderRadiusSM: 999,
      },
    },
  }
}
