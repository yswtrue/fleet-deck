import { useQuery } from '@tanstack/react-query'
import { useHubApi } from '../hooks/useHubApi.ts'
import { WorkspaceCard } from './WorkspaceCard.tsx'
import { LoadingSkeleton } from './LoadingSkeleton.tsx'
import { EmptyState } from './EmptyState.tsx'
import { ErrorState } from './ErrorState.tsx'
import type { TaskStatus } from '../types.ts'

export function WorkspaceList({ nodeId }: { nodeId: string }) {
  const api = useHubApi()
  const workspacesQuery = useQuery({
    queryKey: ['workspaces', nodeId],
    queryFn: () => api.listWorkspaces(nodeId),
    enabled: !!nodeId,
  })

  const tasksQuery = useQuery({
    queryKey: ['tasks-by-workspace', nodeId],
    queryFn: () => api.listTasksByNode(nodeId),
    enabled: !!nodeId,
  })

  const workspaces = workspacesQuery.data ?? []

  // Per-workspace task counts by status, from one node-wide task listing.
  const statusCounts: Record<string, Partial<Record<TaskStatus, number>>> = {}
  for (const task of tasksQuery.data ?? []) {
    const counts = (statusCounts[task.workspace_id] ??= {})
    counts[task.status] = (counts[task.status] ?? 0) + 1
  }

  if (workspacesQuery.isLoading) return <LoadingSkeleton count={4} />
  if (workspacesQuery.error)
    return (
      <ErrorState title="Could not load workspaces" onRetry={() => void workspacesQuery.refetch()}>
        {workspacesQuery.error.message}
      </ErrorState>
    )
  if (workspaces.length === 0)
    return <EmptyState title="No workspaces yet">Use “New workspace” above to add one.</EmptyState>

  return (
    <div className="fd-grid">
      {workspaces.map((workspace) => (
        <WorkspaceCard key={workspace.id} workspace={workspace} statusCounts={statusCounts[workspace.id] ?? {}} />
      ))}
    </div>
  )
}
