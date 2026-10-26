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
function HostHarness({ showCard, cardTask = task }: { showCard: boolean; cardTask?: Task }) {
  const [messageApi, messageHolder] = message.useMessage()
  return (
    <div>
      {messageHolder}
      {showCard ? <TaskCard task={cardTask} messageApi={messageApi} /> : null}
    </div>
  )
}

describe('TaskCard', () => {
  it('a waiting card acts through Start and Archive only — no move menu', () => {
    renderCard({ ...task, session_id: '' })
    expect(screen.queryByRole('button', { name: 'Change status of Fix the flaky test' })).toBeNull()
    expect(screen.getByRole('button', { name: 'Start Fix the flaky test' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Archive Fix the flaky test' })).toBeTruthy()
  })

  it('offers only hub-legal transitions; awaiting_user is agent-triggered; done is terminal', async () => {
    const first = renderCard({ ...task, status: 'running' })
    fireEvent.click(screen.getByRole('button', { name: 'Change status of Fix the flaky test' }))
    await waitFor(() => expect(screen.getByText('Move to Waiting')).toBeTruthy())
    expect(screen.getByText('Move to Done')).toBeTruthy()
    // The agent alone parks a task in awaiting_user: it is never a manual target.
    expect(screen.queryByText('Move to Awaiting you')).toBeNull()
    first.unmount()

    const second = renderCard({ ...task, status: 'awaiting_user' })
    fireEvent.click(screen.getByRole('button', { name: 'Change status of Fix the flaky test' }))
    await waitFor(() => expect(screen.getByText('Move to Running')).toBeTruthy())
    expect(screen.getByText('Move to Done')).toBeTruthy()
    expect(screen.queryByText('Move to Waiting')).toBeNull()
    second.unmount()

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
    renderCard({ ...task, status: 'running' })
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
    renderCard({ ...task, status: 'awaiting_user' })
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
          <HostHarness showCard={showCard} cardTask={{ ...task, status: 'running' }} />
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

  it('a waiting card archives via PATCH {archived:true} (shelve without finishing)', async () => {
    fetchMock.mockResolvedValue(
      new Response(
        JSON.stringify({ ...task, status: 'waiting', archived_at: '2026-09-28T01:00:00Z' }),
        {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }
      )
    )
    renderCard({ ...task, session_id: '' })
    fireEvent.click(screen.getByRole('button', { name: 'Archive Fix the flaky test' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    expect(fetchMock.mock.calls[0][1]).toMatchObject({
      method: 'PATCH',
      body: JSON.stringify({ archived: true }),
    })
    await waitFor(() => expect(document.body.textContent).toContain('archived'))
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

  it('an unstarted task opens the edit modal and PATCHes its content', async () => {
    fetchMock.mockResolvedValue(
      new Response(
        JSON.stringify({
          ...task,
          session_id: '',
          title: 'New label',
          prompt: 'New instruction',
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } }
      )
    )
    renderCard({ ...task, session_id: '', status: 'waiting' })
    fireEvent.click(screen.getByRole('button', { name: 'Edit Fix the flaky test' }))

    const title = await screen.findByRole('textbox', { name: 'Title (optional)' })
    const prompt = screen.getByRole('textbox', { name: 'Prompt' })
    expect((title as HTMLInputElement).value).toBe('Fix the flaky test')
    expect((prompt as HTMLTextAreaElement).value).toBe('Investigate and fix.')
    fireEvent.change(title, { target: { value: 'New label' } })
    fireEvent.change(prompt, { target: { value: 'New instruction' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    expect(fetchMock.mock.calls[0][0]).toBe('/api/tasks/t-1')
    expect(fetchMock.mock.calls[0][1]).toMatchObject({ method: 'PATCH' })
    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({
      prompt: 'New instruction',
      title: 'New label',
    })
    await waitFor(() => expect(document.body.textContent).toContain('“New label” updated'))
  })

  it('an untouched title is omitted so an auto title keeps following the prompt', async () => {
    fetchMock.mockResolvedValue(
      new Response(JSON.stringify({ ...task, session_id: '', prompt: 'New instruction' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    )
    renderCard({ ...task, session_id: '', status: 'waiting' })
    fireEvent.click(screen.getByRole('button', { name: 'Edit Fix the flaky test' }))
    fireEvent.change(await screen.findByRole('textbox', { name: 'Prompt' }), {
      target: { value: 'New instruction' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({ prompt: 'New instruction' })
  })

  it('a dispatched task offers no edit action (the agent already holds the prompt)', () => {
    renderCard({ ...task, session_id: 's-1' })
    expect(screen.queryByRole('button', { name: 'Edit Fix the flaky test' })).toBeNull()
  })
})
