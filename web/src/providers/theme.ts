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
/** The single interaction accent per appearance (teal; never the status blue). */
export const accentColors: Record<ThemeMode, string> = {
  dark: '#2dd4bf',
  light: '#0f8f85',
}

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
  borderRadius: 10,
  borderRadiusSM: 8,
  borderRadiusLG: 16,
  fontFamily: "system-ui, 'Segoe UI', Roboto, 'Helvetica Neue', sans-serif",
  fontSize: 15,
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
      // Teal accent (see index.css): the locked status palette keeps blue for
      // "running", so selection and primary actions use a distinct hue. antd
      // derives hover/active shades by computing on this value, so it must be
      // a concrete color, not a CSS custom property.
      colorPrimary: accentColors[mode],
      colorLink: accentColors[mode],
      colorInfo: palette.blue,
      colorSuccess: palette.green,
      colorWarning: palette.amber,
      colorError: palette.red,
      ...shape,
    },
    components: {
      Layout: {
        siderBg: 'var(--surface)',
        headerBg: 'transparent',
        bodyBg: 'var(--bg)',
      },
      Card: {
        colorBgContainer: 'var(--surface)',
        colorBorderSecondary: 'var(--border)',
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
        itemHoverBg: 'var(--surface-3)',
        itemSelectedBg: 'var(--accent-bg)',
        itemSelectedColor: 'var(--accent-strong)',
        itemColor: 'var(--text)',
        itemHeight: 42,
        itemMarginInline: 10,
        itemBorderRadius: shape.borderRadius,
        subMenuItemBg: 'transparent',
      },
      Modal: {
        borderRadiusLG: shape.borderRadiusLG,
        contentBg: 'var(--surface)',
        headerBg: 'var(--surface)',
        titleFontSize: 18,
      },
      Input: {
        colorBgContainer: 'var(--surface)',
        activeBorderColor: 'var(--accent)',
        hoverBorderColor: 'var(--accent-border)',
      },
      Select: {
        colorBgContainer: 'var(--surface)',
        optionSelectedBg: 'var(--accent-bg)',
      },
      Table: {
        headerBg: 'var(--surface-2)',
        headerColor: 'var(--text-dim)',
        headerSplitColor: 'transparent',
        borderColor: 'var(--border)',
        rowHoverBg: 'var(--surface-3)',
        colorBgContainer: 'var(--surface)',
      },
      Tag: {
        defaultBg: 'var(--surface-2)',
        defaultColor: 'var(--text-h)',
        borderRadiusSM: 999,
      },
      Statistic: {
        contentFontSize: 28,
        titleFontSize: 13,
      },
      Dropdown: {
        colorBgElevated: 'var(--surface)',
        controlItemBgHover: 'var(--surface-3)',
      },
    },
  }
}
