import { useParams } from 'react-router-dom'
import { ErrorState } from '../components/ErrorState.tsx'
import { SessionView } from '../components/SessionView.tsx'

export function Session() {
  const { sessionId } = useParams<{ sessionId: string }>()

  if (!sessionId) return <ErrorState title="Invalid session" />

  return <SessionView sessionId={sessionId} />
}
