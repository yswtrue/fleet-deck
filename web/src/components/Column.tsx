import type { MessageInstance } from 'antd/es/message/interface'
import styles from './Column.module.css'
import { TaskCard } from './TaskCard.tsx'
import { statusLabel } from '../util.ts'
import { useTranslation } from '../i18n/index.ts'
import type { Task, Subtask } from '../types.ts'

/** BoardColumn is one kanban column: a title, a dot colour, and the task
 * statuses it collects. `failed` shares the attention column with
 * `awaiting_user` — both need the operator, and neither belongs under Done. */
export type BoardColumn = {
  key: string
  statuses: Task['status'][]
  dot: Task['status']
  /** Board-scoped i18n key for a column whose label is not a single status. */
  titleKey?: string
}

export function Column({
  column,
  tasks,
  subtasksByTask,
  messageApi,
}: {
  column: BoardColumn
  tasks: Task[]
  subtasksByTask: Record<string, Subtask[]>
  /** Board-scoped toast instance, forwarded to the task cards (see TaskCard). */
  messageApi?: MessageInstance
}) {
  const { t } = useTranslation('board')
  const title = column.titleKey ? t(column.titleKey) : statusLabel(column.dot)

  return (
    <section
      className={styles.column}
      role="region"
      aria-label={t('column.region', { status: title })}
    >
      <header className={styles.header}>
        <span className={styles.dot} data-status={column.dot} aria-hidden="true" />
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
