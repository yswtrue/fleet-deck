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
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { Refine } from '@refinedev/core'
import routerProvider from '@refinedev/react-router'
import { NewTaskForm } from './NewTaskForm.tsx'
import { Board } from './Board.tsx'
import { WebSocketContext } from '../hooks/WebSocketContext.ts'
import { dataProvider } from '../dataProvider.ts'

const fetchMock = vi.fn()
vi.stubGlobal('fetch', fetchMock)

afterEach(() => {
  fetchMock.mockReset()
})

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

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

function renderWithProviders(ui: React.ReactNode) {
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
            {ui}
          </WebSocketContext.Provider>
        </Refine>
      </MemoryRouter>
    </QueryClientProvider>
  )
}

describe('NewTaskForm auto_start toggle', () => {
  it('sends auto_start: false when the switch is turned off', async () => {
    fetchMock.mockImplementation(() => Promise.resolve(json(createdTask())))
    const onClose = vi.fn()
    renderWithProviders(<NewTaskForm workspaceId="w-1" onClose={onClose} />)

    fireEvent.change(await screen.findByPlaceholderText('What should the agent do?'), {
      target: { value: 'Backlog item' },
    })
    fireEvent.change(screen.getByPlaceholderText('Describe the task in detail…'), {
      target: { value: 'Do the thing.' },
    })
    // "Start immediately" defaults on; turning it off creates a backlog task.
    fireEvent.click(screen.getByRole('switch', { name: 'Start immediately' }))
    fireEvent.click(screen.getByRole('button', { name: 'Create' }))

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/tasks')
    expect(init?.method).toBe('POST')
    expect(JSON.parse(String(init?.body))).toMatchObject({ auto_start: false })
  })
})

describe('NewTaskForm agent type', () => {
  it('submits the agent picked in the suggestion list', async () => {
    fetchMock.mockImplementation(() => Promise.resolve(json(createdTask())))
    renderWithProviders(<NewTaskForm workspaceId="w-1" onClose={() => {}} />)

    fireEvent.change(await screen.findByPlaceholderText('What should the agent do?'), {
      target: { value: 'Run with omp' },
    })
    fireEvent.change(screen.getByPlaceholderText('Describe the task in detail…'), {
      target: { value: 'Do the thing.' },
    })
    // The agent field is an AutoComplete: picking a suggestion must reach the
    // POST body. It previously lived inside a wrapper <div>, so Form.Item never
    // received the value and every task fell back to the `fake` default.
    const agentInput = screen.getByRole('combobox')
    // Open the list and pick an option without typing: a selection alone fires
    // no bubbling `change` event, which is how the wrapper <div> used to lose
    // the value (leaving the `fake` initialValue in the form store).
    fireEvent.mouseDown(agentInput)
    fireEvent.click(await screen.findByTitle('omp'))
    fireEvent.click(screen.getByRole('button', { name: 'Create' }))

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/tasks')
    expect(JSON.parse(String(init?.body))).toMatchObject({ agent_type: 'omp' })
  })
})

describe('NewTaskForm derived title', () => {
  it('fills the title from the prompt and submits it', async () => {
    fetchMock.mockImplementation(() => Promise.resolve(json(createdTask())))
    renderWithProviders(<NewTaskForm workspaceId="w-1" onClose={() => {}} />)

    fireEvent.change(screen.getByPlaceholderText('Describe the task in detail…'), {
      target: { value: 'Update the flake inputs\n\nand rebuild' },
    })
    const title = screen.getByPlaceholderText('What should the agent do?') as HTMLInputElement
    expect(title.value).toBe('Update the flake inputs')

    fireEvent.click(screen.getByRole('button', { name: 'Create' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    const body = JSON.parse(String(fetchMock.mock.calls[0][1]?.body))
    expect(body).toMatchObject({ title: 'Update the flake inputs' })
  })

  it('stops mirroring the prompt once the title is edited', async () => {
    fetchMock.mockImplementation(() => Promise.resolve(json(createdTask())))
    renderWithProviders(<NewTaskForm workspaceId="w-1" onClose={() => {}} />)

    const title = screen.getByPlaceholderText('What should the agent do?') as HTMLInputElement
    fireEvent.change(title, { target: { value: 'Custom label' } })
    fireEvent.change(screen.getByPlaceholderText('Describe the task in detail…'), {
      target: { value: 'Update the flake inputs' },
    })
    expect(title.value).toBe('Custom label')
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
    renderWithProviders(<Board workspaceId="w-1" />)

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
