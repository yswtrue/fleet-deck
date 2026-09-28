import { describe, expect, it, vi } from 'vitest'
import { dispatchEvent } from './dispatchEvent.ts'
import type { Event as ProtocolEvent } from '../gen/protocol/index.ts'

// dispatchEvent is the typed WS dispatcher (locked Lane C code): it validates
// one hub Event and narrows it to a DispatchedEvent. These tests pin the
// consumer-visible contract: a valid event for each discriminator must narrow,
// and payloads missing their required fields must be dropped instead of
// crashing a handler or silently corrupting state.

const evt = (type: string, data: unknown): ProtocolEvent => ({
  type,
  data: data as never,
  ts: '2026-09-28T00:00:00Z',
})

const valid: Array<[string, unknown, string]> = [
  ['node.status_changed', { node_id: 'n-1', status: 'online' }, 'node_id'],
  ['task.created', { task: { id: 't-1' } }, 'task'],
  ['task.status_changed', { task_id: 't-1', from: 'waiting', to: 'running' }, 'task_id'],
  ['subtask.updated', { task_id: 't-1', subtask: { id: 's-1' } }, 'task_id'],
  ['session.delta', { session_id: 'sess-1', text: 'hello' }, 'session_id'],
  ['session.message', { session_id: 'sess-1', message: { id: 'm-1' } }, 'message'],
  ['session.state_changed', { session_id: 'sess-1', status: 'running' }, 'session_id'],
  ['session.user_input_requested', { session_id: 'sess-1', prompt: 'q?' }, 'session_id'],
]

describe('dispatchEvent', () => {
  it.each(valid)('narrows %s and passes the payload through', (type, data, markerKey) => {
    const got = dispatchEvent(evt(type, data))
    expect(got).not.toBeNull()
    expect(got!.type).toBe(type)
    expect((got!.data as unknown as Record<string, unknown>)[markerKey]).toBe(
      (data as Record<string, unknown>)[markerKey]
    )
  })

  const malformed: Array<[string, unknown]> = [
    ['node.status_changed', { node_id: 'n-1' }],
    ['task.created', { task: 'not-a-record' }],
    ['task.created', { task: {} }],
    ['task.status_changed', { task_id: 't-1', from: 'waiting' }],
    ['subtask.updated', { task_id: 't-1', subtask: {} }],
    ['session.delta', { session_id: 'sess-1' }],
    ['session.message', { session_id: 'sess-1', message: { no_id: true } }],
    ['session.state_changed', { session_id: 'sess-1', status: 42 }],
    ['session.user_input_requested', { session_id: 'sess-1' }],
  ]

  it.each(malformed)('drops %s payloads missing required fields', (type, data) => {
    expect(dispatchEvent(evt(type, data))).toBeNull()
  })

  it('drops events whose data is not a record', () => {
    expect(dispatchEvent(evt('session.delta', null))).toBeNull()
    expect(dispatchEvent(evt('session.delta', 'text'))).toBeNull()
    expect(dispatchEvent(evt('session.delta', 7))).toBeNull()
  })

  it('returns null for unknown event types (forward compatibility)', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    try {
      expect(dispatchEvent(evt('session.future_thing', { session_id: 's' }))).toBeNull()
      // Unknown types are not malformed — no drop warning is emitted.
      expect(warn).not.toHaveBeenCalled()
    } finally {
      warn.mockRestore()
    }
  })

  it('warns when dropping a malformed payload', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    try {
      expect(dispatchEvent(evt('session.delta', {}))).toBeNull()
      expect(warn).toHaveBeenCalledOnce()
    } finally {
      warn.mockRestore()
    }
  })
})
