export function formatRelative(iso: string): string {
  const date = new Date(iso)
  const now = new Date()
  const diff = now.getTime() - date.getTime()
  const seconds = Math.floor(diff / 1000)
  if (seconds < 60) return 'just now'
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  if (days < 30) return `${days}d ago`
  return date.toLocaleDateString()
}

export function statusLabel(status: string): string {
  switch (status) {
    case 'waiting':
      return 'Waiting'
    case 'running':
      return 'Running'
    case 'done':
      return 'Done'
    case 'awaiting_user':
      return 'Awaiting you'
    case 'failed':
      return 'Failed'
    default:
      return status
  }
}
