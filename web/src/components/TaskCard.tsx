import { useNavigate } from 'react-router-dom'
import { Button, Card, Dropdown, message } from 'antd'
import type { MenuProps } from 'antd'
import type { MessageInstance } from 'antd/es/message/interface'
import { SubtaskDots } from './SubtaskDots.tsx'
import { TASK_STATUS_TRANSITIONS, useTaskStatusMutation } from '../hooks/useTaskBoard.ts'
import { formatRelative, statusLabel } from '../util.ts'
import styles from './TaskCard.module.css'
import type { Task, Subtask } from '../types.ts'

export function TaskCard({
  task,
  subtasks = [],
  messageApi: externalMessageApi,
}: {
  task: Task
  subtasks?: Subtask[]
  /**
   * Board-scoped toast instance. A status move relocates the card to another
   * column, remounting it — a card-owned toast holder would unmount with the
   * card and drop the confirmation. Standalone use (tests) falls back to a
   * local holder.
   */
  messageApi?: MessageInstance
}) {
  const navigate = useNavigate()
  // The static antd message API renders nothing under React 19; the hook-based
  // API is the one that actually mounts toasts.
  const [localMessageApi, localMessageHolder] = message.useMessage()
  const messageApi = externalMessageApi ?? localMessageApi
  const statusMutation = useTaskStatusMutation()
  // `done` is terminal: no menu at all, and the hub would 409 anyway.
  const transitions = TASK_STATUS_TRANSITIONS[task.status]

  const openSession = () => {
    if (task.session_id) navigate(`/sessions/${task.session_id}`, { state: { task } })
  }

  // Nested actions handle their own keys (Enter opens the move menu); the card
  // only navigates when the event targets the card itself.
  const handleKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    if (event.key === 'Enter' && event.target === event.currentTarget) openSession()
  }

  const menuItems: MenuProps['items'] = transitions.map((status) => ({
    key: status,
    label: `Move to ${statusLabel(status)}`,
  }))

  const handleMenuClick: MenuProps['onClick'] = ({ key }) => {
    const status = key as Task['status']
    // mutateAsync + promise callbacks, not mutate(vars, callbacks): react-query
    // v5 only runs per-mutate callbacks while the observer still has listeners
    // (mutationObserver.js `hasListeners()` gate), and this card unmounts when
    // the optimistic move relocates it to another column. The returned promise
    // settles regardless of unmount, so the toast always fires.
    statusMutation
      .mutateAsync({ id: task.id, status })
      .then(() => messageApi.success(`“${task.title}” moved to ${statusLabel(status)}`))
      .catch((err: unknown) =>
        messageApi.error(err instanceof Error ? `Could not move task: ${err.message}` : 'Could not move task')
      )
  }

  return (
    <Card
      size="small"
      hoverable
      tabIndex={0}
      className={styles.card}
      onClick={openSession}
      onKeyDown={handleKeyDown}
      role="button"
      aria-label={`Task ${task.title}, status ${statusLabel(task.status)}`}
      styles={{ body: { padding: 12 } }}
    >
      {externalMessageApi ? null : localMessageHolder}
      <div className={styles.top}>
        <span className={styles.statusChip} data-status={task.status}>
          {statusLabel(task.status)}
        </span>
        <span className={styles.agentChip}>{task.agent_type}</span>
        {transitions.length > 0 && (
          <Dropdown
            menu={{ items: menuItems, onClick: handleMenuClick }}
            trigger={['click']}
            placement="bottomRight"
          >
            <Button
              size="small"
              type="text"
              className={styles.moveButton}
              aria-label={`Change status of ${task.title}`}
              title="Move to…"
              onClick={(event) => event.stopPropagation()}
            >
              Move…
            </Button>
          </Dropdown>
        )}
      </div>
      <p className={styles.title}>{task.title}</p>
      {task.prompt && <p className={styles.prompt}>{task.prompt}</p>}
      <SubtaskDots subtasks={subtasks} />
      <p className={styles.footer}>Updated {formatRelative(task.updated_at)}</p>
    </Card>
  )
}
