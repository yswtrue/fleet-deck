import { useQuery } from '@tanstack/react-query'
import { useHubApi } from '../hooks/useHubApi.ts'
import { NodeCard } from '../components/NodeCard.tsx'
import { LoadingSkeleton } from '../components/LoadingSkeleton.tsx'
import { EmptyState } from '../components/EmptyState.tsx'
import { ErrorState } from '../components/ErrorState.tsx'

/** Command a new node runs to register itself with the hub. */
const NODE_SNIPPET = 'go run ./cmd/node --config examples/node.config.yaml'

export function Home() {
  const api = useHubApi()
  const nodesQuery = useQuery({
    queryKey: ['nodes'],
    queryFn: () => api.listNodes(),
  })

  const nodes = nodesQuery.data ?? []

  return (
    <div className="fd-stack fd-stack-lg">
      <header className="fd-page-header">
        <h1 className="fd-page-title">Nodes</h1>
        <p className="fd-page-subtitle">
          Machines registered with this hub. Open one to manage its workspaces and tasks.
        </p>
      </header>
      {nodesQuery.isLoading ? (
        <LoadingSkeleton count={6} />
      ) : nodesQuery.error ? (
        <ErrorState title="Could not load nodes" onRetry={() => void nodesQuery.refetch()}>
          {nodesQuery.error.message}
        </ErrorState>
      ) : nodes.length === 0 ? (
        <EmptyState title="No nodes yet" snippet={NODE_SNIPPET}>
          Start a node on any machine and it will register itself here:
        </EmptyState>
      ) : (
        <div className="fd-grid">
          {nodes.map((node) => (
            <NodeCard key={node.id} node={node} />
          ))}
        </div>
      )}
    </div>
  )
}
