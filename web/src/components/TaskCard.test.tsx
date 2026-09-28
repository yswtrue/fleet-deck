import { describe, expect, it, vi, beforeAll, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { message } from 'antd'
import { TaskCard } from './TaskCard.tsx'
import type { Task } from '../types.ts'

const task: Task = {
  id: 't-1',
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

const fetchMock = vi.fn()

beforeAll(() => {
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  fetchMock.mockReset()
})

function renderCard(cardTask: Task = task) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <TaskCard task={cardTask} />
      </MemoryRouter>
    </QueryClientProvider>
  )
}

/** Mirrors the board: the toast holder lives outside the (remount-prone) card. */
function HostHarness({ showCard }: { showCard: boolean }) {
  const [messageApi, messageHolder] = message.useMessage()
  return (
    <div>
      {messageHolder}
      {showCard ? <TaskCard task={task} messageApi={messageApi} /> : null}
    </div>
  )
}

describe('TaskCard', () => {
  it('offers only hub-legal transitions; done is terminal', async () => {
    const first = renderCard()
    fireEvent.click(screen.getByRole('button', { name: 'Change status of Fix the flaky test' }))
    await waitFor(() => expect(screen.getByText('Move to Running')).toBeTruthy())
    expect(screen.getByText('Move to Awaiting you')).toBeTruthy()
    expect(screen.getByText('Move to Done')).toBeTruthy()
    expect(screen.queryByText('Move to Waiting')).toBeNull()
    first.unmount()

    renderCard({ ...task, status: 'done' })
    expect(screen.queryByRole('button', { name: 'Change status of Fix the flaky test' })).toBeNull()
  })

  it('PATCHes {status} and confirms with a success toast', async () => {
    fetchMock.mockResolvedValue(
      new Response(JSON.stringify({ ...task, status: 'done' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    )
    renderCard()
    fireEvent.click(screen.getByRole('button', { name: 'Change status of Fix the flaky test' }))
    fireEvent.click(await screen.findByText('Move to Done'))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    expect(fetchMock.mock.calls[0][0]).toBe('/api/tasks/t-1')
    expect(fetchMock.mock.calls[0][1]).toMatchObject({
      method: 'PATCH',
      body: JSON.stringify({ status: 'done' }),
    })
    // The toast is the user-visible confirmation; it must actually mount
    // (the antd static message API renders nothing under React 19).
    await waitFor(() => expect(document.body.textContent).toContain('moved to Done'))
  })

  it('surfaces a hub 409 as an error toast', async () => {
    fetchMock.mockResolvedValue(
      new Response(JSON.stringify({ error: 'illegal status transition' }), {
        status: 409,
        headers: { 'Content-Type': 'application/json' },
      })
    )
    renderCard()
    fireEvent.click(screen.getByRole('button', { name: 'Change status of Fix the flaky test' }))
    fireEvent.click(await screen.findByText('Move to Running'))
    await waitFor(() =>
      expect(document.body.textContent).toContain('HTTP 409: illegal status transition')
    )
  })

  it('a session-less card shows a badge and toasts on click instead of doing nothing', async () => {
    renderCard({ ...task, session_id: '' })
    // The badge makes the state visible before any click.
    expect(document.body.textContent).toContain('No session yet')
    fireEvent.click(screen.getByRole('button', { name: 'Task Fix the flaky test, status Waiting' }))
    await waitFor(() =>
      expect(document.body.textContent).toContain('Task has no session yet — not dispatched')
    )
    // A plain click never mutates.
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('toast survives the card unmounting before the PATCH settles (column move)', async () => {
    fetchMock.mockResolvedValue(
      new Response(JSON.stringify({ ...task, status: 'done' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    )
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const ui = (showCard: boolean) => (
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <HostHarness showCard={showCard} />
        </MemoryRouter>
      </QueryClientProvider>
    )
    const { rerender } = render(ui(true))
    fireEvent.click(screen.getByRole('button', { name: 'Change status of Fix the flaky test' }))
    fireEvent.click(await screen.findByText('Move to Done'))
    // A status move relocates the card to another column, remounting it. Unmount
    // it synchronously — before the PATCH continuation runs — and the
    // confirmation must still reach the host holder.
    rerender(ui(false))
    await waitFor(() => expect(document.body.textContent).toContain('moved to Done'), {
      timeout: 3000,
    })
  })

  it('a backlog task shows a Backlog chip (not the no-session badge) and starts via POST', async () => {
    fetchMock.mockResolvedValue(
      new Response(JSON.stringify({ ...task, status: 'running', session_id: 's-9' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    )
    renderCard({ ...task, status: 'waiting', session_id: '', auto_start: false })
    // Backlog state is visible before any click; the generic no-session badge
    // stays hidden so the two states are not confused.
    expect(document.body.textContent).toContain('Backlog')
    expect(document.body.textContent).not.toContain('No session yet')
    fireEvent.click(screen.getByRole('button', { name: 'Start Fix the flaky test' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    expect(fetchMock.mock.calls[0][0]).toBe('/api/tasks/t-1/start')
    expect(fetchMock.mock.calls[0][1]).toMatchObject({ method: 'POST' })
    await waitFor(() => expect(document.body.textContent).toContain('started'))
  })

  it('surfaces a start 409 as an error toast', async () => {
    fetchMock.mockResolvedValue(
      new Response(JSON.stringify({ error: 'task is not waiting' }), {
        status: 409,
        headers: { 'Content-Type': 'application/json' },
      })
    )
    renderCard({ ...task, status: 'waiting', session_id: '', auto_start: false })
    fireEvent.click(screen.getByRole('button', { name: 'Start Fix the flaky test' }))
    await waitFor(() =>
      expect(document.body.textContent).toContain('HTTP 409: task is not waiting')
    )
  })

  it('a done card offers Archive; an archived card offers Unarchive instead', async () => {
    fetchMock.mockResolvedValue(
      new Response(
        JSON.stringify({ ...task, status: 'done', archived_at: '2026-09-28T01:00:00Z' }),
        {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }
      )
    )
    const first = renderCard({ ...task, status: 'done' })
    fireEvent.click(screen.getByRole('button', { name: 'Archive Fix the flaky test' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    expect(fetchMock.mock.calls[0][1]).toMatchObject({
      method: 'PATCH',
      body: JSON.stringify({ archived: true }),
    })
    await waitFor(() => expect(document.body.textContent).toContain('archived'))
    first.unmount()
    fetchMock.mockClear()

    renderCard({ ...task, status: 'done', archived_at: '2026-09-28T01:00:00Z' })
    expect(screen.queryByRole('button', { name: 'Archive Fix the flaky test' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Unarchive Fix the flaky test' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    expect(fetchMock.mock.calls[0][1]).toMatchObject({
      method: 'PATCH',
      body: JSON.stringify({ archived: false }),
    })
  })
})
