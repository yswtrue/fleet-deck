import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { Dashboard } from './Dashboard.tsx'

const taskBody = {
  created: 12,
  completed: 8,
  by_status: [
    { status: 'waiting', count: 4 },
    { status: 'running', count: 6 },
    { status: 'awaiting_user', count: 1 },
    { status: 'done', count: 5 },
  ],
}

const tokenBody = {
  total: { prompt_tokens: 12520, completion_tokens: 8180, total_tokens: 20700 },
  by_model: [
    {
      model: 'gpt-x',
      prompt_tokens: 12400,
      completion_tokens: 8100,
      total_tokens: 20500,
      cached_read_tokens: 6200,
      cached_write_tokens: 0,
      cache_prompt_tokens: 12400,
      cache_reports: 7,
      turns: 7,
    },
    {
      model: 'fake',
      prompt_tokens: 120,
      completion_tokens: 80,
      total_tokens: 200,
      cached_read_tokens: 0,
      cached_write_tokens: 0,
      cache_prompt_tokens: 0,
      cache_reports: 0,
      turns: 3,
    },
  ],
}

/** Routes hub requests: task stats and token stats bodies, everything else empty. */
function mockHub(tasks: unknown, tokens: unknown) {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input)
    const payload = url.startsWith('/api/stats/tasks')
      ? tasks
      : url.startsWith('/api/stats/tokens')
        ? tokens
        : []
    return Promise.resolve(
      new Response(JSON.stringify(payload), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    )
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function renderDashboard() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={queryClient}>
      <Dashboard />
    </QueryClientProvider>
  )
}

afterEach(() => vi.unstubAllGlobals())

describe('Dashboard', () => {
  it("renders today's task stats and token stats", async () => {
    mockHub(taskBody, tokenBody)
    renderDashboard()

    expect(await screen.findByText('Tasks today')).toBeTruthy()
    expect(await screen.findByText('Created')).toBeTruthy()
    expect(screen.getByText('Token usage today')).toBeTruthy()

    // Task summary cards: created / completed with their counts.
    expect(screen.getByText('Created')).toBeTruthy()
    expect(screen.getByText('Completed')).toBeTruthy()
    expect(screen.getByTitle('12').textContent).toBe('12')
    expect(screen.getByTitle('8').textContent).toBe('8')

    // By-status strip: localized labels with per-status counts.
    expect(screen.getByLabelText("Today's tasks by status")).toBeTruthy()
    for (const label of ['Waiting', 'Running', 'Awaiting you', 'Done']) {
      expect(screen.getByText(label)).toBeTruthy()
    }
    expect(screen.getByTitle('4').textContent).toBe('4')
    expect(screen.getByTitle('6').textContent).toBe('6')
    expect(screen.getByTitle('5').textContent).toBe('5')

    // Token section: fleet totals, turn count and the per-model table.
    expect(screen.getByText('Prompt tokens')).toBeTruthy()
    expect(screen.getByText('Completion tokens')).toBeTruthy()
    expect(screen.getByText('Total tokens')).toBeTruthy()
    expect(screen.getAllByText('Turns')).toHaveLength(2)
    expect((await screen.findByTitle('20700')).textContent).toBe('20.7k')
    expect(screen.getByTitle('10').textContent).toBe('10') // 7 + 3 turns
    expect(screen.getByText('gpt-x')).toBeTruthy()
    expect(screen.getByText('fake')).toBeTruthy()
    expect(screen.getByTitle('20500').textContent).toBe('20.5k')
    expect(screen.getByText('Cache (read / hit)')).toBeTruthy()
    expect(screen.getByText('6.2k · 33%')).toBeTruthy()
  })

  it('renders zeroed stats and the empty usage state when nothing happened today', async () => {
    mockHub(
      { created: 0, completed: 0, by_status: [] },
      { total: { prompt_tokens: 0, completion_tokens: 0, total_tokens: 0 }, by_model: [] }
    )
    renderDashboard()

    expect(await screen.findByText('Tasks today')).toBeTruthy()
    // Each section is awaited in turn so both queries have landed.
    expect(await screen.findByText('Created')).toBeTruthy()
    expect(await screen.findByText('No usage recorded yet')).toBeTruthy()
    // 2 task cards + 5 status counts + 4 token tiles, all zero.
    expect(screen.getAllByTitle('0')).toHaveLength(11)
    expect(screen.queryByText('gpt-x')).toBeNull()
  })
})
