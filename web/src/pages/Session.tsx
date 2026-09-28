import { useParams } from 'react-router-dom'
import { ErrorState } from '../components/ErrorState.tsx'
import { SessionView } from '../components/SessionView.tsx'
import { useTranslation } from '../i18n/index.ts'

export function Session() {
  const { t } = useTranslation('session')
  const { sessionId } = useParams<{ sessionId: string }>()

  if (!sessionId) return <ErrorState title={t('invalid')} />

  return <SessionView sessionId={sessionId} />
}
