import { useCallback, useEffect, useRef, useState } from 'react'
import { useNavigate, useLocation } from 'react-router-dom'
import { useOne } from '@refinedev/core'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Button, Input, Typography, message } from 'antd'
import { MessageBubble } from './MessageBubble.tsx'
import { SessionUsage } from './SessionUsage.tsx'
import { PermissionPicker } from './PermissionPicker.tsx'
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
import styles from './SessionView.module.css'
import type { SessionSnapshot } from '../apiClient.ts'
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

export function SessionView({ sessionId, task }: SessionViewProps) {
  const { t } = useTranslation('session')
  const { messages, delta, isLoading, error, sessionStatus, pendingPermission, send } =
    useSession(sessionId)
  // The static antd message API renders nothing under React 19; the hook-based
  // API is the one that actually mounts toasts.
  const [messageApi, messageHolder] = message.useMessage()
  const [reply, setReply] = useState('')
  // Auto-scroll follows the stream only while the reader is pinned to the
  // bottom; scrolling up pauses it until "Jump to latest".
  const [atBottom, setAtBottom] = useState(true)
  const scrollRef = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLTextAreaElement>(null)
  const navigate = useNavigate()
  const location = useLocation()
  const api = useHubApi()
  const queryClient = useQueryClient()
  const taskFromState = (location.state as { task?: Task } | null)?.task
  // Direct URLs carry no location.state task. Fetch the session snapshot so
  // the header shows the task title (Refine getOne('sessions') maps to
  // GET /api/sessions/{id}, which returns the owning task). useOne exposes the
  // unwrapped record as `result`, not react-query's `data`.
  const { result: snapshot } = useOne<SessionSnapshot>({ resource: 'sessions', id: sessionId })
  const resolvedTask = task ?? taskFromState ?? snapshot?.task ?? undefined
  const session = snapshot?.session

  // Gate the composer on the session's own state: `awaiting_user` (reply to
  // the agent's question) and `done`/`failed` (continue the conversation —
  // the hub reopens the session and surfaces the new state over WS). The task
  // status is only a fallback for the first paint, before the snapshot/WS
  // state is known (it can lag a resumed session).
  const sessionState = sessionStatus ?? session?.status
  const isAwaiting = sessionState
    ? sessionState === 'awaiting_user'
    : resolvedTask?.status === 'awaiting_user'
  const isContinuable = sessionState
    ? sessionState === 'done' || sessionState === 'failed'
    : resolvedTask?.status === 'done'
  const canSend = isAwaiting || isContinuable

  // Sidebar data: the task's mirrored todos (subtasks), kept fresh over WS.
  const taskId = resolvedTask?.id ?? ''
  const subtasksQuery = useQuery({
    queryKey: ['subtasks', taskId],
    queryFn: () => api.listSubtasks(taskId),
    enabled: !!taskId,
  })
  const handleSubtaskEvent = useCallback(
    (event: ProtocolEvent) => {
      const evt = dispatchEvent(event)
      if (evt?.type === 'subtask.updated' && evt.data.task_id === taskId) {
        void queryClient.invalidateQueries({ queryKey: ['subtasks', taskId] })
      }
    },
    [taskId, queryClient]
  )
  const { connectionState } = useWebSocket(handleSubtaskEvent)

  // Session meta: resolve the node name from the shared nodes list (the hub
  // has no GET /api/nodes/{id}; `getNode` 404s into the SPA fallback).
  const nodeId = session?.node_id ?? ''
  const nodesQuery = useQuery({ queryKey: ['nodes'], queryFn: () => api.listNodes() })
  const nodeName = nodesQuery.data?.find((n) => n.id === nodeId)?.name

  const currentStatus = sessionStatus ?? session?.status
  const agentType = session?.agent_type ?? resolvedTask?.agent_type

  useEffect(() => {
    const el = scrollRef.current
    if (!el || !atBottom) return
    el.scrollTop = el.scrollHeight
  }, [messages, delta, atBottom])

  useEffect(() => {
    if (canSend && inputRef.current) {
      inputRef.current.focus()
    }
  }, [canSend])

  const handleScroll = () => {
    const el = scrollRef.current
    if (!el) return
    setAtBottom(el.scrollHeight - el.scrollTop - el.clientHeight < 48)
  }

  const jumpToLatest = () => {
    const el = scrollRef.current
    if (!el) return
    el.scrollTo({ top: el.scrollHeight, behavior: 'smooth' })
    setAtBottom(true)
  }

  const handleSend = async (event?: React.FormEvent) => {
    event?.preventDefault()
    if (!reply.trim() || !canSend) return
    try {
      await send(reply)
    } catch (err) {
      messageApi.warning(
        err instanceof Error
          ? t('composer.sendErrorDetail', { message: err.message })
          : t('composer.sendError')
      )
      return
    }
    setReply('')
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

      <div className={styles.panes}>
        <section className={styles.transcriptPane}>
          <div ref={scrollRef} tabIndex={-1} className={styles.transcript} onScroll={handleScroll}>
            {messages.length === 0 && !delta ? (
              <EmptyState title={t('transcript.empty')} />
            ) : (
              messages.map((m) => <MessageBubble key={m.id} message={m} />)
            )}
            {delta && (
              <div className={styles.streaming} aria-live="polite">
                <span className={styles.streamLabel}>{t('role.assistant')}</span>
                <p className={styles.streamText}>
                  {delta}
                  <span className={styles.cursor} aria-hidden="true" />
                </p>
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
            {pendingPermission && isAwaiting ? (
              <PermissionPicker
                request={pendingPermission}
                onPick={(optionId) => {
                  void send(formatPermissionReply(optionId))
                }}
              />
            ) : (
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
                      : isContinuable
                        ? t('composer.continuePlaceholder')
                        : t('composer.waiting')
                  }
                  disabled={!canSend}
                  rows={3}
                  className={styles.replyInput}
                />
                <Button type="primary" htmlType="submit" disabled={!canSend || !reply.trim()}>
                  {t('composer.send')}
                </Button>
              </form>
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
