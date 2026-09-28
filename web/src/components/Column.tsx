import type { MessageInstance } from 'antd/es/message/interface'
import styles from './Column.module.css'
import { TaskCard } from './TaskCard.tsx'
import { statusLabel } from '../util.ts'
import { useTranslation } from '../i18n/index.ts'
import type { Task, Subtask } from '../types.ts'

export function Column({
  status,
  tasks,
  subtasksByTask,
  messageApi,
}: {
  status: Task['status']
  tasks: Task[]
  subtasksByTask: Record<string, Subtask[]>
  /** Board-scoped toast instance, forwarded to the task cards (see TaskCard). */
  messageApi?: MessageInstance
}) {
  const { t } = useTranslation('board')
  const title = statusLabel(status)
  return (
    <section
      className={styles.column}
      role="region"
      aria-label={t('column.region', { status: title })}
    >
      <header className={styles.header}>
        <span className={styles.dot} data-status={status} aria-hidden="true" />
        <h2 className={styles.title}>{title}</h2>
        <span className={styles.count} aria-label={t('column.count', { count: tasks.length })}>
          {tasks.length}
        </span>
      </header>
      <div className={styles.list}>
        {tasks.length === 0 ? (
          <p className={styles.empty}>{t('column.empty')}</p>
        ) : (
          tasks.map((task) => (
            <TaskCard
              key={task.id}
              task={task}
              subtasks={subtasksByTask[task.id] ?? []}
              messageApi={messageApi}
            />
          ))
        )}
      </div>
    </section>
  )
}
