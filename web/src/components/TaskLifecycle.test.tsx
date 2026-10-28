/**
 * NewTaskForm "Start immediately" toggle → POST body, and the Board
 * "Show archived" filter toggle → list query.
 *
 * The form goes through Refine's useForm (dataProvider.create → POST
 * /api/tasks), so the toggle's value must reach the wire body. The board
 * test drives the toolbar toggle and asserts the follow-up list query hits
 * `?archived=1`.
 */
import { describe, expect, it, vi, afterEach } from 'vitest'
import { act, render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { Refine } from '@refinedev/core'
import routerProvider from '@refinedev/react-router'
import { NewTaskForm } from './NewTaskForm.tsx'
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

const node = {
  id: 'n-1',
  name: 'node-1',
  status: 'online',
  agent_types: ['codex', 'omp'],
}

/** Node lookup plus the task creation POST the form issues. */
const mockFormFetch = (nodeBody: unknown = node) => {
  fetchMock.mockImplementation((input: unknown) => {
    const url = String(input)
    if (url === '/api/nodes/n-1') return Promise.resolve(json(nodeBody))
    return Promise.resolve(json(createdTask()))
  })
}

const taskPost = () =>
  fetchMock.mock.calls.find(([url, init]) => url === '/api/tasks' && init?.method === 'POST')

/** The node query seeds the first registered agent, not a hardcoded fake. */
const waitForAgentDefault = () =>
  waitFor(() =>
    expect(screen.getByText('codex', { selector: '.ant-select-selection-item' })).toBeTruthy()
  )

const createdTask = (overrides: Record<string, unknown> = {}) => ({
  id: 't-1',
  workspace_id: 'w-1',
  title: 'Backlog item',
  status: 'waiting',
  agent_type: 'fake',
  session_id: '',
  sort_order: 0,
  auto_start: false,
  created_at: '2026-09-28T00:00:00Z',
  updated_at: '2026-09-28T00:00:00Z',
  ...overrides,
})

function renderWithProviders(
  ui: React.ReactNode,
  subscribe: (listener: (event: ProtocolEvent) => void) => () => void = () => () => {},
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
) {
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <Refine
          dataProvider={dataProvider}
          routerProvider={routerProvider}
          options={{ reactQuery: { clientConfig: client } }}
        >
          <WebSocketContext.Provider
            value={{
              connected: false,
              connectionState: 'closed',
              error: null,
              subscribe,
            }}
          >
            {ui}
          </WebSocketContext.Provider>
        </Refine>
      </MemoryRouter>
    </QueryClientProvider>
  )
}

describe('NewTaskForm auto_start toggle', () => {
  it('sends auto_start: false when the switch is turned off', async () => {
    mockFormFetch()
    const onClose = vi.fn()
    renderWithProviders(<NewTaskForm workspaceId="w-1" nodeId="n-1" onClose={onClose} />)

    fireEvent.change(await screen.findByPlaceholderText('Leave empty — the agent will name it'), {
      target: { value: 'Backlog item' },
    })
    fireEvent.change(screen.getByPlaceholderText('Describe the task in detail…'), {
      target: { value: 'Do the thing.' },
    })
    // "Start immediately" defaults on; turning it off creates a backlog task.
    fireEvent.click(screen.getByRole('switch', { name: 'Start immediately' }))
    await waitForAgentDefault()
    fireEvent.click(screen.getByRole('button', { name: 'Create' }))

    // The node lookup runs alongside; locate the task POST rather than
    // assuming it is the only request.
    await waitFor(() => expect(taskPost()).toBeTruthy())
    expect(JSON.parse(String(taskPost()?.[1]?.body))).toMatchObject({ auto_start: false })
  })
})

describe('NewTaskForm agent type', () => {
  it('preselects the node’s default_adapter over the first registered agent', async () => {
    // agent_types is sorted; the operator's default need not be first.
    mockFormFetch({ ...node, default_adapter: 'omp' })
    renderWithProviders(<NewTaskForm workspaceId="w-1" nodeId="n-1" onClose={() => {}} />)

    fireEvent.change(await screen.findByPlaceholderText('Leave empty — the agent will name it'), {
      target: { value: 'Run with the default agent' },
    })
    fireEvent.change(screen.getByPlaceholderText('Describe the task in detail…'), {
      target: { value: 'Do the thing.' },
    })
    await waitFor(() =>
      expect(screen.getByText('omp', { selector: '.ant-select-selection-item' })).toBeTruthy()
    )
    fireEvent.click(screen.getByRole('button', { name: 'Create' }))

    await waitFor(() => expect(taskPost()).toBeTruthy())
    expect(JSON.parse(String(taskPost()?.[1]?.body))).toMatchObject({ agent_type: 'omp' })
  })

  it('falls back to the first registered agent when the default is not offered', async () => {
    mockFormFetch({ ...node, default_adapter: 'stale-agent' })
    renderWithProviders(<NewTaskForm workspaceId="w-1" nodeId="n-1" onClose={() => {}} />)

    fireEvent.change(screen.getByPlaceholderText('Describe the task in detail…'), {
      target: { value: 'Do the thing.' },
    })
    await waitForAgentDefault()
  })

  it('offers only the node’s registered agents and submits the picked one', async () => {
    mockFormFetch()
    renderWithProviders(<NewTaskForm workspaceId="w-1" nodeId="n-1" onClose={() => {}} />)

    fireEvent.change(await screen.findByPlaceholderText('Leave empty — the agent will name it'), {
      target: { value: 'Run with omp' },
    })
    fireEvent.change(screen.getByPlaceholderText('Describe the task in detail…'), {
      target: { value: 'Do the thing.' },
    })
    // The agent field is an AutoComplete: picking a suggestion must reach the
    // POST body. It previously lived inside a wrapper <div>, so Form.Item never
    // received the value and every task fell back to the `fake` default.
    await waitForAgentDefault()
    const agentInput = screen.getByRole('combobox')
    // Open the list and pick an option without typing: a selection alone fires
    // no bubbling `change` event, which is how the wrapper <div> used to lose
    // the value.
    fireEvent.mouseDown(agentInput)
    const ompOption = await screen.findByTitle('omp')
    expect(screen.queryByTitle('fake')).toBeNull()
    fireEvent.click(ompOption)
    fireEvent.click(screen.getByRole('button', { name: 'Create' }))

    await waitFor(() => expect(taskPost()).toBeTruthy())
    expect(JSON.parse(String(taskPost()?.[1]?.body))).toMatchObject({ agent_type: 'omp' })
  })
})

describe('NewTaskForm title', () => {
  it('leaves the title empty and omits it from the create body', async () => {
    mockFormFetch()
    renderWithProviders(<NewTaskForm workspaceId="w-1" nodeId="n-1" onClose={() => {}} />)

    fireEvent.change(screen.getByPlaceholderText('Describe the task in detail…'), {
      target: { value: 'Update the flake inputs\n\nand rebuild' },
    })
    const title = screen.getByPlaceholderText(
      'Leave empty — the agent will name it'
    ) as HTMLInputElement
    expect(title.value).toBe('')

    await waitForAgentDefault()
    fireEvent.click(screen.getByRole('button', { name: 'Create' }))
    await waitFor(() => expect(taskPost()).toBeTruthy())
    const body = JSON.parse(String(taskPost()?.[1]?.body))
    // The hub derives a provisional title from the prompt and lets the agent
    // replace it, so an empty field must not send a user-chosen title.
    expect(body.title ?? '').toBe('')
  })

  it('keeps an operator-entered title instead of mirroring the prompt', async () => {
    mockFormFetch()
    renderWithProviders(<NewTaskForm workspaceId="w-1" nodeId="n-1" onClose={() => {}} />)

    const title = screen.getByPlaceholderText(
      'Leave empty — the agent will name it'
    ) as HTMLInputElement
    fireEvent.change(title, { target: { value: 'Custom label' } })
    fireEvent.change(screen.getByPlaceholderText('Describe the task in detail…'), {
      target: { value: 'Update the flake inputs' },
    })
    expect(title.value).toBe('Custom label')

    await waitForAgentDefault()
    fireEvent.click(screen.getByRole('button', { name: 'Create' }))
    await waitFor(() => expect(taskPost()).toBeTruthy())
    expect(JSON.parse(String(taskPost()?.[1]?.body))).toMatchObject({ title: 'Custom label' })
  })
})

describe('Board failure column', () => {
  it('groups failed tasks with awaiting_user under "Needs attention"', async () => {
    fetchMock.mockImplementation((input: unknown) => {
      const url = String(input)
      if (url.startsWith('/api/tasks')) {
        return Promise.resolve(
          json([
            createdTask({ id: 't-failed', title: 'Pi crashed', status: 'failed' }),
            createdTask({ id: 't-done', title: 'Finished work', status: 'done' }),
          ])
        )
      }
      throw new Error(`unexpected fetch: ${url}`)
    })
    renderWithProviders(<Board workspaceId="w-1" nodeId="n-1" />)

    const attention = await screen.findByRole('region', { name: 'Needs attention tasks' })
    expect(within(attention).getByText('Pi crashed')).toBeTruthy()
    expect(within(attention).queryByText('Finished work')).toBeNull()

    const done = screen.getByRole('region', { name: 'Done tasks' })
    expect(within(done).getByText('Finished work')).toBeTruthy()
    expect(within(done).queryByText('Pi crashed')).toBeNull()
  })
})

describe('Board live title', () => {
  it('replaces the provisional title when the agent names the task', async () => {
    fetchMock.mockImplementation((input: unknown) => {
      const url = String(input)
      if (url.startsWith('/api/tasks')) {
        return Promise.resolve(json([createdTask({ title: 'Fix the parser' })]))
      }
      throw new Error(`unexpected fetch: ${url}`)
    })
    let emit: ((event: ProtocolEvent) => void) | undefined
    renderWithProviders(<Board workspaceId="w-1" nodeId="n-1" />, (listener) => {
      emit = listener
      return () => {}
    })

    expect(await screen.findByText('Fix the parser')).toBeTruthy()
    act(() => {
      emit?.({
        type: 'task.title_changed',
        data: { task_id: 't-1', title: 'Parser recovery' },
      } as ProtocolEvent)
    })
    expect(await screen.findByText('Parser recovery')).toBeTruthy()
    expect(screen.queryByText('Fix the parser')).toBeNull()
  })
})

describe('Board live status while searching', () => {
  it('moves a card rendered from the search cache when its status changes', async () => {
    fetchMock.mockImplementation((input: unknown) => {
      const url = String(input)
      if (url.startsWith('/api/tasks')) {
        return Promise.resolve(json([createdTask({ status: 'running', title: 'Fix the parser' })]))
      }
      throw new Error(`unexpected fetch: ${url}`)
    })
    let emit: ((event: ProtocolEvent) => void) | undefined
    renderWithProviders(<Board workspaceId="w-1" nodeId="n-1" />, (listener) => {
      emit = listener
      return () => {}
    })

    expect(await screen.findByText('Fix the parser')).toBeTruthy()
    // An active search renders a query key other than ['tasks', workspaceId];
    // the status event must patch that cache too.
    fireEvent.change(screen.getByLabelText('Search task titles'), { target: { value: 'Fix' } })
    await waitFor(() =>
      expect(fetchMock.mock.calls.some(([url]) => String(url).includes('q=Fix'))).toBe(true)
    )
    expect(await screen.findByText('Fix the parser')).toBeTruthy()

    act(() => {
      emit?.({
        type: 'task.status_changed',
        data: { task_id: 't-1', from: 'running', to: 'done' },
      } as ProtocolEvent)
    })
    const done = screen.getByRole('region', { name: 'Done tasks' })
    await waitFor(() => expect(within(done).getByText('Fix the parser')).toBeTruthy())
  })
})

describe('Board task ordering', () => {
  it('moves a live-updated card to the top of its column', async () => {
    fetchMock.mockImplementation((input: unknown) => {
      const url = String(input)
      if (url.startsWith('/api/tasks')) {
        return Promise.resolve(
          json([
            createdTask({ id: 't-1', title: 'Older', status: 'done' }),
            createdTask({ id: 't-2', title: 'Newer', status: 'done' }),
          ])
        )
      }
      throw new Error(`unexpected fetch: ${url}`)
    })
    let emit: ((event: ProtocolEvent) => void) | undefined
    renderWithProviders(<Board workspaceId="w-1" nodeId="n-1" />, (listener) => {
      emit = listener
      return () => {}
    })

    const done = await screen.findByRole('region', { name: 'Done tasks' })
    expect(
      within(done)
        .getAllByRole('button', { name: /^Task / })
        .map((card) => card.getAttribute('aria-label'))
    ).toEqual(['Task Older, status Done', 'Task Newer, status Done'])

    act(() => {
      emit?.({
        type: 'task.title_changed',
        data: { task_id: 't-2', title: 'Newer, renamed' },
      } as ProtocolEvent)
    })
    // The hub orders by updated_at DESC; the live patch must reorder too, not
    // just replace the title in place.
    await waitFor(() =>
      expect(
        within(done)
          .getAllByRole('button', { name: /^Task / })
          .map((card) => card.getAttribute('aria-label'))
      ).toEqual(['Task Newer, renamed, status Done', 'Task Older, status Done'])
    )
  })
})

describe('Board remount freshness', () => {
  it('re-reads the task list on remount instead of trusting the stale cache', async () => {
    // The board only listens to WS events while mounted and task deltas are not
    // gap-filled, so a status change that lands while the operator is on a
    // session page must be recovered by the remount fetch. The hostile client
    // below pins a long staleTime to prove the board's own refetchOnMount
    // guarantee, not the global default.
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false, staleTime: 5 * 60_000 } },
    })
    const tasks = [createdTask({ status: 'running', title: 'Long running task' })]
    fetchMock.mockImplementation((input: unknown) => {
      const url = String(input)
      if (url.startsWith('/api/tasks')) return Promise.resolve(json(tasks))
      throw new Error(`unexpected fetch: ${url}`)
    })

    const first = renderWithProviders(<Board workspaceId="w-1" nodeId="n-1" />, undefined, client)
    const running = await screen.findByRole('region', { name: 'Running tasks' })
    expect(within(running).getByText('Long running task')).toBeTruthy()
    first.unmount()

    // The task finishes while the board is unmounted; no WS listener exists.
    tasks[0] = createdTask({ status: 'done', title: 'Long running task' })

    renderWithProviders(<Board workspaceId="w-1" nodeId="n-1" />, undefined, client)
    await waitFor(() =>
      expect(
        within(screen.getByRole('region', { name: 'Done tasks' })).getByText('Long running task')
      ).toBeTruthy()
    )
    // Card subtasks are seeded separately; only the board's task-list read
    // must be proven to re-run on remount.
    expect(
      fetchMock.mock.calls.filter(([url]) => String(url).includes('/api/tasks?workspace_id='))
    ).toHaveLength(2)
    expect(
      within(screen.getByRole('region', { name: 'Running tasks' })).queryByText('Long running task')
    ).toBeNull()
  })
})

describe('Board archived filter', () => {
  it('refetches with ?archived=1 when "Show archived" is toggled', async () => {
    fetchMock.mockImplementation((input: unknown) => {
      const url = String(input)
      if (url.startsWith('/api/tasks')) {
        // Archived view: only a done+archived row; default view: empty.
        return Promise.resolve(
          json(
            url.includes('archived=1')
              ? [createdTask({ status: 'done', archived_at: '2026-09-28T01:00:00Z' })]
              : []
          )
        )
      }
      throw new Error(`unexpected fetch: ${url}`)
    })
    renderWithProviders(<Board workspaceId="w-1" nodeId="n-1" />)

    // Default view: the archived row is excluded, so the board is empty.
    await waitFor(() => expect(screen.getByText('No tasks yet')).toBeTruthy())
    expect(fetchMock.mock.calls[0][0]).toBe('/api/tasks?workspace_id=w-1')

    fireEvent.click(screen.getByRole('button', { name: 'Show archived' }))
    // The toggle flips to the archived-only listing…
    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some(([url]) => url === '/api/tasks?workspace_id=w-1&archived=1')
      ).toBe(true)
    )
    // …and the archived card renders with an Unarchive action.
    expect(await screen.findByText('Backlog item')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Unarchive Backlog item' })).toBeTruthy()
    // Toggling back hides archived rows again.
    fireEvent.click(screen.getByRole('button', { name: 'Hide archived' }))
    await waitFor(() => expect(screen.getByText('No tasks yet')).toBeTruthy())
  })
})
