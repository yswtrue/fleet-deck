import { permissionReplyOptionID } from '../permission.ts'
import styles from './MessageBubble.module.css'
import type { Message } from '../types.ts'

const ROLE_LABELS: Record<Message['role'], string> = {
  user: 'You',
  assistant: 'Assistant',
  system: 'System',
}

export function MessageBubble({ message }: { message: Message }) {
  // A permission reply is stored verbatim as `[permission_response] option_id=…`
  // (the locked adapter wire format); render the chosen option instead of the
  // marker.
  const permissionOptionID = permissionReplyOptionID(message.content)
  return (
    <article className={styles.bubble} data-role={message.role}>
      <header className={styles.meta}>
        <span className={styles.role} data-role={message.role}>
          {ROLE_LABELS[message.role]}
        </span>
        <time className={styles.time} dateTime={message.created_at}>
          {new Date(message.created_at).toLocaleTimeString()}
        </time>
      </header>
      {permissionOptionID !== null ? (
        <div className={styles.permission}>
          <span className={styles.permissionTag}>Permission</span>
          <code className={styles.permissionOption}>{permissionOptionID}</code>
        </div>
      ) : (
        <p className={styles.content}>{message.content}</p>
      )}
    </article>
  )
}
