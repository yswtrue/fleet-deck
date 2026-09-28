import { useQuery } from '@tanstack/react-query'
import { apiClient } from '../apiClient.ts'
import { useTranslation } from '../i18n/index.ts'

export interface SessionUsageProps {
  /** Session whose token usage is summarized. */
  sessionId: string
}

/**
 * Compact per-session token usage panel, mounted in the session sidebar. One
 * line per model — `fake · 120 in / 80 out / 200 total` — with a loading
 * placeholder and an empty state before any tokens have been recorded.
 */
export function SessionUsage({ sessionId }: SessionUsageProps) {
  const { t } = useTranslation('stats')
  const statsQuery = useQuery({
    queryKey: ['stats-tokens', 'session', sessionId],
    queryFn: () => apiClient.getStatsTokens({ session_id: sessionId }),
  })
  const rows = statsQuery.data?.by_model ?? []

  return (
    <section className="fd-stack" aria-labelledby="session-usage-title">
      <h3 id="session-usage-title" className="fd-section-title">
        {t('session.title')}
      </h3>
      {statsQuery.isLoading ? (
        <p className="fd-meta" role="status">
          {t('loading')}
        </p>
      ) : statsQuery.error ? (
        <p className="fd-meta" role="alert">
          {t('loadError')}
        </p>
      ) : rows.length === 0 ? (
        <p className="fd-meta">{t('session.empty')}</p>
      ) : (
        <div className="fd-stack" role="list">
          {rows.map((row) => (
            <div key={row.model} className="fd-meta" role="listitem">
              {t('session.line', {
                model: row.model,
                prompt: row.prompt_tokens.toLocaleString(),
                completion: row.completion_tokens.toLocaleString(),
                total: row.total_tokens.toLocaleString(),
              })}
            </div>
          ))}
        </div>
      )}
    </section>
  )
}
