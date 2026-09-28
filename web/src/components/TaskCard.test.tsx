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
    expect(fetchMock.mock.calls[0][1]).toMatchObject({ method: 'PATCH', body: JSON.stringify({ status: 'done' }) })
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
    await waitFor(() => expect(document.body.textContent).toContain('HTTP 409: illegal status transition'))
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
    await waitFor(() => expect(document.body.textContent).toContain('moved to Done'), { timeout: 3000 })
  })
})
