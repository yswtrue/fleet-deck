import { useNavigate } from 'react-router-dom'
import { Button, Card, Dropdown, message } from 'antd'
import type { MenuProps } from 'antd'
import type { MessageInstance } from 'antd/es/message/interface'
import { SubtaskDots } from './SubtaskDots.tsx'
import {
  TASK_STATUS_TRANSITIONS,
  useArchiveTaskMutation,
  useStartTaskMutation,
  useTaskStatusMutation,
} from '../hooks/useTaskBoard.ts'
import { formatRelative, statusLabel } from '../util.ts'
import { useTranslation } from '../i18n/index.ts'
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
  const { t } = useTranslation('board')
  // The static antd message API renders nothing under React 19; the hook-based
  // API is the one that actually mounts toasts.
  const [localMessageApi, localMessageHolder] = message.useMessage()
  const messageApi = externalMessageApi ?? localMessageApi
  const statusMutation = useTaskStatusMutation()
  const startMutation = useStartTaskMutation()
  const archiveMutation = useArchiveTaskMutation()
  // `done` is terminal: no move menu at all, and the hub would 409 anyway.
  // Archive/unarchive and backlog-start are separate actions below.
  const transitions = TASK_STATUS_TRANSITIONS[task.status]
  const isBacklog = task.status === 'waiting' && !task.auto_start
  const isArchived = !!task.archived_at

  const openSession = () => {
    if (task.session_id) {
      navigate(`/sessions/${task.session_id}`, { state: { task } })
      return
    }
    // Session-less tasks (e.g. waiting while their node is offline) have
    // nothing to open: say so instead of swallowing the click.
    messageApi.warning(t('card.noSessionToast'))
  }

  // Nested actions handle their own keys (Enter opens the move menu); the card
  // only navigates when the event targets the card itself.
  const handleKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    if (event.key === 'Enter' && event.target === event.currentTarget) openSession()
  }

  const menuItems: MenuProps['items'] = transitions.map((status) => ({
    key: status,
    label: t('card.moveTo', { status: statusLabel(status) }),
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
      .then(() =>
        messageApi.success(t('card.movedTo', { title: task.title, status: statusLabel(status) }))
      )
      .catch((err: unknown) =>
        messageApi.error(
          err instanceof Error
            ? t('card.moveErrorDetail', { message: err.message })
            : t('card.moveError')
        )
      )
  }

  // Start/archive (unmount-prone: archive removes the card from the default
  // list, start moves it) use the same mutateAsync + promise pattern as the
  // status move above so the toast always fires.
  const handleStart = (event: React.MouseEvent) => {
    event.stopPropagation()
    startMutation
      .mutateAsync(task.id)
      .then(() => messageApi.success(t('card.started', { title: task.title })))
      .catch((err: unknown) =>
        messageApi.error(
          err instanceof Error
            ? t('card.startErrorDetail', { message: err.message })
            : t('card.startError')
        )
      )
  }

  const handleArchiveToggle = (event: React.MouseEvent) => {
    event.stopPropagation()
    if (isArchived) {
      archiveMutation
        .mutateAsync({ id: task.id, archived: false })
        .then(() => messageApi.success(t('card.unarchived', { title: task.title })))
        .catch((err: unknown) =>
          messageApi.error(
            err instanceof Error
              ? t('card.unarchiveErrorDetail', { message: err.message })
              : t('card.unarchiveError')
          )
        )
      return
    }
    archiveMutation
      .mutateAsync({ id: task.id, archived: true })
      .then(() => messageApi.success(t('card.archived', { title: task.title })))
      .catch((err: unknown) =>
        messageApi.error(
          err instanceof Error
            ? t('card.archiveErrorDetail', { message: err.message })
            : t('card.archiveError')
        )
      )
  }

  return (
    <Card
      size="small"
      hoverable
      tabIndex={0}
      className={`${styles.card}${isArchived ? ` ${styles.archived}` : ''}`}
      onClick={openSession}
      onKeyDown={handleKeyDown}
      role="button"
      aria-label={t('card.aria', { title: task.title, status: statusLabel(task.status) })}
      styles={{ body: { padding: 12 } }}
    >
      {externalMessageApi ? null : localMessageHolder}
      <div className={styles.top}>
        <span className={styles.statusChip} data-status={task.status}>
          {statusLabel(task.status)}
        </span>
        <span className={styles.agentChip}>{task.agent_type}</span>
        {!task.session_id && !isBacklog && (
          <span className={styles.noSessionChip} title={t('card.noSessionHint')}>
            {t('card.noSession')}
          </span>
        )}
        {isBacklog && (
          <span className={styles.noSessionChip} title={t('card.backlogHint')}>
            {t('card.backlog')}
          </span>
        )}
        {isBacklog && (
          <Button
            size="small"
            type="text"
            className={styles.moveButton}
            aria-label={t('card.startAria', { title: task.title })}
            onClick={handleStart}
            loading={startMutation.isPending}
          >
            {t('card.startButton')}
          </Button>
        )}
        {task.status === 'done' && !isArchived && (
          <Button
            size="small"
            type="text"
            className={styles.moveButton}
            aria-label={t('card.archiveAria', { title: task.title })}
            onClick={handleArchiveToggle}
            loading={archiveMutation.isPending}
          >
            {t('card.archiveButton')}
          </Button>
        )}
        {isArchived && (
          <Button
            size="small"
            type="text"
            className={styles.moveButton}
            aria-label={t('card.unarchiveAria', { title: task.title })}
            onClick={handleArchiveToggle}
            loading={archiveMutation.isPending}
          >
            {t('card.unarchiveButton')}
          </Button>
        )}
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
              aria-label={t('card.moveAria', { title: task.title })}
              title={t('card.moveTitle')}
              onClick={(event) => event.stopPropagation()}
            >
              {t('card.moveButton')}
            </Button>
          </Dropdown>
        )}
      </div>
      <p className={styles.title}>{task.title}</p>
      {task.prompt && <p className={styles.prompt}>{task.prompt}</p>}
      <SubtaskDots subtasks={subtasks} />
      <p className={styles.footer}>
        {t('card.updated', { time: formatRelative(task.updated_at) })}
      </p>
    </Card>
  )
}
