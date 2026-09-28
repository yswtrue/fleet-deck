import { useState } from 'react'
import { DatePicker, Select, Table } from 'antd'
import type { TableColumnsType } from 'antd'
import { useQueries, useQuery } from '@tanstack/react-query'
import { apiClient } from '../apiClient.ts'
import type { StatsModelUsage, StatsTokensFilters } from '../apiClient.ts'
import { EmptyState } from '../components/EmptyState.tsx'
import { ErrorState } from '../components/ErrorState.tsx'
import { SummaryCard } from '../components/SummaryCard.tsx'
import { formatCompact } from '../util.ts'
import { useTranslation } from '../i18n/index.ts'

interface DateRange {
  from?: string
  to?: string
}

/**
 * Token usage dashboard: fleet totals, the per-model breakdown and the
 * optional workspace/date filters narrowing the aggregation window.
 */
export function Usage() {
  const { t } = useTranslation('stats')
  const [workspaceId, setWorkspaceId] = useState<string | undefined>(undefined)
  const [range, setRange] = useState<DateRange>({})

  // The hub lists workspaces per node, so picker options come from one
  // listWorkspaces call per node the user can see.
  const nodesQuery = useQuery({
    queryKey: ['nodes'],
    queryFn: () => apiClient.listNodes(),
  })
  const nodes = nodesQuery.data ?? []
  const workspaceQueries = useQueries({
    queries: nodes.map((node) => ({
      queryKey: ['workspaces', node.id] as const,
      queryFn: () => apiClient.listWorkspaces(node.id),
    })),
  })
  const workspaceOptions = [
    { value: '', label: t('filters.allWorkspaces') },
    ...workspaceQueries.flatMap((query) =>
      (query.data ?? []).map((workspace) => {
        const nodeName = nodes.find((node) => node.id === workspace.node_id)?.name
        return {
          value: workspace.id,
          label: nodeName ? `${workspace.name} — ${nodeName}` : workspace.name,
        }
      })
    ),
  ]

  const filters: StatsTokensFilters = {
    workspace_id: workspaceId,
    from: range.from,
    to: range.to,
  }
  const statsQuery = useQuery({
    queryKey: ['stats-tokens', 'dashboard', filters],
    queryFn: () => apiClient.getStatsTokens(filters),
  })
  const totals = statsQuery.data?.total ?? {
    prompt_tokens: 0,
    completion_tokens: 0,
    total_tokens: 0,
  }
  const rows = statsQuery.data?.by_model ?? []
  const turns = rows.reduce((sum, model) => sum + model.turns, 0)

  const compactCell = (value: number) => <span title={String(value)}>{formatCompact(value)}</span>
  const columns: TableColumnsType<StatsModelUsage> = [
    { title: t('table.model'), dataIndex: 'model', key: 'model' },
    {
      title: t('table.prompt'),
      dataIndex: 'prompt_tokens',
      key: 'prompt_tokens',
      align: 'right',
      render: compactCell,
    },
    {
      title: t('table.completion'),
      dataIndex: 'completion_tokens',
      key: 'completion_tokens',
      align: 'right',
      render: compactCell,
    },
    {
      title: t('table.total'),
      dataIndex: 'total_tokens',
      key: 'total_tokens',
      align: 'right',
      render: compactCell,
    },
    {
      title: t('table.turns'),
      dataIndex: 'turns',
      key: 'turns',
      align: 'right',
      render: compactCell,
    },
  ]

  return (
    <div className="fd-stack fd-stack-lg">
      <header className="fd-page-header">
        <h1 className="fd-page-title">{t('title')}</h1>
        <p className="fd-page-subtitle">{t('subtitle')}</p>
      </header>

      <div className="fd-chips" role="group" aria-label={t('title')}>
        <label className="fd-chip-label" htmlFor="usage-workspace">
          {t('filters.workspace')}
        </label>
        <Select
          id="usage-workspace"
          style={{ minWidth: 220 }}
          value={workspaceId ?? ''}
          options={workspaceOptions}
          onChange={(value: string) => setWorkspaceId(value || undefined)}
        />
        <span className="fd-chip-label">{t('filters.range')}</span>
        <DatePicker.RangePicker
          allowClear
          placeholder={[t('filters.fromPlaceholder'), t('filters.toPlaceholder')]}
          onChange={(dates) =>
            setRange({
              from: dates?.[0]?.startOf('day').toISOString(),
              to: dates?.[1]?.endOf('day').toISOString(),
            })
          }
        />
      </div>

      {statsQuery.error ? (
        <ErrorState title={t('loadError')} onRetry={() => void statsQuery.refetch()}>
          {statsQuery.error.message}
        </ErrorState>
      ) : statsQuery.isLoading ? (
        <Table<StatsModelUsage>
          rowKey="model"
          columns={columns}
          dataSource={[]}
          loading
          pagination={false}
        />
      ) : rows.length === 0 ? (
        <EmptyState title={t('empty.title')}>{t('empty.body')}</EmptyState>
      ) : (
        <>
          <div className="fd-grid">
            <SummaryCard label={t('summary.prompt')} value={totals.prompt_tokens} />
            <SummaryCard label={t('summary.completion')} value={totals.completion_tokens} />
            <SummaryCard label={t('summary.total')} value={totals.total_tokens} />
            <SummaryCard label={t('summary.turns')} value={turns} />
          </div>
          <section className="fd-stack">
            <h2 className="fd-section-title">{t('byModel')}</h2>
            <Table<StatsModelUsage>
              rowKey="model"
              columns={columns}
              dataSource={rows}
              pagination={false}
            />
          </section>
        </>
      )}
    </div>
  )
}
