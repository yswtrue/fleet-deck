import { useParams } from 'react-router-dom'
import { ErrorState } from '../components/ErrorState.tsx'
import { SessionView } from '../components/SessionView.tsx'
import { useTranslation } from '../i18n/index.ts'

export function Session() {
  const { t } = useTranslation('session')
  const { sessionId } = useParams<{ sessionId: string }>()

  if (!sessionId) return <ErrorState title={t('invalid')} />

  // Keyed by session: switching sessions must drop the transcript window,
  // streamed text and composer state rather than carry them over.
  return <SessionView key={sessionId} sessionId={sessionId} />
}
