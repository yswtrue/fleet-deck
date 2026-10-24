/**
 * SessionView composer gating per session status.
 *
 * SessionView mixes Refine's useOne (session snapshot), react-query (messages)
 * and the app WebSocketContext. This test drives the composer through two
 * finished states:
 * - `done`: the composer is enabled with the "continue the conversation" copy
 *   and sending the same send API lets the hub reopen the session;
 * - `running`: the composer stays disabled with the "waiting for the agent" copy.
 */
import { describe, expect, it, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { Refine } from '@refinedev/core'
import routerProvider from '@refinedev/react-router'
import { SessionView } from './SessionView.tsx'
import { WebSocketContext } from '../hooks/WebSocketContext.ts'
import { dataProvider } from '../dataProvider.ts'
import type { SessionSnapshot } from '../apiClient.ts'

const fetchMock = vi.fn()
vi.stubGlobal('fetch', fetchMock)

afterEach(() => {
  fetchMock.mockReset()
})

const doneTask = {
  id: 't-1',
  workspace_id: 'w-1',
  title: 'Continuable task',
  status: 'done',
  agent_type: 'fake',
  session_id: 's-1',
  sort_order: 0,
  auto_start: true,
  created_at: '2026-09-28T00:00:00Z',
  updated_at: '2026-09-28T00:00:00Z',
}

function snapshotWith(
  status: SessionSnapshot['session']['status'],
  partial = '',
  thought = ''
): SessionSnapshot {
  return {
    session: {
      id: 's-1',
      task_id: 't-1',
      node_id: 'n-1',
      agent_type: 'fake',
      status,
      external_session_id: 'ext-1',
      workspace_path: '/tmp/w',
      created_at: '2026-09-28T00:00:00Z',
      updated_at: '2026-09-28T00:00:00Z',
    },
    task: { ...doneTask, status: 'done' },
    pending_input: '',
    partial_output: partial,
    partial_thought: thought,
  } as unknown as SessionSnapshot
}

function renderView(
  sessionStatus: SessionSnapshot['session']['status'],
  opts: {
    pending?: string
    send?: () => Promise<Response>
    connected?: boolean
    messages?: unknown
    partial?: string
    thought?: string
    subtasks?: unknown[]
    subscribe?: (handler: (event: never) => void) => () => void
  } = {}
) {
  const snapshot = snapshotWith(sessionStatus, opts.partial, opts.thought)
  if (opts.pending) snapshot.pending_input = opts.pending
  fetchMock.mockImplementation((input: unknown) => {
    const url = String(input)
    if (url === '/api/sessions/s-1') return Promise.resolve(json(snapshot))
    if (url.startsWith('/api/sessions/s-1/messages'))
      return Promise.resolve(json(opts.messages ?? []))
    if (url.startsWith('/api/sessions/s-1/events')) return Promise.resolve(json([]))
    if (url === '/api/nodes') return Promise.resolve(json([]))
    if (url.startsWith('/api/tasks/t-1/subtasks'))
      return Promise.resolve(json({ subtasks: opts.subtasks ?? [] }))
    if (url === '/api/sessions/s-1/send') return (opts.send ?? (() => Promise.resolve(json({}))))()
    throw new Error(`unexpected fetch: ${url}`)
  })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <Refine dataProvider={dataProvider} routerProvider={routerProvider}>
          <WebSocketContext.Provider
            value={{
              connected: opts.connected ?? false,
              connectionState: opts.connected ? 'open' : 'closed',
              seq: 0,
              lastSeq: 0,
              error: null,
              subscribe: opts.subscribe ?? (() => () => {}),
            }}
          >
            <SessionView sessionId="s-1" />
          </WebSocketContext.Provider>
        </Refine>
      </MemoryRouter>
    </QueryClientProvider>
  )
}

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

describe.each(['done', 'failed'] as const)('SessionView finished session (%s)', (status) => {
  it('enables the composer with continue copy and sends via the send API', async () => {
    renderView(status)
    const box = await screen.findByPlaceholderText(
      'Continue the conversation… (Enter to send, Shift+Enter for a new line)'
    )
    expect((box as HTMLTextAreaElement).disabled).toBe(false)
    fireEvent.change(box, { target: { value: 'one more thing' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() =>
      expect(fetchMock.mock.calls.some(([url]) => url === '/api/sessions/s-1/send')).toBe(true)
    )
    const sendCall = fetchMock.mock.calls.find(([url]) => url === '/api/sessions/s-1/send')
    expect(sendCall?.[1]).toMatchObject({
      method: 'POST',
      body: JSON.stringify({ message: 'one more thing' }),
    })
  })
})

const PERMISSION_PROMPT =
  'nix flake update 2>&1 | tail -40\n\n[permission_request] options=' +
  '[{"id":"allow_once","label":"Allow once"},{"id":"reject_once","label":"Reject"}]'

describe('SessionView permission ask', () => {
  it('replies with the picked option and keeps the card until the hub answers', async () => {
    renderView('awaiting_user', { pending: PERMISSION_PROMPT, connected: true })

    fireEvent.click(await screen.findByRole('button', { name: 'Allow once' }))
    await waitFor(() =>
      expect(fetchMock.mock.calls.some(([url]) => url === '/api/sessions/s-1/send')).toBe(true)
    )
    const sendCall = fetchMock.mock.calls.find(([url]) => url === '/api/sessions/s-1/send')
    expect(sendCall?.[1]).toMatchObject({
      method: 'POST',
      body: JSON.stringify({ message: '[permission_response] option_id="allow_once"' }),
    })
  })

  it('keeps the picker and reports a rejected reply instead of failing silently', async () => {
    renderView('awaiting_user', {
      pending: PERMISSION_PROMPT,
      connected: true,
      send: () => Promise.resolve(json({ error: 'session is running' }, 409)),
    })

    fireEvent.click(await screen.findByRole('button', { name: 'Allow once' }))
    await waitFor(() => expect(document.body.textContent).toContain('HTTP 409: session is running'))
    // The card (and its buttons) must survive the failed reply.
    expect(await screen.findByRole('button', { name: /Allow once/ })).toBeTruthy()
  })

  it('still sends when the history endpoint has no messages yet (null cache)', async () => {
    // A live session's buffer starts empty; an older hub answered null, which
    // crashed the optimistic cache update before the reply left the browser.
    renderView('awaiting_user', {
      pending: PERMISSION_PROMPT,
      connected: true,
      messages: null,
    })

    fireEvent.click(await screen.findByRole('button', { name: /Allow once/ }))
    await waitFor(() =>
      expect(fetchMock.mock.calls.some(([url]) => url === '/api/sessions/s-1/send')).toBe(true)
    )
  })
})

describe('SessionView reasoning trace', () => {
  it('renders the live thought stream and drops it once the answer lands', async () => {
    // Several hooks subscribe to the socket; broadcast to all of them.
    const subscribers: Array<(event: unknown) => void> = []
    renderView('running', {
      connected: true,
      subscribe: (handler) => {
        subscribers.push(handler as (event: unknown) => void)
        return () => {
          const i = subscribers.indexOf(handler as (event: unknown) => void)
          if (i >= 0) subscribers.splice(i, 1)
        }
      },
    })
    await waitFor(() => expect(subscribers.length).toBeGreaterThan(0))
    const emit = (event: unknown) => subscribers.forEach((h) => h(event))

    emit({ type: 'session.thought', data: { session_id: 's-1', text: 'weighing the options' } })
    expect(await screen.findByText('weighing the options')).toBeTruthy()

    emit({
      type: 'session.state_changed',
      data: { session_id: 's-1', status: 'awaiting_user' },
    })
    // A permission pause keeps the trace.
    expect(screen.queryByText('weighing the options')).toBeTruthy()

    emit({
      type: 'session.message',
      data: {
        session_id: 's-1',
        message: { id: 'm-9', session_id: 's-1', role: 'assistant', content: 'done' },
      },
    })
    await waitFor(() => expect(screen.queryByText('weighing the options')).toBeNull())
  })
})

describe('SessionView running session', () => {
  it('keeps the composer disabled with the waiting copy', async () => {
    renderView('running')
    const box = await screen.findByPlaceholderText('Waiting for the agent to ask a question…')
    expect((box as HTMLTextAreaElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: 'Send' }) as HTMLButtonElement).disabled).toBe(true)
    expect(fetchMock.mock.calls.some(([url]) => url === '/api/sessions/s-1/send')).toBe(false)
  })

  it('restores the in-progress stream from the snapshot after a reload', async () => {
    renderView('running', { connected: true, partial: 'Half an answer, still streaming' })
    // The live delta is not in the durable event stream; only the snapshot's
    // partial_output can bring it back on a mid-turn refresh.
    expect(await screen.findByText('Half an answer, still streaming')).toBeTruthy()
  })

  it('restores the reasoning trace from the snapshot after a reload', async () => {
    renderView('running', { connected: true, thought: 'weighing the options' })
    expect(await screen.findByText('weighing the options')).toBeTruthy()
  })
})

describe('SessionView subtasks', () => {
  it('applies a subtask.updated frame without waiting for a refetch', async () => {
    const subscribers: Array<(event: unknown) => void> = []
    renderView('running', {
      connected: true,
      subtasks: [
        { id: 'st-1', task_id: 't-1', title: 'Research', status: 'pending', sort_order: 0 },
      ],
      subscribe: (handler) => {
        subscribers.push(handler as (event: unknown) => void)
        return () => {
          const i = subscribers.indexOf(handler as (event: unknown) => void)
          if (i >= 0) subscribers.splice(i, 1)
        }
      },
    })
    expect(await screen.findByText('Pending')).toBeTruthy()
    const before = fetchMock.mock.calls.filter(([url]) =>
      String(url).startsWith('/api/tasks/t-1/subtasks')
    ).length

    subscribers.forEach((h) =>
      h({
        type: 'subtask.updated',
        data: {
          task_id: 't-1',
          subtask: { id: 'st-1', task_id: 't-1', title: 'Research', status: 'done', sort_order: 0 },
        },
      })
    )

    expect(await screen.findByText('Done')).toBeTruthy()
    expect(
      fetchMock.mock.calls.filter(([url]) => String(url).startsWith('/api/tasks/t-1/subtasks'))
        .length
    ).toBe(before)
  })
})
