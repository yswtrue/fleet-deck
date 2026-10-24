import { useNavigate, useParams } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { message } from 'antd'
import { useHubApi } from '../hooks/useHubApi.ts'
import { useTranslation } from '../i18n/index.ts'
import { AutoAllowScopeSelect } from '../components/AutoAllowScopeSelect.tsx'
import { Board } from '../components/Board.tsx'
import { ErrorState } from '../components/ErrorState.tsx'
import { LoadingSkeleton } from '../components/LoadingSkeleton.tsx'
import { WorkspaceActions } from '../components/WorkspaceActions.tsx'
import type { Workspace as WorkspaceData } from '../gen/protocol/index.ts'

/**
 * Workspace board page. The header resolves the workspace itself
 * (GET /api/workspaces/{id}) for its name + path; the board below owns the
 * task columns. The header also carries the permission auto-allow range: it
 * decides which ACP permission requests the node answers without asking.
 */
export function Workspace() {
  const { t } = useTranslation('board')
  const { workspaceId } = useParams<{ workspaceId: string }>()
  const navigate = useNavigate()
  const api = useHubApi()
  const queryClient = useQueryClient()
  // `message.useMessage()`: antd v5's static API renders nothing under React 19.
  const [messageApi, messageHolder] = message.useMessage()
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

  const setAutoAllowScope = useMutation({
    mutationFn: (scope: WorkspaceData['auto_allow_scope']) =>
      api.setWorkspaceAutoAllowScope(workspaceId ?? '', scope),
    onSuccess: (updated) => {
      queryClient.setQueryData(['workspace', workspaceId], updated)
    },
    onError: (error) => {
      messageApi.error(t('workspace.autoAllowFailed', { message: error.message }))
    },
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
      {messageHolder}
      <header className="fd-page-header">
        <div className="fd-section-header">
          <h1 className="fd-page-title">{workspace.name}</h1>
          <WorkspaceActions
            workspace={workspace}
            variant="buttons"
            messageApi={messageApi}
            onDeleted={() => navigate(`/nodes/${workspace.node_id}`)}
          />
        </div>
        <p className="fd-page-subtitle">
          <code title={workspace.path}>{workspace.path}</code>
        </p>
        <div className="fd-meta">
          <span className="fd-chip-label">{t('workspace.autoAllow')}</span>
          <AutoAllowScopeSelect
            value={workspace.auto_allow_scope}
            onChange={(scope) => setAutoAllowScope.mutate(scope)}
            busy={setAutoAllowScope.isPending}
          />
          <span>{t('workspace.autoAllowHint')}</span>
        </div>
      </header>
      <Board workspaceId={workspaceId} nodeId={workspace.node_id} />
    </div>
  )
}
