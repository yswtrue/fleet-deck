import { useQuery } from '@tanstack/react-query'
import { message } from 'antd'
import { useHubApi } from '../hooks/useHubApi.ts'
import { useTranslation } from '../i18n/index.ts'
import { WorkspaceCard } from './WorkspaceCard.tsx'
import { LoadingSkeleton } from './LoadingSkeleton.tsx'
import { EmptyState } from './EmptyState.tsx'
import { ErrorState } from './ErrorState.tsx'
import type { TaskStatus } from '../types.ts'

export function WorkspaceList({ nodeId }: { nodeId: string }) {
  const api = useHubApi()
  const { t } = useTranslation('pages')
  // Card-owned holders unmount with a deleted card and would drop its toast;
  // the list owns the holder for the whole workspace grid.
  const [messageApi, messageHolder] = message.useMessage()
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

  let content: React.ReactNode
  if (workspacesQuery.isLoading) {
    content = <LoadingSkeleton count={4} />
  } else if (workspacesQuery.error) {
    content = (
      <ErrorState title={t('workspace.loadError')} onRetry={() => void workspacesQuery.refetch()}>
        {workspacesQuery.error.message}
      </ErrorState>
    )
  } else if (workspaces.length === 0) {
    content = <EmptyState title={t('workspace.emptyTitle')}>{t('workspace.emptyBody')}</EmptyState>
  } else {
    content = (
      <div className="fd-grid">
        {workspaces.map((workspace) => (
          <WorkspaceCard
            key={workspace.id}
            workspace={workspace}
            statusCounts={statusCounts[workspace.id] ?? {}}
            messageApi={messageApi}
          />
        ))}
      </div>
    )
  }

  return (
    <>
      {messageHolder}
      {content}
    </>
  )
}
