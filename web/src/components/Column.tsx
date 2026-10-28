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

  // Queued tasks render first, in queue order (queued_at ascending). The
  // position is the index within that queue, so a waiting column that also
  // holds backlog tasks still shows "Queued #1" for the next one to start.
  const queued = tasks
    .filter((task) => task.queued_at)
    .sort((a, b) => Date.parse(a.queued_at!) - Date.parse(b.queued_at!))
  const queuePositions = new Map(queued.map((task, index) => [task.id, index + 1]))
  const ordered = [...queued, ...tasks.filter((task) => !task.queued_at)]

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
        {ordered.length === 0 ? (
          <p className={styles.empty}>{t('column.empty')}</p>
        ) : (
          ordered.map((task) => (
            <TaskCard
              key={task.id}
              task={task}
              subtasks={subtasksByTask[task.id] ?? []}
              queuePosition={queuePositions.get(task.id)}
              messageApi={messageApi}
            />
          ))
        )}
      </div>
    </section>
  )
}
