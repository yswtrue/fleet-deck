import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Button } from 'antd'
import { useHubApi } from '../hooks/useHubApi.ts'
import { AddNodeModal } from '../components/AddNodeModal.tsx'
import { NodeCard } from '../components/NodeCard.tsx'
import { LoadingSkeleton } from '../components/LoadingSkeleton.tsx'
import { EmptyState } from '../components/EmptyState.tsx'
import { ErrorState } from '../components/ErrorState.tsx'
import { useTranslation } from '../i18n/index.ts'

export function Home() {
  const api = useHubApi()
  const { t } = useTranslation('pages')
  const [addOpen, setAddOpen] = useState(false)
  const nodesQuery = useQuery({
    queryKey: ['nodes'],
    queryFn: () => api.listNodes(),
    // last_seen_at tracks node heartbeats; poll so the displayed age (and the
    // live status overlay) stays fresh while the page is open.
    refetchInterval: 30_000,
  })

  const nodes = nodesQuery.data ?? []

  return (
    <div className="fd-stack fd-stack-lg">
      <header className="fd-section-header">
        <div className="fd-page-header">
          <h1 className="fd-page-title">{t('home.title')}</h1>
          <p className="fd-page-subtitle">{t('home.subtitle')}</p>
        </div>
        <Button type="primary" onClick={() => setAddOpen(true)}>
          {t('home.addNode')}
        </Button>
      </header>
      {nodesQuery.isLoading ? (
        <LoadingSkeleton count={6} />
      ) : nodesQuery.isError && !nodesQuery.data ? (
        <ErrorState title={t('home.loadError')} onRetry={() => void nodesQuery.refetch()}>
          {nodesQuery.error.message}
        </ErrorState>
      ) : nodes.length === 0 ? (
        <EmptyState
          title={t('home.emptyTitle')}
          action={
            <Button type="primary" onClick={() => setAddOpen(true)}>
              {t('home.addNode')}
            </Button>
          }
        >
          {t('home.emptyBody')}
        </EmptyState>
      ) : (
        <div className="fd-grid">
          {nodes.map((node) => (
            <NodeCard key={node.id} node={node} />
          ))}
        </div>
      )}
      <AddNodeModal open={addOpen} onClose={() => setAddOpen(false)} />
    </div>
  )
}
