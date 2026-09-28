import { useCallback } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useWebSocket } from './useWebSocket.ts'
import { dispatchEvent } from './dispatchEvent.ts'
import { useTranslation } from '../i18n/index.ts'
import type { QueryClient } from '@tanstack/react-query'
import type { Event as ProtocolEvent } from '../gen/protocol/index.ts'
import type { Task } from '../types.ts'

export const NOTIFY_STORAGE_KEY = 'fleetdeck.notify'

export function notificationsSupported(): boolean {
  return (
    typeof window !== 'undefined' &&
    typeof Notification !== 'undefined' &&
    typeof Notification.requestPermission === 'function'
  )
}

export function isNotifyEnabled(): boolean {
  if (typeof window === 'undefined') return false
  try {
    return window.localStorage.getItem(NOTIFY_STORAGE_KEY) === 'on'
  } catch {
    return false
  }
}

export function setNotifyEnabled(enabled: boolean): void {
  if (typeof window === 'undefined') return
  try {
    window.localStorage.setItem(NOTIFY_STORAGE_KEY, enabled ? 'on' : 'off')
  } catch {
    // localStorage can throw in private browsing / sandboxed iframes.
  }
}

function resolveTaskTitle(queryClient: QueryClient, taskId: string): string {
  for (const [, data] of queryClient.getQueriesData<Task[] | Task>({ queryKey: ['tasks'] })) {
    if (Array.isArray(data)) {
      const match = data.find((task) => task.id === taskId)
      if (match) return match.title
    } else if (data?.id === taskId) {
      return data.title
    }
  }
  return taskId.slice(0, 8)
}

/**
 * Subscribes to task WebSocket events and surfaces them as browser
 * notifications when the user has both granted permission and enabled the
 * in-app setting. Safe to mount/unmount freely: it uses the shared
 * WebSocketContext and the app-wide QueryClient.
 */
export function useTaskNotifications(): void {
  const queryClient = useQueryClient()
  const { t } = useTranslation('notify')

  const handleEvent = useCallback(
    (event: ProtocolEvent) => {
      if (
        !notificationsSupported() ||
        Notification.permission !== 'granted' ||
        !isNotifyEnabled()
      ) {
        return
      }

      const evt = dispatchEvent(event)
      if (!evt) return

      let title: string
      let body: string
      let tag: string

      if (evt.type === 'task.created') {
        const { task } = evt.data
        title = t('created.title')
        body = t('created.body', { title: task.title })
        tag = task.id
      } else if (evt.type === 'task.status_changed') {
        const { task_id, from, to } = evt.data
        if (from === to) return
        title = t(`status.${to}.title`)
        body = t(`status.${to}.body`, { title: resolveTaskTitle(queryClient, task_id) })
        tag = task_id
      } else {
        return
      }

      const notification = new Notification(title, { body, tag })
      notification.onclick = () => {
        window.focus()
      }
    },
    [queryClient, t]
  )

  useWebSocket(handleEvent)
}
