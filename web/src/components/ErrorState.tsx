import { Button } from 'antd'
import { useTranslation } from '../i18n/index.ts'
import styles from './ErrorState.module.css'

export interface ErrorStateProps {
  title: string
  /** Error detail shown under the title. */
  children?: React.ReactNode
  /** Present when the failed query can be refetched. */
  onRetry?: () => void
}

function ErrorIcon() {
  return (
    <svg viewBox="0 0 24 24" width="28" height="28" aria-hidden="true" focusable="false">
      <circle cx="12" cy="12" r="9" fill="none" stroke="currentColor" strokeWidth="1.5" />
      <path d="M12 7.5v5.5" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" />
      <circle cx="12" cy="16.3" r="1.1" fill="currentColor" />
    </svg>
  )
}

export function ErrorState({ title, children, onRetry }: ErrorStateProps) {
  const { t } = useTranslation('pages')
  return (
    <div className={styles.error} role="alert">
      <div className={styles.icon}>
        <ErrorIcon />
      </div>
      <h3 className={styles.title}>{title}</h3>
      {children && <p className={styles.description}>{children}</p>}
      {onRetry && (
        <div className={styles.action}>
          <Button type="primary" onClick={onRetry}>
            {t('common:actions.retry')}
          </Button>
        </div>
      )}
    </div>
  )
}
