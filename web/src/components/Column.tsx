import type { MessageInstance } from 'antd/es/message/interface'
import styles from './Column.module.css'
import { TaskCard } from './TaskCard.tsx'
import { statusLabel } from '../util.ts'
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
  const title = statusLabel(status)
  return (
    <section className={styles.column} role="region" aria-label={`${title} tasks`}>
      <header className={styles.header}>
        <span className={styles.dot} data-status={status} aria-hidden="true" />
        <h2 className={styles.title}>{title}</h2>
        <span className={styles.count} aria-label={`${tasks.length} tasks`}>
          {tasks.length}
        </span>
      </header>
      <div className={styles.list}>
        {tasks.length === 0 ? (
          <p className={styles.empty}>Nothing here yet</p>
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
