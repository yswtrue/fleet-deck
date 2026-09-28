import { useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { Tag } from 'antd'
import { useHubApi } from '../hooks/useHubApi.ts'
import { useAuth } from '../hooks/useAuth.ts'
import { WorkspaceList } from '../components/WorkspaceList.tsx'
import { WorkspaceForm } from '../components/WorkspaceForm.tsx'
import { ErrorState } from '../components/ErrorState.tsx'
import { LoadingSkeleton } from '../components/LoadingSkeleton.tsx'
import { formatRelative } from '../util.ts'

export function NodeDetail() {
  const { nodeId } = useParams<{ nodeId: string }>()
  const api = useHubApi()
  const { isAdmin } = useAuth()

  const nodeQuery = useQuery({
    queryKey: ['nodes', nodeId],
    queryFn: () => api.getNode(nodeId ?? ''),
    enabled: !!nodeId,
  })
  const tasksQuery = useQuery({
    queryKey: ['tasks-by-workspace', nodeId],
    queryFn: () => api.listTasksByNode(nodeId ?? ''),
    enabled: !!nodeId,
  })

  if (!nodeId) return <ErrorState title="Invalid node" />

  const node = nodeQuery.data
  // Agent types are whatever the node's tasks ran with; the node itself does
  // not advertise them.
  const seenAgentTypes = new Set<string>()
  for (const task of tasksQuery.data ?? []) seenAgentTypes.add(task.agent_type)
  const agentTypes = [...seenAgentTypes].sort()
  const capabilities = node?.capabilities ?? []

  return (
    <div className="fd-stack fd-stack-lg">
      {nodeQuery.isLoading ? (
        <LoadingSkeleton count={1} />
      ) : nodeQuery.error ? (
        <ErrorState title="Could not load node" onRetry={() => void nodeQuery.refetch()}>
          {nodeQuery.error.message}
        </ErrorState>
      ) : node ? (
        <header className="fd-stack">
          <div className="fd-section-header">
            <h1 className="fd-page-title">{node.name}</h1>
            <span className="fd-pill" data-status={node.status}>
              {node.status === 'online' ? 'Online' : 'Offline'}
            </span>
          </div>
          <div className="fd-meta">
            <span>
              Host: <code>{node.host || 'No host set'}</code>
            </span>
            {node.last_seen_at && (
              <span title={new Date(node.last_seen_at).toLocaleString()}>
                Last seen {formatRelative(node.last_seen_at)}
              </span>
            )}
          </div>
          <div className="fd-chips">
            <span className="fd-chip-label">Capabilities</span>
            {capabilities.length > 0 ? (
              capabilities.map((capability) => <Tag key={capability}>{capability}</Tag>)
            ) : (
              <span>none</span>
            )}
          </div>
          <div className="fd-chips">
            <span className="fd-chip-label">Agents</span>
            {agentTypes.length > 0 ? (
              agentTypes.map((agentType) => (
                <Tag key={agentType} color="blue">
                  {agentType}
                </Tag>
              ))
            ) : (
              <span>none yet</span>
            )}
          </div>
        </header>
      ) : null}
      <section className="fd-stack">
        <div className="fd-section-header">
          <h2 className="fd-section-title">Workspaces</h2>
          {/* Workspace creation is admin-only on the hub; hide the affordance
              for regular users instead of letting the POST 403. */}
          {isAdmin && <WorkspaceForm nodeId={nodeId} />}
        </div>
        <WorkspaceList nodeId={nodeId} />
      </section>
    </div>
  )
}
