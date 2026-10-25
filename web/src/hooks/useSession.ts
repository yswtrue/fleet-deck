import { useCallback, useRef, useState, useEffect } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useWebSocket } from './useWebSocket.ts'
import { useHubApi } from './useHubApi.ts'
import { dispatchEvent } from './dispatchEvent.ts'
import { parsePermissionRequest } from '../permission.ts'
import { parseElicitationRequest } from '../elicitation.ts'
import type { PermissionRequest } from '../permission.ts'
import type { ElicitationRequest } from '../elicitation.ts'
import type {
  Event as ProtocolEvent,
  Message,
  SessionStateChanged,
  Task,
} from '../gen/protocol/index.ts'

/**
 * mergeMessage appends or updates a message in a session's list:
 * - an existing row with the same id is replaced in place. The node's live
 *   transcript is authoritative; tool calls send the same id again as their
 *   status/title changes, and a repeated assistant echo must not duplicate;
 * - a pending optimistic user message (id `local-*`) with the same content is
 *   replaced by the server's authoritative row, so a reply is not shown twice.
 */
function mergeMessage(prev: Message[] | null | undefined, incoming: Message): Message[] {
  const list = prev ?? []
  const existing = list.findIndex((m) => m.id === incoming.id)
  if (existing >= 0) {
    const next = list.slice()
    next[existing] = incoming
    return next
  }
  const withoutOptimisticEcho = list.filter(
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
  // thought is the agent's live reasoning trace. The node and hub keep a
  // bounded copy for the session snapshot, so reopening the page mid-turn
  // restores it; the agent's own replay provides it for finished sessions.
  const [thought, setThought] = useState('')
  const [sessionStatus, setSessionStatus] = useState<SessionStateChanged['status'] | null>(null)
  // failureReason is why the session failed (adapter exit, lost node process).
  // It arrives on the failed state event and in the session snapshot, so a
  // reload still explains why the task stopped; cleared when it runs again.
  const [failureReason, setFailureReason] = useState('')
  // pendingPermission is the structured ACP permission ask awaiting a pick
  // (null for plain free-text questions and once the ask is answered).
  const [pendingPermission, setPendingPermission] = useState<PermissionRequest | null>(null)
  // pendingElicitation is the structured ACP form ask awaiting submit/decline.
  const [pendingElicitation, setPendingElicitation] = useState<ElicitationRequest | null>(null)
  const rafRef = useRef<number | null>(null)
  const pendingRef = useRef('')
  const thoughtRafRef = useRef<number | null>(null)
  const pendingThoughtRef = useRef('')

  const flush = useCallback(() => {
    setDelta((d) => d + pendingRef.current)
    pendingRef.current = ''
    rafRef.current = null
  }, [])

  const flushThought = useCallback(() => {
    // Bound the live trace: a long reasoning stream would otherwise keep
    // growing the DOM for the whole turn.
    setThought((t) => (t + pendingThoughtRef.current).slice(-4000))
    pendingThoughtRef.current = ''
    thoughtRafRef.current = null
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

  const appendThought = useCallback(
    (text: string) => {
      pendingThoughtRef.current += text
      if (thoughtRafRef.current === null) {
        thoughtRafRef.current = requestAnimationFrame(flushThought)
      }
    },
    [flushThought]
  )

  // discardDelta drops both the painted stream and any chunks still waiting
  // for the next animation frame. Without cancelling the queued frame, a
  // terminal state or complete message clears the text and the pending flush
  // immediately resurrects it — the stale-chunk bug that made a finished turn
  // bleed into the next one.
  const discardDelta = useCallback(() => {
    if (rafRef.current !== null) {
      cancelAnimationFrame(rafRef.current)
      rafRef.current = null
    }
    pendingRef.current = ''
    setDelta('')
  }, [])

  const discardThought = useCallback(() => {
    if (thoughtRafRef.current !== null) {
      cancelAnimationFrame(thoughtRafRef.current)
      thoughtRafRef.current = null
    }
    pendingThoughtRef.current = ''
    setThought('')
  }, [])

  useEffect(() => {
    return () => {
      if (rafRef.current !== null) cancelAnimationFrame(rafRef.current)
      if (thoughtRafRef.current !== null) cancelAnimationFrame(thoughtRafRef.current)
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
        case 'session.thought': {
          const { session_id, text } = evt.data
          if (session_id !== sessionId) return
          appendThought(text)
          break
        }
        case 'session.state_changed': {
          const { session_id, status: newStatus, error: reason } = evt.data
          if (session_id !== sessionId) return
          setSessionStatus(newStatus)
          setFailureReason(newStatus === 'failed' ? (reason ?? '') : '')
          if (newStatus !== 'awaiting_user') {
            setPendingPermission(null)
            setPendingElicitation(null)
          }
          // A pause (awaiting_user) is mid-turn: keep the trace. It is cleared
          // when the turn ends or its answer arrives.
          if (newStatus === 'done' || newStatus === 'failed') {
            discardThought()
            // A terminal turn discards any in-progress stream (a successful
            // turn already replaced it with the complete assistant message).
            discardDelta()
          }
          break
        }
        case 'session.user_input_requested': {
          const { session_id, prompt } = evt.data
          if (session_id !== sessionId) return
          setSessionStatus('awaiting_user')
          // A new ask supersedes any previous one; a permission prompt is
          // parsed first, everything else may carry a form elicitation. Plain
          // questions parse to null and clear both structured panels.
          const permission = parsePermissionRequest(prompt)
          setPendingPermission(permission)
          setPendingElicitation(permission ? null : parseElicitationRequest(prompt))
          break
        }
        case 'session.message': {
          const { session_id, message } = evt.data
          if (session_id !== sessionId) return
          queryClient.setQueryData<Message[]>(['messages', sessionId], (prev) =>
            mergeMessage(prev, message)
          )
          if (message.role === 'assistant') {
            // A queued rAF must be cancelled too, or its chunk lands after
            // the completed message and resurrects the stream.
            discardDelta()
            // The turn's answer supersedes its reasoning trace.
            discardThought()
          }
          break
        }
        case 'session.usage': {
          const { session_id } = evt.data
          if (session_id !== sessionId) return
          // Usage arrives once per turn (after the prompt result). Refresh the
          // session sidebar plus any global stats view; the cache is keyed by
          // ['stats-tokens', ...] in all three consumers.
          void queryClient.invalidateQueries({ queryKey: ['stats-tokens'] })
          break
        }
        default:
          break
      }
    },
    [sessionId, appendDelta, appendThought, discardDelta, discardThought, queryClient]
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
    // Usage reports may have landed while the socket was down; the event
    // replay below is cheap and the aggregate endpoints are authoritative.
    void queryClient.invalidateQueries({ queryKey: ['stats-tokens'] })
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
        setFailureReason(snapshot.session.status === 'failed' ? (snapshot.session.error ?? '') : '')
        // Streaming text lives only in the hub's buffer. Restore it after the
        // event replay so a refresh mid-turn keeps the output produced so far;
        // a live delta that outgrew the snapshot wins (it includes chunks the
        // snapshot request may have raced).
        const partial = snapshot.partial_output ?? ''
        if (partial) {
          setDelta((current) => (partial.length >= current.length ? partial : current))
        }
        // Reasoning is live-only over WS, but the hub/node keep a bounded copy
        // for the snapshot so reopening the page mid-turn does not blank the
        // trace. A live chunk that outgrew the snapshot wins.
        const partialThought = snapshot.partial_thought ?? ''
        if (partialThought) {
          setThought((current) =>
            partialThought.length >= current.length ? partialThought : current
          )
        }
        // The pending ask's prompt is content: it lives in the hub's ephemeral
        // buffer (not the event stream, which stores only its occurrence), so
        // a reload mid-pause restores the picker from the snapshot.
        const pending = snapshot.pending_input
        const permission = pending ? parsePermissionRequest(pending) : null
        setPendingPermission(permission)
        setPendingElicitation(pending && !permission ? parseElicitationRequest(pending) : null)
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
      // The ask stays visible until the hub accepts the reply: clearing it here
      // would hide the picker on a 4xx and leave the operator with no feedback.
      // A successful send flips the session to `running`, whose state event
      // clears both panels.
      const optimisticID = `local-${Date.now()}`
      queryClient.setQueryData<Message[]>(['messages', sessionId], (prev) =>
        mergeMessage(prev, {
          id: optimisticID,
          session_id: sessionId,
          role: 'user',
          content: trimmed,
          created_at: new Date().toISOString(),
        } as Message)
      )
      // A reply to an awaiting_user pause resumes the same turn: keep the
      // pre-ask streamed text (the hub keeps its buffer copy too). A resumed
      // finished session starts with an empty delta (terminal state cleared it).
      try {
        await api.sendMessage(sessionId, trimmed)
      } catch (err) {
        // The optimistic row never reached the hub (e.g. 409 "session is not
        // awaiting user input"): roll it back and let the caller report why.
        queryClient.setQueryData<Message[]>(['messages', sessionId], (prev) =>
          (prev ?? []).filter((m) => m.id !== optimisticID)
        )
        throw err
      }
    },
    [sessionId, api, queryClient]
  )

  return {
    messages: messages ?? [],
    delta,
    thought,
    isLoading,
    error,
    sessionStatus,
    failureReason,
    pendingPermission,
    pendingElicitation,
    send,
  }
}
