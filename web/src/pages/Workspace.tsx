import { useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { useHubApi } from '../hooks/useHubApi.ts'
import { Board } from '../components/Board.tsx'
import { ErrorState } from '../components/ErrorState.tsx'
import { LoadingSkeleton } from '../components/LoadingSkeleton.tsx'

/**
 * Workspace board page. The header resolves the workspace itself
 * (GET /api/workspaces/{id}) for its name + path; the board below owns the
 * task columns.
 */
export function Workspace() {
  const { workspaceId } = useParams<{ workspaceId: string }>()
  const api = useHubApi()
  const {
    data: workspace,
    isLoading,
    isError,
    refetch,
  } = useQuery({
    queryKey: ['workspace', workspaceId],
    queryFn: () => api.getWorkspace(workspaceId ?? ''),
    enabled: !!workspaceId,
  })

  if (!workspaceId) return <ErrorState title="Invalid workspace" />
  if (isError)
    return (
      <ErrorState title="Workspace not found" onRetry={() => void refetch()}>
        It may have been deleted, or your account may not have access to it.
      </ErrorState>
    )
  if (isLoading || !workspace) return <LoadingSkeleton count={2} />

  return (
    <div>
      <header className="fd-page-header">
        <h1 className="fd-page-title">{workspace.name}</h1>
        <p className="fd-page-subtitle">
          <code title={workspace.path}>{workspace.path}</code>
        </p>
      </header>
      <Board workspaceId={workspaceId} />
    </div>
  )
}
