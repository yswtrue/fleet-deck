import { permissionReplyOptionID } from '../permission.ts'
import { i18n, useTranslation } from '../i18n/index.ts'
import styles from './MessageBubble.module.css'
import type { Message } from '../types.ts'

export function MessageBubble({ message }: { message: Message }) {
  const { t } = useTranslation('session')
  // A permission reply is stored verbatim as `[permission_response] option_id=…`
  // (the locked adapter wire format); render the chosen option instead of the
  // marker.
  const permissionOptionID = permissionReplyOptionID(message.content)
  // Replayed history carries no timestamps (the replay stream has none and no
  // timestamp is ever fabricated): hide the time rather than render a wrong
  // one.
  const ts = new Date(message.created_at)
  const hasTime = !Number.isNaN(ts.getTime()) && ts.getFullYear() > 2000
  return (
    <article className={styles.bubble} data-role={message.role}>
      <header className={styles.meta}>
        <span className={styles.role} data-role={message.role}>
          {t(`role.${message.role}`)}
        </span>
        {hasTime && (
          <time className={styles.time} dateTime={message.created_at}>
            {ts.toLocaleTimeString(i18n.language)}
          </time>
        )}
      </header>
      {permissionOptionID !== null ? (
        <div className={styles.permission}>
          <span className={styles.permissionTag}>{t('permission.tag')}</span>
          <code className={styles.permissionOption}>{permissionOptionID}</code>
        </div>
      ) : (
        <p className={styles.content}>{message.content}</p>
      )}
    </article>
  )
}
