import styles from './SubtaskDots.module.css'
import type { Subtask } from '../types.ts'

interface SubtaskDotsProps {
  subtasks: Subtask[]
  max?: number
}

export function SubtaskDots({ subtasks, max = 8 }: SubtaskDotsProps) {
  if (subtasks.length === 0) return null
  const done = subtasks.filter((s) => s.status === 'done').length
  const total = Math.min(subtasks.length, max)

  return (
    <div className={styles.wrap} aria-label={`${done} of ${subtasks.length} subtasks done`}>
      <div className={styles.dots}>
        {Array.from({ length: total }).map((_, i) => {
          const status = subtasks[i]?.status
          return <span key={i} className={styles.dot} data-status={status} />
        })}
      </div>
      {subtasks.length > max && <span className={styles.more}>+{subtasks.length - max}</span>}
      <span className={styles.label}>
        {done}/{subtasks.length} done
      </span>
    </div>
  )
}
