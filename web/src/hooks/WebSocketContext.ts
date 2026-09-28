import { createContext } from 'react'
import type { Event as ProtocolEvent } from '../gen/protocol/index.ts'

type Listener = (event: ProtocolEvent) => void

/**
 * 'failed' is terminal: the hub rejected the socket (401) and the provider
 * stopped its reconnect loop until the auth token changes. Consumers that
 * treat anything != 'open' as offline need no change.
 */
export type ConnectionState = 'connecting' | 'open' | 'closed' | 'failed'

export interface ContextValue {
  connected: boolean
  connectionState: ConnectionState
  seq: number
  lastSeq: number
  error: Error | null
  subscribe: (listener: Listener) => () => void
}

export const WebSocketContext = createContext<ContextValue | null>(null)
