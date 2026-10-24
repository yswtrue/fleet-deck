import { permissionReplyOptionID } from '../permission.ts'
import { parseElicitationReply } from '../elicitation.ts'
import { i18n, useTranslation } from '../i18n/index.ts'
import styles from './MessageBubble.module.css'
import type { Message } from '../types.ts'

export function MessageBubble({ message }: { message: Message }) {
  const { t } = useTranslation('session')
  // A permission reply is stored verbatim as `[permission_response] option_id=…`
  // (the locked adapter wire format); render the chosen option instead of the
  // marker.
  const permissionOptionID = permissionReplyOptionID(message.content)
  // A form elicitation reply is likewise stored verbatim; render its action
  // instead of the marker.
  const elicitationReply = parseElicitationReply(message.content)
  // Replayed history carries no timestamps (the replay stream has none and no
  // timestamp is ever fabricated): hide the time rather than render a wrong
  // one.
  const ts = new Date(message.created_at)
  const hasTime = !Number.isNaN(ts.getTime()) && ts.getFullYear() > 2000
  const tool = message.tool
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
      {tool ? (
        <div className={styles.tool}>
          <span className={styles.toolTitle}>{tool.title || message.content || tool.id}</span>
          {tool.kind && <code className={styles.toolKind}>{tool.kind}</code>}
          {tool.status && (
            <span className={styles.toolStatus} data-status={tool.status}>
              {t(`tool.status.${tool.status}`, { defaultValue: tool.status })}
            </span>
          )}
        </div>
      ) : permissionOptionID !== null ? (
        <div className={styles.permission}>
          <span className={styles.permissionTag}>{t('permission.tag')}</span>
          <code className={styles.permissionOption}>{permissionOptionID}</code>
        </div>
      ) : elicitationReply ? (
        <div className={styles.permission}>
          <span className={styles.permissionTag}>{t('elicitation.tag')}</span>
          <code className={styles.permissionOption}>
            {t(`elicitation.action.${elicitationReply.action}`)}
          </code>
        </div>
      ) : (
        <p className={styles.content}>{message.content}</p>
      )}
    </article>
  )
}
