import { useCallback, useEffect, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useWebSocket } from './useWebSocket.ts'
import { useHubApi } from './useHubApi.ts'
import { dispatchEvent } from './dispatchEvent.ts'
import type { Event as ProtocolEvent, Task, Subtask } from '../gen/protocol/index.ts'

type SessionStatus = 'waiting' | 'running' | 'done' | 'failed' | 'awaiting_user'

function mapSessionStatus(status: SessionStatus): Task['status'] {
  switch (status) {
    case 'waiting':
      return 'waiting'
    case 'running':
      return 'running'
    case 'awaiting_user':
      return 'awaiting_user'
    default:
      return 'done'
  }
}

export function useTaskBoard(workspaceId: string, q = '') {
  const api = useHubApi()
  const queryClient = useQueryClient()
  const [subtasksByTask, setSubtasksByTask] = useState<Record<string, Subtask[]>>({})

  // The unfiltered board keeps the canonical ['tasks', workspaceId] key (WS
  // handlers and task-creating code target it); an active search runs under an
  // extended key, which still falls under the same invalidation prefix.
  const taskKey = q ? ['tasks', workspaceId, q] : ['tasks', workspaceId]

  const {
    data: tasks,
    isLoading,
    isFetching,
    error,
  } = useQuery({
    queryKey: taskKey,
    queryFn: () => api.listTasks(workspaceId, q || undefined),
    enabled: !!workspaceId,
  })

  const handleEvent = useCallback(
    (event: ProtocolEvent) => {
      const evt = dispatchEvent(event)
      if (!evt) return

      switch (evt.type) {
        case 'task.created': {
          const { task } = evt.data
          if (task.workspace_id === workspaceId) {
            queryClient.setQueryData<Task[]>(['tasks', workspaceId], (prev = []) => {
              if (prev.some((t) => t.id === task.id)) return prev
              return [...prev, task]
            })
          }
          break
        }
        case 'task.status_changed': {
          const { task_id, to } = evt.data
          queryClient.setQueryData<Task[]>(['tasks', workspaceId], (prev = []) =>
            prev.map((t) =>
              t.id === task_id ? { ...t, status: to, updated_at: new Date().toISOString() } : t
            )
          )
          break
        }
        case 'subtask.updated': {
          const { task_id, subtask, removed } = evt.data
          setSubtasksByTask((prev) => {
            const list = prev[task_id] || []
            if (removed) {
              return { ...prev, [task_id]: list.filter((s) => s.id !== subtask.id) }
            }
            const exists = list.some((s) => s.id === subtask.id)
            const next = exists
              ? list.map((s) => (s.id === subtask.id ? subtask : s))
              : [...list, subtask]
            return { ...prev, [task_id]: next }
          })
          break
        }
        case 'session.state_changed': {
          const { session_id, status: sessionStatus } = evt.data
          queryClient.setQueryData<Task[]>(['tasks', workspaceId], (prev = []) => {
            const task = prev.find((t) => t.session_id === session_id)
            if (!task) return prev
            const nextStatus = mapSessionStatus(sessionStatus)
            return prev.map((t) =>
              t.id === task.id
                ? { ...t, status: nextStatus, updated_at: new Date().toISOString() }
                : t
            )
          })
          break
        }
        case 'session.user_input_requested': {
          const { session_id } = evt.data
          queryClient.setQueryData<Task[]>(['tasks', workspaceId], (prev = []) => {
            const task = prev.find((t) => t.session_id === session_id)
            if (!task) return prev
            return prev.map((t) =>
              t.id === task.id
                ? { ...t, status: 'awaiting_user' as const, updated_at: new Date().toISOString() }
                : t
            )
          })
          break
        }
        default:
          break
      }
    },
    [workspaceId, queryClient]
  )

  const { connectionState } = useWebSocket(handleEvent)

  // Board state is not gap-filled from the event stream (task/subtask deltas are
  // not persisted as events). On reconnect, refetch the REST state instead.
  const hasConnectedRef = useRef(false)
  useEffect(() => {
    if (connectionState !== 'open') return
    if (hasConnectedRef.current) {
      void queryClient.invalidateQueries({ queryKey: ['tasks', workspaceId] })
      void queryClient.invalidateQueries({ queryKey: ['nodes'] })
    }
    hasConnectedRef.current = true
  }, [connectionState, queryClient, workspaceId])

  return { tasks: tasks ?? [], subtasksByTask, isLoading, isFetching, error }
}

/**
 * TASK_STATUS_TRANSITIONS lists the hub-legal task status moves. `done` is
 * terminal, so a finished task exposes no actions at all.
 */
export const TASK_STATUS_TRANSITIONS: Record<Task['status'], Task['status'][]> = {
  waiting: ['running', 'awaiting_user', 'done'],
  running: ['awaiting_user', 'done'],
  awaiting_user: ['running', 'done'],
  done: [],
}

/**
 * useTaskStatusMutation moves a task to a new status via PATCH /api/tasks/{id}.
 * The board updates optimistically — every ['tasks', …] list in the cache,
 * canonical and search-filtered alike — then rolls back on error (e.g. the
 * hub's 409 for a stale transition) and refetches the hub truth on settle.
 */
export function useTaskStatusMutation() {
  const api = useHubApi()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ id, status }: { id: string; status: Task['status'] }) =>
      api.patchTask(id, { status }),
    onMutate: async ({ id, status }) => {
      await queryClient.cancelQueries({ queryKey: ['tasks'] })
      const previous = queryClient.getQueriesData<Task[]>({ queryKey: ['tasks'] })
      queryClient.setQueriesData<Task[]>({ queryKey: ['tasks'] }, (prev) =>
        prev
          ? prev.map((t) =>
              t.id === id ? { ...t, status, updated_at: new Date().toISOString() } : t
            )
          : prev
      )
      return { previous }
    },
    onError: (_err, _vars, context) => {
      context?.previous.forEach(([key, data]) => queryClient.setQueryData(key, data))
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ['tasks'] }),
  })
}
