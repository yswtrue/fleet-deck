import { useNavigate } from 'react-router-dom'
import { Card, Typography } from 'antd'
import type { MessageInstance } from 'antd/es/message/interface'
import { statusLabel } from '../util.ts'
import { useTranslation } from '../i18n/index.ts'
import { WorkspaceActions } from './WorkspaceActions.tsx'
import type { TaskStatus, Workspace } from '../types.ts'
import styles from './WorkspaceCard.module.css'

/** Display order for the task-count chips: oldest state first. */
const STATUS_ORDER: TaskStatus[] = ['waiting', 'running', 'awaiting_user', 'done']

export function WorkspaceCard({
  workspace,
  statusCounts,
  messageApi,
}: {
  workspace: Workspace
  statusCounts: Partial<Record<TaskStatus, number>>
  /** Parent-owned toast holder so delete confirmation survives the card. */
  messageApi?: MessageInstance
}) {
  const navigate = useNavigate()
  const { t } = useTranslation('pages')

  const openWorkspace = () => navigate(`/workspaces/${workspace.id}`)

  // Nested actions handle their own keys; the card only navigates when the
  // event targets the card itself.
  const handleKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    if (event.key === 'Enter' && event.target === event.currentTarget) openWorkspace()
  }

  // Dialogs and dropdown menus render through portals: their events bubble
  // through the React tree to this card, so only navigate for in-card targets.
  const handleClick = (event: React.MouseEvent<HTMLDivElement>) => {
    if (event.target instanceof Node && !event.currentTarget.contains(event.target)) return
    openWorkspace()
  }

  const counted = STATUS_ORDER.filter((status) => (statusCounts[status] ?? 0) > 0)

  return (
    <Card
      className={styles.card}
      tabIndex={0}
      role="button"
      onClick={handleClick}
      onKeyDown={handleKeyDown}
      aria-label={t('workspace.cardAria', { name: workspace.name })}
    >
      <div className={styles.head}>
        <Typography.Title level={5} className={styles.name} ellipsis={{ tooltip: workspace.name }}>
          {workspace.name}
        </Typography.Title>
        <span className={styles.actions}>
          <WorkspaceActions workspace={workspace} messageApi={messageApi} />
        </span>
      </div>
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
          <span className={styles.muted}>{t('workspace.noTasksYet')}</span>
        )}
      </div>
    </Card>
  )
}
