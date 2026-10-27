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

/**
 * Task lists are ordered by `updated_at` DESC on the hub. A live patch must
 * therefore move the touched card to the front as well, or a status/rename
 * event would leave it at its old position until the next REST read.
 */
function patchTaskInList(list: Task[], id: string, patch: Partial<Task>): Task[] {
  const task = list.find((t) => t.id === id)
  if (!task) return list
  const updated = { ...task, ...patch, updated_at: new Date().toISOString() }
  return [updated, ...list.filter((t) => t.id !== id)]
}

export function useTaskBoard(workspaceId: string, q = '', archived = false) {
  const api = useHubApi()
  const queryClient = useQueryClient()
  const [subtasksByTask, setSubtasksByTask] = useState<Record<string, Subtask[]>>({})

  // The unfiltered board keeps the canonical ['tasks', workspaceId] key (WS
  // handlers and task-creating code target it); an active search runs under an
  // extended key, which still falls under the same invalidation prefix. The
  // archived view gets its own suffix so toggling the filter refetches instead
  // of reusing the active-task cache.
  const taskKey = [
    ...(['tasks', workspaceId] as const),
    ...(q ? [q] : []),
    ...(archived ? ['archived'] : []),
  ]

  const {
    data: tasks,
    isLoading,
    isFetching,
    error,
  } = useQuery({
    queryKey: taskKey,
    queryFn: () => api.listTasks(workspaceId, q || undefined, { archived }),
    enabled: !!workspaceId,
    // Board state is not gap-filled from the event stream (task deltas are not
    // persisted) and this hook only listens while the board is mounted, so any
    // transition that lands while the operator is on a session page (or in
    // another tab) would leave the cached list stale. Remounting must therefore
    // always re-read the REST truth, independent of any query staleTime.
    refetchOnMount: 'always',
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
              // Newest first, matching the hub's updated_at DESC ordering.
              return [task, ...prev]
            })
          }
          break
        }
        case 'task.status_changed': {
          const { task_id, to } = evt.data
          // Patch every board cache under this workspace (canonical and
          // search-filtered): a filtered board renders its own query key, so
          // writing only the canonical entry would leave visible cards stale.
          queryClient.setQueriesData<Task[]>({ queryKey: ['tasks', workspaceId] }, (prev) =>
            prev ? patchTaskInList(prev, task_id, { status: to }) : prev
          )
          break
        }
        case 'task.title_changed': {
          const { task_id, title } = evt.data
          // Patch every board cache under this workspace (canonical and
          // search-filtered) so a renamed row never keeps the provisional title.
          queryClient.setQueriesData<Task[]>({ queryKey: ['tasks', workspaceId] }, (prev) =>
            prev ? patchTaskInList(prev, task_id, { title }) : prev
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
          queryClient.setQueriesData<Task[]>({ queryKey: ['tasks', workspaceId] }, (prev = []) => {
            const task = prev.find((t) => t.session_id === session_id)
            if (!task) return prev
            return patchTaskInList(prev, task.id, { status: mapSessionStatus(sessionStatus) })
          })
          break
        }
        case 'session.user_input_requested': {
          const { session_id } = evt.data
          queryClient.setQueriesData<Task[]>({ queryKey: ['tasks', workspaceId] }, (prev = []) => {
            const task = prev.find((t) => t.session_id === session_id)
            if (!task) return prev
            return patchTaskInList(prev, task.id, { status: 'awaiting_user' as const })
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
 * TASK_STATUS_TRANSITIONS lists the hub-legal manual task status moves. The
 * move menu is a narrow window: `waiting` cards are start + archive only
 * (dispatch owns waiting -> running), `awaiting_user` is entered only by the
 * agent's session derivation, and `done` is terminal — finished tasks expose
 * no move actions at all.
 */
export const TASK_STATUS_TRANSITIONS: Record<Task['status'], Task['status'][]> = {
  waiting: [],
  running: ['waiting', 'done'],
  awaiting_user: ['running', 'done'],
  done: [],
  failed: ['done'],
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
        prev ? patchTaskInList(prev, id, { status }) : prev
      )
      return { previous }
    },
    onError: (_err, _vars, context) => {
      context?.previous.forEach(([key, data]) => queryClient.setQueryData(key, data))
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ['tasks'] }),
  })
}

/**
 * useStartTaskMutation dispatches a backlog (`waiting` + `auto_start: false`)
 * task via POST /api/tasks/{id}/start. The hub flips it to `running` and the
 * settle refetch (every ['tasks', …] list) relocates the card; on error (e.g.
 * the hub's 409 for an already-started task) the caller's toast reports the
 * hub message.
 */
export function useStartTaskMutation() {
  const api = useHubApi()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => api.startTask(id),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ['tasks'] }),
  })
}

/**
 * useArchiveTaskMutation archives/unarchives a task via PATCH
 * /api/tasks/{id} ({archived}). Archiving removes the row from the default
 * lists (the hub excludes archived rows unless `?archived=1`), so like the
 * status move this refetches every ['tasks', …] list on settle.
 */
export function useArchiveTaskMutation() {
  const api = useHubApi()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: ({ id, archived }: { id: string; archived: boolean }) =>
      api.patchTask(id, { archived }),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ['tasks'] }),
  })
}
