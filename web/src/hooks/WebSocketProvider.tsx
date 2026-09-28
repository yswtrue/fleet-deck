import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { authenticatedWsUrl, onTokenChange } from '../apiClient.ts'
import { queryClient } from '../queryClient.ts'
import { WebSocketContext, type ConnectionState, type ContextValue } from './WebSocketContext.ts'
import type { Event as ProtocolEvent, Node } from '../gen/protocol/index.ts'

interface HelloFrame {
  type: 'hello'
  data: { nodes: Node[] }
}

export function WebSocketProvider({ children }: { children: React.ReactNode }) {
  const [connected, setConnected] = useState(false)
  const [connectionState, setConnectionState] = useState<ConnectionState>('connecting')
  const [error, setError] = useState<Error | null>(null)
  const [seq, setSeq] = useState(0)
  const seqRef = useRef(0)
  const listenersRef = useRef<Set<(event: ProtocolEvent) => void>>(new Set())
  const wsRef = useRef<WebSocket | null>(null)
  const backoffRef = useRef(1000)

  const subscribe = useCallback((listener: (event: ProtocolEvent) => void) => {
    listenersRef.current.add(listener)
    return () => {
      listenersRef.current.delete(listener)
    }
  }, [])

  useEffect(() => {
    let reconnectTimer: number | null = null
    let disposed = false
    // Terminal after the hub rejects the socket (browser WebSockets never
    // expose the handshake status, so a close before `open` is our 401 signal).
    let rejected = false

    // Stale sockets (superseded by a reconnect or a token change) must not
    // touch shared state when their deferred close/error events land.
    const isCurrent = (ws: WebSocket) => !disposed && wsRef.current === ws

    const scheduleReconnect = () => {
      if (rejected || reconnectTimer) return
      const delay = Math.min(backoffRef.current, 30000)
      backoffRef.current = Math.min(backoffRef.current * 1.5, 30000)
      reconnectTimer = setTimeout(() => {
        reconnectTimer = null
        connect()
      }, delay)
    }

    const connect = () => {
      try {
        setConnectionState('connecting')
        const ws = new WebSocket(authenticatedWsUrl())
        wsRef.current = ws
        let opened = false

        ws.addEventListener('open', () => {
          if (!isCurrent(ws)) {
            ws.close()
            return
          }
          opened = true
          setConnected(true)
          setConnectionState('open')
          setError(null)
          backoffRef.current = 1000
        })

        ws.addEventListener('message', (event) => {
          if (!isCurrent(ws)) return
          try {
            const parsed = JSON.parse(event.data as string) as HelloFrame | ProtocolEvent
            if ('type' in parsed && parsed.type === 'hello') {
              queryClient.setQueryData(['nodes'], (parsed as HelloFrame).data.nodes)
              return
            }
            const evt = parsed as ProtocolEvent
            if (typeof evt.seq === 'number' && evt.seq > seqRef.current) {
              seqRef.current = evt.seq
              setSeq(evt.seq)
            }
            listenersRef.current.forEach((listener) => listener(evt))
          } catch (err) {
            console.error('Failed to parse WS message', err)
          }
        })

        ws.addEventListener('close', () => {
          if (!isCurrent(ws)) return
          setConnected(false)
          if (!opened) {
            // Rejected before opening (bad or missing token): stop the loop
            // instead of hammering the hub. A token change (login/logout)
            // below is the only thing that reconnects from here.
            rejected = true
            setConnectionState('failed')
            setError(new Error('WebSocket connection rejected'))
            return
          }
          setConnectionState('closed')
          scheduleReconnect()
        })

        ws.addEventListener('error', () => {
          if (!isCurrent(ws)) return
          setError(new Error('WebSocket error'))
        })
      } catch (err) {
        setError(err instanceof Error ? err : new Error(String(err)))
        scheduleReconnect()
      }
    }

    connect()

    // Login/logout changes the token: drop the dead-end state and reconnect
    // once with (or without) the new credentials.
    const unsubscribeToken = onTokenChange(() => {
      rejected = false
      backoffRef.current = 1000
      if (reconnectTimer) {
        clearTimeout(reconnectTimer)
        reconnectTimer = null
      }
      const old = wsRef.current
      wsRef.current = null
      old?.close()
      connect()
    })

    return () => {
      disposed = true
      unsubscribeToken()
      if (reconnectTimer) clearTimeout(reconnectTimer)
      wsRef.current?.close()
    }
  }, [])

  const value = useMemo<ContextValue>(
    () => ({ connected, connectionState, seq, lastSeq: seq, error, subscribe }),
    [connected, connectionState, seq, error, subscribe]
  )

  return <WebSocketContext.Provider value={value}>{children}</WebSocketContext.Provider>
}
