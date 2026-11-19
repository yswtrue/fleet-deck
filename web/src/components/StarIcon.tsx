import type { CSSProperties } from 'react'

/**
 * Five-point star used by the favorite toggle and the sidebar favorites
 * submenu. antd menus clone their icon and inject `className`/`style`; both
 * must reach the `<svg>` for the collapsed rail rendering.
 */
export function StarIcon({
  filled = false,
  className,
  style,
}: {
  filled?: boolean
  className?: string
  style?: CSSProperties
}) {
  return (
    <svg
      className={className}
      style={style}
      viewBox="0 0 24 24"
      width="16"
      height="16"
      fill={filled ? 'currentColor' : 'none'}
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      <path d="M12 3.6l2.6 5.27 5.82.85-4.21 4.1 1 5.79L12 16.87l-5.21 2.74 1-5.79-4.21-4.1 5.82-.85z" />
    </svg>
  )
}
