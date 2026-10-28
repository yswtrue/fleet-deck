import type { QueryClient } from '@tanstack/react-query'
import type { Subtask, SubtaskUpdated } from '../gen/protocol/index.ts'

/** Shared cache key of one task's mirrored todos (board cards + session panel). */
export const subtasksQueryKey = (taskId: string) => ['subtasks', taskId] as const

/**
 * applySubtaskUpdated folds one `subtask.updated` frame into the shared
 * subtask cache. The row is applied instead of invalidating: todo snapshots
 * arrive in bursts, and an invalidate that lands while a refetch is in flight
 * is coalesced, leaving the last status unapplied until the next event.
 *
 * A fetch that started before this frame can still resolve with the
 * pre-update rows and overwrite them, so a refetch is requested when one is
 * already in flight (that cancels and restarts it); with nothing in flight the
 * applied row is already exact.
 */
export function applySubtaskUpdated(
  queryClient: QueryClient,
  { task_id, subtask, removed }: SubtaskUpdated
) {
  const key = subtasksQueryKey(task_id)
  queryClient.setQueryData<Subtask[]>(key, (prev = []) => {
    if (removed) return prev.filter((s) => s.id !== subtask.id)
    const idx = prev.findIndex((s) => s.id === subtask.id)
    const next = idx < 0 ? [...prev, subtask] : prev.map((s, i) => (i === idx ? subtask : s))
    return next.sort((a, b) => a.sort_order - b.sort_order || a.id.localeCompare(b.id))
  })
  if (queryClient.isFetching({ queryKey: key }) > 0) {
    void queryClient.invalidateQueries({ queryKey: key })
  }
}
