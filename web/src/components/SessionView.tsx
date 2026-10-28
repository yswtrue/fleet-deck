import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { useNavigate, useLocation } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Alert, Button, Checkbox, Input, Popconfirm, Typography, message } from 'antd'
import { MessageBubble } from './MessageBubble.tsx'
import { Markdown } from './Markdown.tsx'
import { SessionUsage } from './SessionUsage.tsx'
import { PermissionPicker } from './PermissionPicker.tsx'
import { ElicitationForm } from './ElicitationForm.tsx'
import { useSession } from '../hooks/useSession.ts'
import { useWebSocket } from '../hooks/useWebSocket.ts'
import { useHubApi } from '../hooks/useHubApi.ts'
import { dispatchEvent } from '../hooks/dispatchEvent.ts'
import { LoadingSkeleton } from './LoadingSkeleton.tsx'
import { ErrorState } from './ErrorState.tsx'
import { EmptyState } from './EmptyState.tsx'
import { statusLabel } from '../util.ts'
import { useTranslation } from '../i18n/index.ts'
import { formatPermissionReply } from '../permission.ts'
import { formatElicitationReply } from '../elicitation.ts'
import styles from './SessionView.module.css'
import type { Event as ProtocolEvent } from '../gen/protocol/index.ts'
import type { Subtask, Task } from '../types.ts'

const SUBTASK_LABEL_KEYS: Record<Subtask['status'], string> = {
  pending: 'subtask.pending',
  in_progress: 'subtask.inProgress',
  done: 'subtask.done',
}

interface SessionViewProps {
  sessionId: string
  task?: Task
}

/**
 * Rows rendered at the tail of the transcript. A long task can accumulate
 * thousands of rows (every tool call is one), and parsing markdown for all of
 * them synchronously on open — and reconciling them on every streamed delta —
 * is what made long sessions slow. The window keeps the rendered set bounded;
 * older rows load on demand, and new rows slide it forward while the reader
 * stays pinned to the bottom.
 */
const TRANSCRIPT_PAGE = 100

export function SessionView({ sessionId, task }: SessionViewProps) {
  const { t } = useTranslation('session')
  const {
    messages,
    delta,
    isLoading,
    error,
    sessionStatus,
    failureReason,
    pendingPermission,
    pendingElicitation,
    thought,
    send,
    cancel,
  } = useSession(sessionId)
  // The static antd message API renders nothing under React 19; the hook-based
  // API is the one that actually mounts toasts.
  const [messageApi, messageHolder] = message.useMessage()
  const [reply, setReply] = useState('')
  // Auto-scroll follows the stream only while the reader is pinned to the
  // bottom; scrolling up pauses it until "Jump to latest".
  const [atBottom, setAtBottom] = useState(true)
  // frozenStart pins the rendered window after the reader scrolls away from
  // the tail (null = follow the tail). Without it, a long turn's arrivals would
  // slide rows out of the DOM while they are being read.
  const [frozenStart, setFrozenStart] = useState<number | null>(null)
  // Distance from the scroll bottom, captured before older rows are prepended
  // so the row the reader is on stays put.
  const restoreFromBottomRef = useRef<number | null>(null)
  // One reply in flight at a time: a double click must not send two answers.
  const [sending, setSending] = useState(false)
  // While a turn is running, the operator can either steer (interrupt it) or
  // queue the message for after it finishes.
  const [queueAfterTurn, setQueueAfterTurn] = useState(false)
  // stopping holds the stop button in its loading state until the node's
  // terminal report arrives (the button disappears with the state change).
  const [stopping, setStopping] = useState(false)
  const scrollRef = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLTextAreaElement>(null)
  const navigate = useNavigate()
  const location = useLocation()
  const api = useHubApi()
  const queryClient = useQueryClient()
  const taskFromState = (location.state as { task?: Task } | null)?.task
  // Direct URLs carry no location.state task. Fetch the session snapshot so
  // the header shows the task title (GET /api/sessions/{id} returns the owning
  // task). Header resolves the breadcrumb parents from the same query key, so
  // both share one request.
  const snapshotQuery = useQuery({
    queryKey: ['session', sessionId],
    queryFn: () => api.getSession(sessionId),
    enabled: !!sessionId,
  })
  const snapshot = snapshotQuery.data
  const refetchSnapshot = snapshotQuery.refetch
  const resolvedTask = task ?? taskFromState ?? snapshot?.task ?? undefined
  const session = snapshot?.session

  // Gate the composer on the session's own state: `awaiting_user` (reply to
  // the agent's question), `running` (steer: the node interrupts the turn and
  // prompts again) and `done`/`failed` (continue the conversation — the hub
  // reopens the session and surfaces the new state over WS). The task status is
  // only a fallback for the first paint, before the snapshot/WS state is known
  // (it can lag a resumed session).
  const sessionState = sessionStatus ?? session?.status
  const isAwaiting = sessionState
    ? sessionState === 'awaiting_user'
    : resolvedTask?.status === 'awaiting_user'
  const isRunning = sessionState ? sessionState === 'running' : resolvedTask?.status === 'running'
  const isContinuable = sessionState
    ? sessionState === 'done' || sessionState === 'failed'
    : resolvedTask?.status === 'done'
  const canSend = isAwaiting || isRunning || isContinuable
  // Stopping is meaningful while the agent holds a turn (or is paused on a
  // question); finished sessions are continued, not stopped.
  const canStop = isAwaiting || isRunning

  // Sidebar data: the task's mirrored todos (subtasks), kept fresh over WS.
  const taskId = resolvedTask?.id ?? ''
  const subtasksQuery = useQuery({
    queryKey: ['subtasks', taskId],
    queryFn: () => api.listSubtasks(taskId),
    enabled: !!taskId,
  })
  const handleSessionEvent = useCallback(
    (event: ProtocolEvent) => {
      const evt = dispatchEvent(event)
      if (!evt || !taskId) return
      if (evt.type === 'subtask.updated' && evt.data.task_id === taskId) {
        // Apply the row instead of invalidating: todo snapshots arrive in
        // bursts, and an invalidate that lands while a refetch is in flight is
        // coalesced, leaving the last status unapplied until the next event.
        const { subtask, removed } = evt.data
        queryClient.setQueryData<Subtask[]>(['subtasks', taskId], (prev) => {
          const list = prev ?? []
          if (removed) return list.filter((s) => s.id !== subtask.id)
          const idx = list.findIndex((s) => s.id === subtask.id)
          const next = idx < 0 ? [...list, subtask] : list.map((s, i) => (i === idx ? subtask : s))
          return next.sort((a, b) => a.sort_order - b.sort_order || a.id.localeCompare(b.id))
        })
        // A fetch that started before this frame can still resolve with the
        // pre-update rows and overwrite them. Refetching (which cancels the
        // in-flight request) keeps the list converging on the server's last
        // write; when nothing is in flight the applied row is already exact.
        if (queryClient.isFetching({ queryKey: ['subtasks', taskId] }) > 0) {
          void queryClient.invalidateQueries({ queryKey: ['subtasks', taskId] })
        }
      }
      // Subtask frames are transient. A state transition is a cheap point to
      // re-read the mirrored list, healing a frame dropped under load.
      if (evt.type === 'session.state_changed' && evt.data.session_id === sessionId) {
        void queryClient.invalidateQueries({ queryKey: ['subtasks', taskId] })
      }
      if (evt.type === 'task.title_changed' && evt.data.task_id === taskId) {
        // The session snapshot carries the owning task; refetch it so the
        // header shows the agent-assigned title.
        void refetchSnapshot()
      }
    },
    [taskId, sessionId, queryClient, refetchSnapshot]
  )
  const { connectionState } = useWebSocket(handleSessionEvent)

  // A reconnect may have missed subtask frames (the hub drops events for a
  // subscriber that falls behind), so re-read the list once the socket is back.
  useEffect(() => {
    if (connectionState === 'open' && taskId) {
      void queryClient.invalidateQueries({ queryKey: ['subtasks', taskId] })
    }
  }, [connectionState, taskId, queryClient])

  // Session meta: resolve the node name from the shared nodes list (the hub
  // has no GET /api/nodes/{id}; `getNode` 404s into the SPA fallback).
  const nodeId = session?.node_id ?? ''
  const nodesQuery = useQuery({ queryKey: ['nodes'], queryFn: () => api.listNodes() })
  const nodeName = nodesQuery.data?.find((n) => n.id === nodeId)?.name

  const currentStatus = sessionStatus ?? session?.status
  const agentType = session?.agent_type ?? resolvedTask?.agent_type
  // The live event carries the reason; the snapshot is the reload fallback.
  const failureText =
    currentStatus === 'failed' ? failureReason || session?.error || t('failure.unknown') : ''

  // Render only the tail of the transcript; the whole list stays in the query
  // cache so "show earlier" costs no request. `windowStart` is derived, not
  // stored, while following the tail: new rows therefore slide the window
  // forward without growing the rendered set.
  const tailStart = Math.max(0, messages.length - TRANSCRIPT_PAGE)
  const windowStart = frozenStart === null ? tailStart : Math.min(frozenStart, tailStart)
  const visibleMessages = windowStart > 0 ? messages.slice(windowStart) : messages

  useEffect(() => {
    const el = scrollRef.current
    if (!el || !atBottom) return
    el.scrollTop = el.scrollHeight
  }, [messages, delta, atBottom])

  // Older rows were prepended: hold the reader's distance from the bottom so
  // the content under their eyes does not move.
  useLayoutEffect(() => {
    const el = scrollRef.current
    const fromBottom = restoreFromBottomRef.current
    if (!el || fromBottom === null) return
    restoreFromBottomRef.current = null
    el.scrollTop = el.scrollHeight - fromBottom
  }, [windowStart])

  useEffect(() => {
    if (canSend && inputRef.current) {
      inputRef.current.focus()
    }
  }, [canSend])

  const handleScroll = () => {
    const el = scrollRef.current
    if (!el) return
    const nextAtBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 48
    setAtBottom(nextAtBottom)
    // Freeze the window while the reader is away from the tail so arrivals
    // cannot unmount the rows being read; follow the tail again at the bottom.
    if (!nextAtBottom && frozenStart === null) setFrozenStart(windowStart)
    if (nextAtBottom && frozenStart !== null) setFrozenStart(null)
  }

  const loadEarlier = () => {
    const el = scrollRef.current
    if (el) restoreFromBottomRef.current = el.scrollHeight - el.scrollTop
    setFrozenStart(Math.max(0, windowStart - TRANSCRIPT_PAGE))
  }

  const jumpToLatest = () => {
    const el = scrollRef.current
    if (!el) return
    el.scrollTo({ top: el.scrollHeight, behavior: 'smooth' })
    setAtBottom(true)
    setFrozenStart(null)
  }

  const handleSend = async (event?: React.FormEvent) => {
    event?.preventDefault()
    if (!reply.trim() || !canSend) return
    const queued = isRunning && queueAfterTurn
    try {
      await send(reply, queued ? { queue: true } : undefined)
    } catch (err) {
      messageApi.warning(
        err instanceof Error
          ? t('composer.sendErrorDetail', { message: err.message })
          : t('composer.sendError')
      )
      return
    }
    setReply('')
    if (queued) messageApi.success(t('composer.queuedToast'))
  }

  const handleStop = async () => {
    setStopping(true)
    try {
      await cancel()
    } catch (err) {
      messageApi.warning(
        err instanceof Error ? t('stop.errorDetail', { message: err.message }) : t('stop.error')
      )
      setStopping(false)
    }
    // On success the button stays busy until the session's failed state event
    // unmounts it; clearing here would flash an enabled Stop on a dead session.
  }

  const goBack = () => {
    if (resolvedTask) {
      navigate(`/workspaces/${resolvedTask.workspace_id}`)
    } else {
      navigate(-1)
    }
  }

  if (isLoading) return <LoadingSkeleton count={2} />
  if (error) return <ErrorState title={t('error.load')}>{error.message}</ErrorState>

  return (
    <div className={styles.page}>
      {messageHolder}
      <header className={styles.header}>
        <Button onClick={goBack}>{t('common:actions.back')}</Button>
        {resolvedTask ? (
          <div className={styles.headerTitle}>
            <Typography.Title level={4} style={{ margin: 0 }}>
              {resolvedTask.title}
            </Typography.Title>
            <div className={styles.headerTags}>
              {currentStatus && (
                <span className={styles.statusChip} data-status={currentStatus}>
                  {statusLabel(currentStatus)}
                </span>
              )}
              {agentType && <span className={styles.agentChip}>{agentType}</span>}
            </div>
          </div>
        ) : (
          <Typography.Title level={4} style={{ margin: 0 }}>
            {t('sessionTitle', { id: sessionId.slice(0, 8) })}
          </Typography.Title>
        )}
        {canStop && (
          <Popconfirm
            title={t('stop.confirm')}
            okText={t('common:actions.stop')}
            cancelText={t('common:actions.cancel')}
            okButtonProps={{ danger: true }}
            onConfirm={() => void handleStop()}
          >
            <Button danger loading={stopping}>
              {t('common:actions.stop')}
            </Button>
          </Popconfirm>
        )}
        {connectionState !== 'open' && (
          <span className={styles.reconnect} data-state={connectionState} role="status">
            {connectionState === 'connecting'
              ? t('connection.connecting')
              : connectionState === 'failed'
                ? t('connection.failed')
                : t('connection.reconnecting')}
          </span>
        )}
      </header>

      {failureText && (
        <Alert
          className={styles.failureBanner}
          type="error"
          showIcon
          message={t('failure.title')}
          description={failureText}
        />
      )}

      <div className={styles.panes}>
        <section className={styles.transcriptPane}>
          <div ref={scrollRef} tabIndex={-1} className={styles.transcript} onScroll={handleScroll}>
            {messages.length === 0 && !delta ? (
              <EmptyState title={t('transcript.empty')} />
            ) : (
              <>
                {windowStart > 0 && (
                  <div className={styles.earlier}>
                    <Button size="small" onClick={loadEarlier}>
                      {t('transcript.showEarlier')}
                    </Button>
                  </div>
                )}
                {visibleMessages.map((m) => (
                  <MessageBubble key={m.id} message={m} />
                ))}
              </>
            )}
            {thought && (
              <div className={styles.thinking} aria-live="polite">
                <span className={styles.streamLabel}>{t('transcript.thinking')}</span>
                <p className={styles.streamText}>{thought}</p>
              </div>
            )}
            {delta && (
              <div className={styles.streaming} aria-live="polite">
                <span className={styles.streamLabel}>{t('role.assistant')}</span>
                <Markdown content={delta} className={styles.streamText} streaming />
              </div>
            )}
          </div>
          {!atBottom && (
            <div className={styles.jumpWrap}>
              <Button size="small" onClick={jumpToLatest}>
                {t('transcript.jumpToLatest')}
              </Button>
            </div>
          )}
          <div className={styles.composer}>
            {pendingElicitation && isAwaiting ? (
              <ElicitationForm
                key={`${pendingElicitation.message}:${pendingElicitation.fields
                  .map((field) => field.name)
                  .join(',')}`}
                request={pendingElicitation}
                onSubmit={(action, content) => {
                  setSending(true)
                  send(formatElicitationReply(action, content))
                    .catch((err: unknown) => {
                      messageApi.warning(
                        err instanceof Error
                          ? t('composer.sendErrorDetail', { message: err.message })
                          : t('composer.sendError')
                      )
                    })
                    .finally(() => setSending(false))
                }}
              />
            ) : pendingPermission && isAwaiting ? (
              <PermissionPicker
                request={pendingPermission}
                busy={sending}
                onPick={(optionId) => {
                  setSending(true)
                  send(formatPermissionReply(optionId))
                    .catch((err: unknown) => {
                      messageApi.warning(
                        err instanceof Error
                          ? t('composer.sendErrorDetail', { message: err.message })
                          : t('composer.sendError')
                      )
                    })
                    .finally(() => setSending(false))
                }}
              />
            ) : (
              <>
                <form className={styles.replyForm} onSubmit={handleSend}>
                  <Input.TextArea
                    ref={inputRef}
                    value={reply}
                    onChange={(e) => setReply(e.target.value)}
                    onKeyDown={(event) => {
                      if (event.key === 'Enter' && !event.shiftKey) {
                        event.preventDefault()
                        void handleSend()
                      }
                    }}
                    placeholder={
                      isAwaiting
                        ? t('composer.placeholder')
                        : isRunning
                          ? queueAfterTurn
                            ? t('composer.queuePlaceholder')
                            : t('composer.steerPlaceholder')
                          : isContinuable
                            ? t('composer.continuePlaceholder')
                            : t('composer.waiting')
                    }
                    disabled={!canSend || stopping}
                    rows={3}
                    className={styles.replyInput}
                  />
                  <Button
                    type="primary"
                    htmlType="submit"
                    disabled={!canSend || stopping || !reply.trim()}
                  >
                    {t('composer.send')}
                  </Button>
                </form>
                {isRunning && (
                  <div className={styles.queueRow}>
                    <Checkbox
                      checked={queueAfterTurn}
                      onChange={(event) => setQueueAfterTurn(event.target.checked)}
                    >
                      {t('composer.queueToggle')}
                    </Checkbox>
                    <p className={styles.composerHint}>
                      {queueAfterTurn ? t('composer.queueHint') : t('composer.steerHint')}
                    </p>
                  </div>
                )}
              </>
            )}
          </div>
        </section>

        <aside className={styles.sidebar}>
          <section className={styles.panel}>
            <h3 className={styles.panelTitle}>{t('panel.task')}</h3>
            <p className={styles.taskTitle}>
              {resolvedTask?.title ?? t('sessionTitle', { id: sessionId.slice(0, 8) })}
            </p>
            {resolvedTask?.prompt && <p className={styles.taskPrompt}>{resolvedTask.prompt}</p>}
          </section>
          <section className={styles.panel}>
            <h3 className={styles.panelTitle}>{t('panel.subtasks')}</h3>
            {(subtasksQuery.data ?? []).length === 0 ? (
              <p className={styles.muted}>{t('subtask.empty')}</p>
            ) : (
              <ul className={styles.subtasks}>
                {(subtasksQuery.data ?? []).map((subtask) => (
                  <li key={subtask.id} className={styles.subtask}>
                    <span
                      className={styles.subtaskDot}
                      data-status={subtask.status}
                      aria-hidden="true"
                    />
                    <span className={styles.subtaskTitle}>{subtask.title}</span>
                    <span className={styles.subtaskStatus} data-status={subtask.status}>
                      {t(SUBTASK_LABEL_KEYS[subtask.status])}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </section>
          <SessionUsage sessionId={sessionId} />
          <section className={styles.panel}>
            <h3 className={styles.panelTitle}>{t('panel.session')}</h3>
            <dl className={styles.meta}>
              <div className={styles.metaRow}>
                <dt>{t('meta.status')}</dt>
                <dd>
                  {currentStatus ? (
                    <span className={styles.statusChip} data-status={currentStatus}>
                      {statusLabel(currentStatus)}
                    </span>
                  ) : (
                    '—'
                  )}
                </dd>
              </div>
              <div className={styles.metaRow}>
                <dt>{t('meta.agent')}</dt>
                <dd>{agentType ?? '—'}</dd>
              </div>
              <div className={styles.metaRow}>
                <dt>{t('meta.node')}</dt>
                <dd>{nodeName ?? (nodeId ? nodeId.slice(0, 8) : '—')}</dd>
              </div>
            </dl>
          </section>
        </aside>
      </div>
    </div>
  )
}
