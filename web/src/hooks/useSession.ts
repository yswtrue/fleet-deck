import { useCallback, useRef, useState, useEffect } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useWebSocket } from './useWebSocket.ts'
import { useHubApi } from './useHubApi.ts'
import { dispatchEvent } from './dispatchEvent.ts'
import { parsePermissionRequest } from '../permission.ts'
import type { PermissionRequest } from '../permission.ts'
import type { Event as ProtocolEvent, Message, SessionStateChanged, Task } from '../gen/protocol/index.ts'

/**
 * mergeMessage appends a message to a session's list, deduping two ways:
 * - an existing row with the same id wins (server echo or refetch overlap);
 * - a pending optimistic user message (id `local-*`) with the same content is
 *   replaced by the server's authoritative row, so a reply is not shown twice.
 */
function mergeMessage(prev: Message[], incoming: Message): Message[] {
  if (prev.some((m) => m.id === incoming.id)) return prev
  const withoutOptimisticEcho = prev.filter(
    (m) => !(m.id.startsWith('local-') && m.role === 'user' && m.content === incoming.content)
  )
  return [...withoutOptimisticEcho, incoming]
}

export function useSession(sessionId: string) {
  const api = useHubApi()
  const queryClient = useQueryClient()

  const {
    data: messages,
    isLoading,
    error,
  } = useQuery({
    queryKey: ['messages', sessionId],
    queryFn: () => api.getMessages(sessionId),
    enabled: !!sessionId,
  })

  const [delta, setDelta] = useState('')
  const [sessionStatus, setSessionStatus] = useState<SessionStateChanged['status'] | null>(null)
  // pendingPermission is the structured ACP permission ask awaiting a pick
  // (null for plain free-text questions and once the ask is answered).
  const [pendingPermission, setPendingPermission] = useState<PermissionRequest | null>(null)
  const rafRef = useRef<number | null>(null)
  const pendingRef = useRef('')

  const flush = useCallback(() => {
    setDelta((d) => d + pendingRef.current)
    pendingRef.current = ''
    rafRef.current = null
  }, [])

  const appendDelta = useCallback(
    (text: string) => {
      pendingRef.current += text
      if (rafRef.current === null) {
        rafRef.current = requestAnimationFrame(flush)
      }
    },
    [flush]
  )

  useEffect(() => {
    return () => {
      if (rafRef.current !== null) {
        cancelAnimationFrame(rafRef.current)
      }
    }
  }, [])

  const handleEvent = useCallback(
    (event: ProtocolEvent) => {
      const evt = dispatchEvent(event)
      if (!evt) return

      switch (evt.type) {
        case 'session.delta': {
          const { session_id, text } = evt.data
          if (session_id !== sessionId) return
          appendDelta(text)
          break
        }
        case 'session.state_changed': {
          const { session_id, status: newStatus } = evt.data
          if (session_id !== sessionId) return
          setSessionStatus(newStatus)
          if (newStatus === 'running') {
            setDelta('')
          }
          if (newStatus !== 'awaiting_user') {
            setPendingPermission(null)
          }
          break
        }
        case 'session.user_input_requested': {
          const { session_id, prompt } = evt.data
          if (session_id !== sessionId) return
          setSessionStatus('awaiting_user')
          // A new ask supersedes any previous one; plain questions parse to
          // null and clear a stale picker.
          setPendingPermission(parsePermissionRequest(prompt))
          break
        }
        case 'session.message': {
          const { session_id, message } = evt.data
          if (session_id !== sessionId) return
          queryClient.setQueryData<Message[]>(['messages', sessionId], (prev = []) =>
            mergeMessage(prev, message)
          )
          if (message.role === 'assistant') {
            setDelta('')
          }
          break
        }
        default:
          break
      }
    },
    [sessionId, appendDelta, queryClient]
  )

  const { connectionState } = useWebSocket(handleEvent)

  // useHubApi returns a fresh object each render; keep the latest in a ref so
  // the gap-fill effect depends only on connection transitions.
  const apiRef = useRef(api)
  useEffect(() => {
    apiRef.current = api
  }, [api])

  // Session-scoped state reconstruction whenever the socket opens: the first
  // open (fresh page load) included, so a reload in the middle of an
  // awaiting_user pause — a permission pick in particular — is rebuilt from the
  // stored event stream instead of waiting for the next live frame.
  //
  // Cursor note: the live WS Event.seq is the hub-wide broadcast ordinal, while
  // /api/sessions/{id}/events?after_seq= expects the per-session seq (a locked
  // Gate 2 distinction). Passing the WS seq here would skip the session's whole
  // history, so we re-read the session stream from 0 and rely on idempotent
  // merges: messages dedupe by id and state events are absolute. Transient
  // `session.delta` frames are skipped; the durable assistant message is
  // refetched via the messages query invalidated below.
  useEffect(() => {
    if (connectionState !== 'open') return
    let cancelled = false
    void queryClient.invalidateQueries({ queryKey: ['messages', sessionId] })
    const replay = async () => {
      // Page through to the tail: the pending ask lives in the *last* events,
      // and a single limit=100 read from 0 would miss it in long sessions.
      let after = 0
      for (;;) {
        const batch = await apiRef.current.getEvents(sessionId, after, 100)
        if (cancelled) return
        for (const s of batch) {
          if (s.type === 'session.delta') continue
          handleEvent({ type: s.type, data: s.payload, ts: s.ts, seq: s.seq })
        }
        if (batch.length < 100) break
        after = batch[batch.length - 1].seq
      }
      // The replayed event stream can end on a stale state (e.g. a node-offline
      // cascade writes `failed` after the last stored event). Sync the current
      // truth from REST so the live status wins.
      if (cancelled) return
      try {
        const snapshot = await apiRef.current.getSession(sessionId)
        if (cancelled) return
        setSessionStatus(snapshot.session.status)
        const task = snapshot.task
        if (!task) return
        queryClient.setQueryData<Task[]>(['tasks', task.workspace_id], (prev) => {
          if (!prev) return prev
          return prev.map((t) => (t.id === task.id ? task : t))
        })
      } catch (err: unknown) {
        console.warn('[useSession] session sync failed', err)
      }
    }
    replay().catch((err: unknown) => {
      console.warn('[useSession] gap-fill failed', err)
    })
    return () => {
      cancelled = true
    }
  }, [connectionState, sessionId, handleEvent, queryClient])

  const send = useCallback(
    async (text: string) => {
      const trimmed = text.trim()
      if (!trimmed) return
      // Any reply answers the current ask (the adapter resumes on SendMessage).
      setPendingPermission(null)
      const optimisticID = `local-${Date.now()}`
      queryClient.setQueryData<Message[]>(['messages', sessionId], (prev = []) =>
        mergeMessage(prev, {
          id: optimisticID,
          session_id: sessionId,
          role: 'user',
          content: trimmed,
          created_at: new Date().toISOString(),
        } as Message)
      )
      setDelta('')
      try {
        await api.sendMessage(sessionId, trimmed)
      } catch (err) {
        // The optimistic row never reached the hub (e.g. 409 "session is not
        // awaiting user input"): roll it back and let the caller report why.
        queryClient.setQueryData<Message[]>(['messages', sessionId], (prev = []) =>
          prev.filter((m) => m.id !== optimisticID)
        )
        throw err
      }
    },
    [sessionId, api, queryClient]
  )

  return { messages: messages ?? [], delta, isLoading, error, sessionStatus, pendingPermission, send }
}
