import { useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { useHubApi } from '../hooks/useHubApi.ts'
import { useTranslation } from '../i18n/index.ts'
import { Board } from '../components/Board.tsx'
import { ErrorState } from '../components/ErrorState.tsx'
import { LoadingSkeleton } from '../components/LoadingSkeleton.tsx'

/**
 * Workspace board page. The header resolves the workspace itself
 * (GET /api/workspaces/{id}) for its name + path; the board below owns the
 * task columns.
 */
export function Workspace() {
  const { t } = useTranslation('board')
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

  if (!workspaceId) return <ErrorState title={t('workspace.invalid')} />
  if (isError)
    return (
      <ErrorState title={t('workspace.notFound')} onRetry={() => void refetch()}>
        {t('workspace.notFoundBody')}
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
