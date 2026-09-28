import { useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { Card, Tag, Typography } from 'antd'
import { useHubApi } from '../hooks/useHubApi.ts'
import { formatRelative } from '../util.ts'
import type { Node } from '../types.ts'
import styles from './NodeCard.module.css'

/**
 * Overview card for one node. Workspace and task numbers come from the same
 * query keys the node detail page uses (`['workspaces', id]`,
 * `['tasks-by-workspace', id]`), so navigating there reuses the cache and
 * invalidations from the workspace/task flows apply here too.
 */
export function NodeCard({ node }: { node: Node }) {
  const navigate = useNavigate()
  const api = useHubApi()

  const workspacesQuery = useQuery({
    queryKey: ['workspaces', node.id],
    queryFn: () => api.listWorkspaces(node.id),
  })
  const tasksQuery = useQuery({
    queryKey: ['tasks-by-workspace', node.id],
    queryFn: () => api.listTasksByNode(node.id),
  })

  const tasks = tasksQuery.data ?? []
  const workspaceCount = workspacesQuery.data?.length ?? 0
  const activeTaskCount = tasks.filter((task) => task.status !== 'done').length
  // Dynamic membership: which agent types appear on this node's tasks.
  const seenAgentTypes = new Set<string>()
  for (const task of tasks) seenAgentTypes.add(task.agent_type)
  const agentTypes = [...seenAgentTypes].sort()
  const capabilities = node.capabilities ?? []

  const handleKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    if (event.key === 'Enter') {
      navigate(`/nodes/${node.id}`)
    }
  }

  return (
    <Card
      className={styles.card}
      tabIndex={0}
      role="button"
      onClick={() => navigate(`/nodes/${node.id}`)}
      onKeyDown={handleKeyDown}
      aria-label={`Node ${node.name}`}
    >
      <div className={styles.head}>
        <Typography.Title level={5} className={styles.name} ellipsis={{ tooltip: node.name }}>
          {node.name}
        </Typography.Title>
        <span className="fd-pill" data-status={node.status}>
          {node.status === 'online' ? 'Online' : 'Offline'}
        </span>
      </div>
      <div className={styles.host} title={node.host}>
        {node.host || 'No host set'}
      </div>
      <div className="fd-chips">
        <span className="fd-chip-label">Capabilities</span>
        {capabilities.length > 0 ? (
          capabilities.map((capability) => <Tag key={capability}>{capability}</Tag>)
        ) : (
          <span className={styles.muted}>none</span>
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
          <span className={styles.muted}>none yet</span>
        )}
      </div>
      <div className={styles.meta}>
        <span>
          {workspaceCount} {workspaceCount === 1 ? 'workspace' : 'workspaces'}
        </span>
        <span>
          {activeTaskCount} active {activeTaskCount === 1 ? 'task' : 'tasks'}
        </span>
      </div>
      {node.last_seen_at && (
        <div className={styles.lastSeen} title={new Date(node.last_seen_at).toLocaleString()}>
          Last seen {formatRelative(node.last_seen_at)}
        </div>
      )}
    </Card>
  )
}
