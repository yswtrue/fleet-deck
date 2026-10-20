import { formatCompact } from '../util.ts'
import styles from './SummaryCard.module.css'

/** Summary tile: compact count on display, exact value in the tooltip. */
export function SummaryCard({ label, value }: { label: string; value: number }) {
  return (
    <div className={styles.tile}>
      <span className={styles.label}>{label}</span>
      <span className={styles.value} title={String(value)}>
        {formatCompact(value)}
      </span>
    </div>
  )
}
