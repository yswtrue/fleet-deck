/**
 * The board renders each card's mirrored agent todos as subtask dots. Those
 * rows live in Postgres, so a board the operator opens after a turn ran must
 * seed them from REST; only a task watched from the start would see live
 * `subtask.updated` frames.
 */
import { describe, expect, it, vi, afterEach } from 'vitest'
import { act, render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { Refine } from '@refinedev/core'
import routerProvider from '@refinedev/react-router'
import { Board } from './Board.tsx'
import { WebSocketContext } from '../hooks/WebSocketContext.ts'
import { dataProvider } from '../dataProvider.ts'
import type { Event as ProtocolEvent } from '../gen/protocol/index.ts'

const fetchMock = vi.fn()
vi.stubGlobal('fetch', fetchMock)

afterEach(() => {
  fetchMock.mockReset()
})

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const runningTask = {
  id: 't-1',
  workspace_id: 'w-1',
  title: 'Refactor the parser',
  status: 'running',
  agent_type: 'fake',
  session_id: 's-1',
  sort_order: 0,
  auto_start: true,
  created_at: '2026-09-28T00:00:00Z',
  updated_at: '2026-09-28T00:00:00Z',
}

const subtask = (id: string, title: string, status: string, sortOrder: number) => ({
  id,
  task_id: 't-1',
  title,
  status,
  sort_order: sortOrder,
})

function renderBoard(subscribe: (listener: (event: ProtocolEvent) => void) => () => void) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <Refine
          dataProvider={dataProvider}
          routerProvider={routerProvider}
          options={{ reactQuery: { clientConfig: client } }}
        >
          <WebSocketContext.Provider
            value={{ connected: true, connectionState: 'open', error: null, subscribe }}
          >
            <Board workspaceId="w-1" nodeId="n-1" />
          </WebSocketContext.Provider>
        </Refine>
      </MemoryRouter>
    </QueryClientProvider>
  )
}

describe('Board subtask progress', () => {
  it('loads existing subtasks for the cards and applies live updates', async () => {
    fetchMock.mockImplementation((input: unknown) => {
      const url = String(input)
      if (url.startsWith('/api/tasks?workspace_id=')) return Promise.resolve(json([runningTask]))
      if (url === '/api/tasks/t-1/subtasks') {
        return Promise.resolve(
          json({
            subtasks: [
              subtask('st-1', 'Research', 'done', 0),
              subtask('st-2', 'Implement', 'in_progress', 1),
            ],
          })
        )
      }
      throw new Error(`unexpected fetch: ${url}`)
    })

    let emit: ((event: ProtocolEvent) => void) | undefined
    renderBoard((listener) => {
      emit = listener
      return () => {}
    })

    const running = await screen.findByRole('region', { name: 'Running tasks' })
    expect(await within(running).findByText('1/2 done')).toBeTruthy()

    act(() => {
      emit?.({
        type: 'subtask.updated',
        data: { task_id: 't-1', subtask: subtask('st-2', 'Implement', 'done', 1) },
      } as ProtocolEvent)
    })
    expect(await within(running).findByText('2/2 done')).toBeTruthy()
  })

  it('does not fetch subtasks for backlog cards that never had a session', async () => {
    fetchMock.mockImplementation((input: unknown) => {
      const url = String(input)
      if (url.startsWith('/api/tasks?workspace_id=')) {
        return Promise.resolve(
          json([{ ...runningTask, id: 't-2', title: 'Later', status: 'waiting', session_id: '' }])
        )
      }
      throw new Error(`unexpected fetch: ${url}`)
    })

    renderBoard(() => () => {})

    const waiting = await screen.findByRole('region', { name: 'Waiting tasks' })
    expect(await within(waiting).findByText('Later')).toBeTruthy()
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/subtasks'))).toBe(false)
  })
})
