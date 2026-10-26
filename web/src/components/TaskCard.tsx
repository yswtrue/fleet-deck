import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Button, Card, Dropdown, message } from 'antd'
import type { MenuProps } from 'antd'
import type { MessageInstance } from 'antd/es/message/interface'
import { SubtaskDots } from './SubtaskDots.tsx'
import { EditTaskModal } from './EditTaskModal.tsx'
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

function Icon({ children }: { children: React.ReactNode }) {
  return (
    <svg
      viewBox="0 0 24 24"
      width="15"
      height="15"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      {children}
    </svg>
  )
}

const StartIcon = () => (
  <Icon>
    <path d="M8 5.5v13l10-6.5z" />
  </Icon>
)

const ArchiveIcon = () => (
  <Icon>
    <rect x="3.5" y="4.5" width="17" height="4" rx="1" />
    <path d="M5 8.5V19a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V8.5M10 13h4" />
  </Icon>
)

const UnarchiveIcon = () => (
  <Icon>
    <rect x="3.5" y="4.5" width="17" height="4" rx="1" />
    <path d="M5 8.5V19a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V8.5M12 16v-5M9.5 13.5 12 11l2.5 2.5" />
  </Icon>
)

const EditIcon = () => (
  <Icon>
    <path d="M4 20h4l10-10a2.1 2.1 0 0 0-3-3L5 17v3z" />
    <path d="M13.5 7.5l3 3" />
  </Icon>
)

const MoreIcon = () => (
  <Icon>
    <circle cx="5" cy="12" r="1" />
    <circle cx="12" cy="12" r="1" />
    <circle cx="19" cy="12" r="1" />
  </Icon>
)

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
  const [editing, setEditing] = useState(false)
  // Waiting tasks act through Start (dispatch) and Archive (shelve); the hub
  // rejects any manual status move from waiting.
  const canStart = task.status === 'waiting' && !task.session_id
  // The hub accepts content edits only before dispatch; a session means the
  // agent already holds the prompt.
  const canEdit = !task.session_id
  const canArchive =
    (task.status === 'done' || task.status === 'failed' || task.status === 'waiting') && !isArchived

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
    <>
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
          <div className={styles.chips}>
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
          </div>
          <span className={styles.actions}>
            {canEdit && (
              <Button
                size="small"
                type="text"
                className={styles.iconButton}
                aria-label={t('card.editAria', { title: task.title })}
                title={t('card.editButton')}
                onClick={(event) => {
                  event.stopPropagation()
                  setEditing(true)
                }}
                icon={<EditIcon />}
              />
            )}
            {canStart && (
              <Button
                size="small"
                type="text"
                className={styles.iconButton}
                aria-label={t('card.startAria', { title: task.title })}
                title={t('card.startButton')}
                onClick={handleStart}
                loading={startMutation.isPending}
                icon={<StartIcon />}
              />
            )}
            {canArchive && (
              <Button
                size="small"
                type="text"
                className={styles.iconButton}
                aria-label={t('card.archiveAria', { title: task.title })}
                title={t('card.archiveButton')}
                onClick={handleArchiveToggle}
                loading={archiveMutation.isPending}
                icon={<ArchiveIcon />}
              />
            )}
            {isArchived && (
              <Button
                size="small"
                type="text"
                className={styles.iconButton}
                aria-label={t('card.unarchiveAria', { title: task.title })}
                title={t('card.unarchiveButton')}
                onClick={handleArchiveToggle}
                loading={archiveMutation.isPending}
                icon={<UnarchiveIcon />}
              />
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
                  className={styles.iconButton}
                  aria-label={t('card.moveAria', { title: task.title })}
                  title={t('card.moveTitle')}
                  onClick={(event) => event.stopPropagation()}
                  icon={<MoreIcon />}
                />
              </Dropdown>
            )}
          </span>
        </div>
        <p className={styles.title}>{task.title}</p>
        {task.prompt && <p className={styles.prompt}>{task.prompt}</p>}
        <div className={styles.footer}>
          <SubtaskDots subtasks={subtasks} />
          <span>{t('card.updated', { time: formatRelative(task.updated_at) })}</span>
        </div>
      </Card>
      {editing && (
        <EditTaskModal task={task} messageApi={messageApi} onClose={() => setEditing(false)} />
      )}
    </>
  )
}
