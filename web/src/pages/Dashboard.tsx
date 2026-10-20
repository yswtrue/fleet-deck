import { useMemo } from 'react'
import { Table } from 'antd'
import type { TableColumnsType } from 'antd'
import { useQuery } from '@tanstack/react-query'
import { apiClient } from '../apiClient.ts'
import type { StatsModelUsage, StatsTasks, StatsTokens } from '../apiClient.ts'
import { EmptyState } from '../components/EmptyState.tsx'
import { ErrorState } from '../components/ErrorState.tsx'
import { LoadingSkeleton } from '../components/LoadingSkeleton.tsx'
import { SummaryCard } from '../components/SummaryCard.tsx'
import { formatCompact, statusLabel } from '../util.ts'
import { useTranslation } from '../i18n/index.ts'
import type { TaskStatus } from '../types.ts'
import styles from './Dashboard.module.css'

/** Statuses shown in the by-status strip, in board order. */
const STATUSES: TaskStatus[] = ['waiting', 'running', 'awaiting_user', 'done']

/** The local-day reporting window as RFC3339 bounds. */
function todayWindow(): { from: string; to: string } {
  const start = new Date()
  start.setHours(0, 0, 0, 0)
  const end = new Date(start)
  end.setDate(end.getDate() + 1)
  return { from: start.toISOString(), to: end.toISOString() }
}

const compactCell = (value: number) => <span title={String(value)}>{formatCompact(value)}</span>

/**
 * Dashboard: today's task statistics (created/completed plus the status split
 * of the day's tasks) and today's token usage (totals, turns, per-model
 * breakdown). Both aggregates come from the /api/stats/* endpoints over the
 * local-day window.
 */
export function Dashboard() {
  const { t } = useTranslation('pages')
  const { t: tStats } = useTranslation('stats')
  const today = useMemo(() => todayWindow(), [])

  const taskQuery = useQuery({
    queryKey: ['stats-tasks', 'today', today],
    queryFn: () => apiClient.getStatsTasks(today),
  })
  const tokenQuery = useQuery({
    queryKey: ['stats-tokens', 'today', today],
    queryFn: () => apiClient.getStatsTokens(today),
  })

  const taskStats: StatsTasks = taskQuery.data ?? { created: 0, completed: 0, by_status: [] }
  const counts: Partial<Record<TaskStatus, number>> = {}
  for (const row of taskStats.by_status) {
    counts[row.status] = (counts[row.status] ?? 0) + row.count
  }
  const tokenStats: StatsTokens = tokenQuery.data ?? {
    total: { prompt_tokens: 0, completion_tokens: 0, total_tokens: 0 },
    by_model: [],
  }
  const turns = tokenStats.by_model.reduce((sum, model) => sum + model.turns, 0)

  const columns: TableColumnsType<StatsModelUsage> = [
    { title: tStats('table.model'), dataIndex: 'model', key: 'model' },
    {
      title: tStats('table.prompt'),
      dataIndex: 'prompt_tokens',
      key: 'prompt_tokens',
      align: 'right',
      render: compactCell,
    },
    {
      title: tStats('table.completion'),
      dataIndex: 'completion_tokens',
      key: 'completion_tokens',
      align: 'right',
      render: compactCell,
    },
    {
      title: tStats('table.total'),
      dataIndex: 'total_tokens',
      key: 'total_tokens',
      align: 'right',
      render: compactCell,
    },
    {
      title: tStats('table.turns'),
      dataIndex: 'turns',
      key: 'turns',
      align: 'right',
      render: compactCell,
    },
  ]

  return (
    <div className="fd-stack fd-stack-lg">
      <header className="fd-page-header">
        <h1 className="fd-page-title">{t('dashboard.title')}</h1>
        <p className="fd-page-subtitle">{t('dashboard.subtitle')}</p>
      </header>

      <section className="fd-stack">
        <h2 className="fd-section-title">{t('dashboard.tasks.title')}</h2>
        {taskQuery.error ? (
          <ErrorState title={t('dashboard.loadError')} onRetry={() => void taskQuery.refetch()}>
            {taskQuery.error.message}
          </ErrorState>
        ) : taskQuery.isLoading ? (
          <LoadingSkeleton count={2} />
        ) : (
          <>
            <div className={styles.taskSummary}>
              <SummaryCard label={t('dashboard.tasks.created')} value={taskStats.created} />
              <SummaryCard label={t('dashboard.tasks.completed')} value={taskStats.completed} />
            </div>
            <div className={styles.statusCard}>
              <div
                className={styles.statusRow}
                role="group"
                aria-label={t('dashboard.tasks.byStatus')}
              >
                {STATUSES.map((status) => (
                  <div key={status} className={styles.statusItem}>
                    <span className={styles.statusLabel}>{statusLabel(status)}</span>
                    <span className={styles.statusCount} title={String(counts[status] ?? 0)}>
                      {formatCompact(counts[status] ?? 0)}
                    </span>
                  </div>
                ))}
              </div>
            </div>
          </>
        )}
      </section>

      <section className="fd-stack">
        <h2 className="fd-section-title">{t('dashboard.tokens.title')}</h2>
        {tokenQuery.error ? (
          <ErrorState title={t('dashboard.loadError')} onRetry={() => void tokenQuery.refetch()}>
            {tokenQuery.error.message}
          </ErrorState>
        ) : tokenQuery.isLoading ? (
          <LoadingSkeleton count={2} />
        ) : (
          <>
            <div className="fd-grid-4">
              <SummaryCard
                label={tStats('summary.prompt')}
                value={tokenStats.total.prompt_tokens}
              />
              <SummaryCard
                label={tStats('summary.completion')}
                value={tokenStats.total.completion_tokens}
              />
              <SummaryCard label={tStats('summary.total')} value={tokenStats.total.total_tokens} />
              <SummaryCard label={tStats('summary.turns')} value={turns} />
            </div>
            {tokenStats.by_model.length === 0 ? (
              <EmptyState title={tStats('empty.title')}>{tStats('empty.body')}</EmptyState>
            ) : (
              <Table<StatsModelUsage>
                rowKey="model"
                columns={columns}
                dataSource={tokenStats.by_model}
                pagination={false}
                size="small"
              />
            )}
          </>
        )}
      </section>
    </div>
  )
}
