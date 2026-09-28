import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { Usage } from './Usage.tsx'

const statsBody = {
  total: { prompt_tokens: 12520, completion_tokens: 8180, total_tokens: 20700 },
  by_model: [
    {
      model: 'gpt-x',
      prompt_tokens: 12400,
      completion_tokens: 8100,
      total_tokens: 20500,
      turns: 7,
    },
    { model: 'fake', prompt_tokens: 120, completion_tokens: 80, total_tokens: 200, turns: 3 },
  ],
}

/** Routes hub requests: stats come from `body`, everything else is empty. */
function mockHub(body: unknown) {
  const fetchMock = vi.fn((input: RequestInfo | URL, _init?: RequestInit) => {
    const url = String(input)
    const payload = url.startsWith('/api/stats/tokens') ? body : []
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

function renderUsage() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={queryClient}>
      <Usage />
    </QueryClientProvider>
  )
}

afterEach(() => vi.unstubAllGlobals())

describe('Usage', () => {
  it('renders summary cards and the by-model table with compact numbers', async () => {
    mockHub(statsBody)
    renderUsage()

    expect(await screen.findByText('gpt-x')).toBeTruthy()
    expect(screen.getByText('fake')).toBeTruthy()

    // Summary cards.
    expect(screen.getByText('Prompt tokens')).toBeTruthy()
    expect(screen.getByText('Completion tokens')).toBeTruthy()
    expect(screen.getByText('Total tokens')).toBeTruthy()
    // "Turns" labels both the summary card and the table column.
    expect(screen.getAllByText('Turns')).toHaveLength(2)
    expect(screen.getByTitle('12520').textContent).toBe('12.5k')
    expect(screen.getByTitle('8180').textContent).toBe('8.2k')
    expect(screen.getByTitle('20700').textContent).toBe('20.7k')

    // Table cells: compact display, exact value as the tooltip.
    expect(screen.getByTitle('12400').textContent).toBe('12.4k')
    expect(screen.getByTitle('20500').textContent).toBe('20.5k')
    expect(screen.getByText('Model')).toBeTruthy()
    expect(screen.getByText('Completion')).toBeTruthy()
  })

  it('shows the empty state when no usage has been recorded', async () => {
    mockHub({
      total: { prompt_tokens: 0, completion_tokens: 0, total_tokens: 0 },
      by_model: [],
    })
    renderUsage()

    expect(await screen.findByText('No usage recorded yet')).toBeTruthy()
    expect(screen.queryByText('gpt-x')).toBeNull()
  })
})
