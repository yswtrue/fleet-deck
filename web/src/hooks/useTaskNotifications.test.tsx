import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { WebSocketContext } from './WebSocketContext.ts'
import { useTaskNotifications, setNotifyEnabled } from './useTaskNotifications.ts'
import type { ContextValue } from './WebSocketContext.ts'
import type { Event as ProtocolEvent } from '../gen/protocol/index.ts'
import type { Task } from '../types.ts'

class FakeNotification {
  static permission: NotificationPermission = 'default'
  static requestPermission = vi.fn().mockResolvedValue('default')
  static instances: FakeNotification[] = []
  title: string
  options?: NotificationOptions
  onclick: (() => void) | null = null
  constructor(title: string, options?: NotificationOptions) {
    this.title = title
    this.options = options
    FakeNotification.instances.push(this)
  }
  close() {}
}

const task: Task = {
  id: 't-deadbeef',
  workspace_id: 'w-1',
  title: 'Fix the flaky test',
  prompt: 'Investigate and fix.',
  status: 'waiting',
  agent_type: 'fake',
  session_id: 's-1',
  sort_order: 0,
  auto_start: true,
  created_at: '2026-09-28T00:00:00Z',
  updated_at: '2026-09-28T00:00:00Z',
}

const listeners = new Set<(event: ProtocolEvent) => void>()

const fakeContext: ContextValue = {
  connected: true,
  connectionState: 'open',
  seq: 1,
  lastSeq: 0,
  error: null,
  subscribe(listener) {
    listeners.add(listener)
    return () => listeners.delete(listener)
  },
}

function Bridge() {
  useTaskNotifications()
  return null
}

function emit(event: ProtocolEvent) {
  listeners.forEach((listener) => listener(event))
}

function event(type: string, data: unknown): ProtocolEvent {
  return { type, data, ts: '2026-09-28T00:00:00Z' }
}

function renderBridge({
  permission,
  enabled,
  tasks,
}: {
  permission: NotificationPermission
  enabled: boolean
  tasks?: Task[]
}) {
  FakeNotification.permission = permission
  FakeNotification.instances = []
  FakeNotification.requestPermission.mockClear()
  listeners.clear()
  if (enabled) setNotifyEnabled(true)
  else setNotifyEnabled(false)

  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  if (tasks) client.setQueryData(['tasks', 'w-1'], tasks)

  return render(
    <QueryClientProvider client={client}>
      <WebSocketContext.Provider value={fakeContext}>
        <Bridge />
      </WebSocketContext.Provider>
    </QueryClientProvider>
  )
}

beforeEach(() => {
  vi.stubGlobal('Notification', FakeNotification)
})

afterEach(() => {
  vi.unstubAllGlobals()
  localStorage.clear()
  listeners.clear()
})

describe('useTaskNotifications', () => {
  it('fires a notification for task.created when enabled and permission granted', () => {
    renderBridge({ permission: 'granted', enabled: true })
    emit(event('task.created', { task }))

    expect(FakeNotification.instances).toHaveLength(1)
    const [n] = FakeNotification.instances
    expect(n.title).toBe('New task')
    expect(n.options?.body).toContain(task.title)
    expect(n.options?.tag).toBe(task.id)
  })

  it('resolves the title from cache for task.status_changed', () => {
    renderBridge({ permission: 'granted', enabled: true, tasks: [task] })
    emit(event('task.status_changed', { task_id: task.id, from: 'waiting', to: 'done' }))

    expect(FakeNotification.instances).toHaveLength(1)
    const [n] = FakeNotification.instances
    expect(n.title).toBe('Task done')
    expect(n.options?.body).toBe(task.title)
  })

  it('falls back to a short id when the task is not cached', () => {
    renderBridge({ permission: 'granted', enabled: true })
    emit(event('task.status_changed', { task_id: task.id, from: 'waiting', to: 'done' }))

    expect(FakeNotification.instances).toHaveLength(1)
    const [n] = FakeNotification.instances
    expect(n.options?.body).toBe(task.id.slice(0, 8))
  })

  it('skips task.status_changed when from equals to', () => {
    renderBridge({ permission: 'granted', enabled: true })
    emit(event('task.status_changed', { task_id: task.id, from: 'running', to: 'running' }))

    expect(FakeNotification.instances).toHaveLength(0)
  })

  it('does nothing when permission is not granted', () => {
    renderBridge({ permission: 'default', enabled: true })
    emit(event('task.created', { task }))

    expect(FakeNotification.instances).toHaveLength(0)
  })

  it('does nothing when the notification setting is off', () => {
    renderBridge({ permission: 'granted', enabled: false })
    emit(event('task.created', { task }))

    expect(FakeNotification.instances).toHaveLength(0)
  })

  it('focuses the window when the notification is clicked', () => {
    renderBridge({ permission: 'granted', enabled: true })
    emit(event('task.created', { task }))

    const focus = vi.spyOn(window, 'focus').mockImplementation(() => {})
    const [n] = FakeNotification.instances
    n.onclick?.()
    expect(focus).toHaveBeenCalledOnce()
    focus.mockRestore()
  })

  it('coalesces repeated updates via the task id tag', () => {
    renderBridge({ permission: 'granted', enabled: true })
    emit(event('task.status_changed', { task_id: task.id, from: 'waiting', to: 'running' }))
    emit(event('task.status_changed', { task_id: task.id, from: 'running', to: 'done' }))

    expect(FakeNotification.instances).toHaveLength(2)
    expect(FakeNotification.instances[0]?.options?.tag).toBe(task.id)
    expect(FakeNotification.instances[1]?.options?.tag).toBe(task.id)
  })

  it('ignores unrelated WebSocket events', () => {
    renderBridge({ permission: 'granted', enabled: true })
    emit(event('node.status_changed', { node_id: 'n-1', status: 'online' }))

    expect(FakeNotification.instances).toHaveLength(0)
  })

  it('drops malformed task events instead of crashing', () => {
    renderBridge({ permission: 'granted', enabled: true })
    emit(event('task.created', { task: { no_id: true } }))
    emit(event('task.status_changed', { task_id: task.id, from: 'waiting' }))

    expect(FakeNotification.instances).toHaveLength(0)
  })
})
