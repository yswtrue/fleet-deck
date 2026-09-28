import { Button, message } from 'antd'
import styles from './EmptyState.module.css'

export interface EmptyStateProps {
  /** Leading glyph; defaults to the empty-tray mark. */
  icon?: React.ReactNode
  title: string
  /** One-liner under the title. */
  children?: React.ReactNode
  /** Call-to-action rendered under the description. */
  action?: React.ReactNode
  /** Copyable command shown in a mono block with a copy button. */
  snippet?: string
}

function DefaultIcon() {
  return (
    <svg viewBox="0 0 24 24" width="28" height="28" aria-hidden="true" focusable="false">
      <path
        d="M3 13.5 5.4 6.2A2 2 0 0 1 7.3 4.8h9.4a2 2 0 0 1 1.9 1.4L21 13.5V18a1.5 1.5 0 0 1-1.5 1.5h-15A1.5 1.5 0 0 1 3 18z"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinejoin="round"
      />
      <path
        d="M3 13.5h5l1.2 2.2h5.6L16 13.5h5"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinejoin="round"
      />
    </svg>
  )
}

function CopyIcon() {
  return (
    <svg viewBox="0 0 24 24" width="14" height="14" aria-hidden="true" focusable="false">
      <rect x="8" y="8" width="12" height="12" rx="2" fill="none" stroke="currentColor" strokeWidth="1.6" />
      <path
        d="M16 5.5A1.5 1.5 0 0 0 14.5 4H5.5A1.5 1.5 0 0 0 4 5.5v9A1.5 1.5 0 0 0 5.5 16"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.6"
        strokeLinecap="round"
      />
    </svg>
  )
}

export function EmptyState({ icon, title, children, action, snippet }: EmptyStateProps) {
  // `message.useMessage()` (not the static `message` API): antd v5's static
  // holders go through the legacy ReactDOM render, which React 19 removed.
  const [messageApi, messageHolder] = message.useMessage()

  const copySnippet = async () => {
    if (!snippet) return
    try {
      await navigator.clipboard.writeText(snippet)
      messageApi.success('Command copied')
    } catch {
      messageApi.error('Could not copy the command')
    }
  }

  return (
    <div className={styles.empty}>
      {messageHolder}
      <div className={styles.icon}>{icon ?? <DefaultIcon />}</div>
      <h3 className={styles.title}>{title}</h3>
      {children && <p className={styles.description}>{children}</p>}
      {snippet && (
        <div className={styles.snippet}>
          <code className={styles.snippetCode} title={snippet}>
            {snippet}
          </code>
          <Button size="small" className={styles.copy} onClick={copySnippet} icon={<CopyIcon />}>
            Copy
          </Button>
        </div>
      )}
      {action && <div className={styles.action}>{action}</div>}
    </div>
  )
}
