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

function snapshotWith(status: SessionSnapshot['session']['status']): SessionSnapshot {
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
  } as unknown as SessionSnapshot
}

function renderView(sessionStatus: 'done' | 'failed' | 'running') {
  fetchMock.mockImplementation((input: unknown) => {
    const url = String(input)
    if (url === '/api/sessions/s-1') return Promise.resolve(json(snapshotWith(sessionStatus)))
    if (url.startsWith('/api/sessions/s-1/messages')) return Promise.resolve(json([]))
    if (url.startsWith('/api/sessions/s-1/events')) return Promise.resolve(json([]))
    if (url === '/api/nodes') return Promise.resolve(json([]))
    if (url.startsWith('/api/tasks/t-1/subtasks')) return Promise.resolve(json({ subtasks: [] }))
    if (url === '/api/sessions/s-1/send') return Promise.resolve(json({}))
    throw new Error(`unexpected fetch: ${url}`)
  })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <Refine dataProvider={dataProvider} routerProvider={routerProvider}>
          <WebSocketContext.Provider
            value={{
              connected: false,
              connectionState: 'closed',
              seq: 0,
              lastSeq: 0,
              error: null,
              subscribe: () => () => {},
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

describe('SessionView running session', () => {
  it('keeps the composer disabled with the waiting copy', async () => {
    renderView('running')
    const box = await screen.findByPlaceholderText('Waiting for the agent to ask a question…')
    expect((box as HTMLTextAreaElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: 'Send' }) as HTMLButtonElement).disabled).toBe(true)
    expect(fetchMock.mock.calls.some(([url]) => url === '/api/sessions/s-1/send')).toBe(false)
  })
})
