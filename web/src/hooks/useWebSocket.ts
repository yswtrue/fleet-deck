import { useContext, useEffect } from 'react'
import { WebSocketContext } from './WebSocketContext.ts'
import type { Event as ProtocolEvent } from '../gen/protocol/index.ts'

export function useWebSocket(onEvent: (event: ProtocolEvent) => void) {
  const ctx = useContext(WebSocketContext)
  if (!ctx) throw new Error('useWebSocket must be used within WebSocketProvider')
  useEffect(() => ctx.subscribe(onEvent), [onEvent, ctx])
  return {
    connected: ctx.connected,
    connectionState: ctx.connectionState,
    error: ctx.error,
  }
}
