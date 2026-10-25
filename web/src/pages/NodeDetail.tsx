import { useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { Tag } from 'antd'
import { useHubApi } from '../hooks/useHubApi.ts'
import { WorkspaceList } from '../components/WorkspaceList.tsx'
import { WorkspaceForm } from '../components/WorkspaceForm.tsx'
import { ErrorState } from '../components/ErrorState.tsx'
import { LoadingSkeleton } from '../components/LoadingSkeleton.tsx'
import { formatRelative } from '../util.ts'
import { useTranslation } from '../i18n/index.ts'

export function NodeDetail() {
  const { nodeId } = useParams<{ nodeId: string }>()
  const api = useHubApi()
  const { t } = useTranslation('pages')

  const nodeQuery = useQuery({
    queryKey: ['nodes', nodeId],
    queryFn: () => api.getNode(nodeId ?? ''),
    enabled: !!nodeId,
    // last_seen_at tracks node heartbeats; poll so the displayed age and the
    // live status overlay do not freeze at page load.
    refetchInterval: 30_000,
    // Deep links to missing/invisible nodes are permanent failures: surface
    // the error state immediately instead of retrying behind a skeleton.
    retry: false,
  })
  if (!nodeId) return <ErrorState title={t('node.invalid')} />
  // An invisible or deleted node is a dead end: do not render the workspaces
  // section (and its create button) below for a node the user cannot see.
  // A failed background heartbeat refetch keeps the last good node on screen.
  if (nodeQuery.isError && !nodeQuery.data)
    return (
      <ErrorState title={t('node.notFound')} onRetry={() => void nodeQuery.refetch()}>
        {t('node.notFoundBody')}
      </ErrorState>
    )

  const node = nodeQuery.data
  const agentTypes = node?.agent_types ?? []

  return (
    <div className="fd-stack fd-stack-lg">
      {nodeQuery.isLoading ? (
        <LoadingSkeleton count={1} />
      ) : node ? (
        <header className="fd-stack">
          <div className="fd-section-header">
            <h1 className="fd-page-title">{node.name}</h1>
            <span className="fd-pill" data-status={node.status}>
              {node.status === 'online' ? t('common:status.online') : t('common:status.offline')}
            </span>
          </div>
          <div className="fd-meta">
            <span>
              {t('node.host')} <code>{node.host || t('node.noHost')}</code>
            </span>
            {node.last_seen_at && (
              <span title={new Date(node.last_seen_at).toLocaleString()}>
                {t('node.lastSeen', { time: formatRelative(node.last_seen_at) })}
              </span>
            )}
          </div>
          <div className="fd-chips">
            <span className="fd-chip-label">{t('node.agents')}</span>
            {agentTypes.length > 0 ? (
              agentTypes.map((agentType) => (
                <Tag key={agentType} color="blue">
                  {agentType}
                </Tag>
              ))
            ) : (
              <span>{t('common:misc.noneYet')}</span>
            )}
          </div>
        </header>
      ) : null}
      <section className="fd-stack">
        <div className="fd-section-header">
          <h2 className="fd-section-title">{t('node.workspaces')}</h2>
          {/* Anyone who can see this page is bound to the node (or is an
              admin), so workspace creation is always permitted here. */}
          <WorkspaceForm nodeId={nodeId} />
        </div>
        <WorkspaceList nodeId={nodeId} />
      </section>
    </div>
  )
}
