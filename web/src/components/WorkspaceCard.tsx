import { useNavigate } from 'react-router-dom'
import { Card, Typography } from 'antd'
import { statusLabel } from '../util.ts'
import type { TaskStatus, Workspace } from '../types.ts'
import styles from './WorkspaceCard.module.css'

/** Display order for the task-count chips: oldest state first. */
const STATUS_ORDER: TaskStatus[] = ['waiting', 'running', 'awaiting_user', 'done']

export function WorkspaceCard({
  workspace,
  statusCounts,
}: {
  workspace: Workspace
  statusCounts: Partial<Record<TaskStatus, number>>
}) {
  const navigate = useNavigate()

  const handleKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    if (event.key === 'Enter') {
      navigate(`/workspaces/${workspace.id}`)
    }
  }

  const counted = STATUS_ORDER.filter((status) => (statusCounts[status] ?? 0) > 0)

  return (
    <Card
      className={styles.card}
      tabIndex={0}
      role="button"
      onClick={() => navigate(`/workspaces/${workspace.id}`)}
      onKeyDown={handleKeyDown}
      aria-label={`Workspace ${workspace.name}`}
    >
      <Typography.Title level={5} className={styles.name} ellipsis={{ tooltip: workspace.name }}>
        {workspace.name}
      </Typography.Title>
      <div className={styles.path} title={workspace.path}>
        {workspace.path}
      </div>
      <div className="fd-chips">
        {counted.length > 0 ? (
          counted.map((status) => (
            <span key={status} className="fd-pill" data-status={status}>
              {statusLabel(status)} {statusCounts[status]}
            </span>
          ))
        ) : (
          <span className={styles.muted}>No tasks yet</span>
        )}
      </div>
    </Card>
  )
}
