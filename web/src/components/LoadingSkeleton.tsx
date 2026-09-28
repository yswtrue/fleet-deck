import { Skeleton } from 'antd'
import styles from './LoadingSkeleton.module.css'

/**
 * Card-grid loading placeholder, tuned to the responsive overview grids
 * (`NodeCard`, `WorkspaceCard`). `count` sets how many cards to fake; callers
 * that load smaller panels pass a lower number.
 */
export function LoadingSkeleton({ count = 6 }: { count?: number }) {
  return (
    <div className="fd-grid" role="status" aria-label="Loading">
      {Array.from({ length: count }).map((_, index) => (
        <div key={index} className={styles.card}>
          <Skeleton active title paragraph={{ rows: 2 }} />
        </div>
      ))}
    </div>
  )
}
