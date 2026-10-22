import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { SessionUsage } from './SessionUsage.tsx'

function mockStats(body: unknown) {
  const fetchMock = vi.fn((_input: RequestInfo | URL, _init?: RequestInit) =>
    Promise.resolve(
      new Response(JSON.stringify(body), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    )
  )
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function renderPanel() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={queryClient}>
      <SessionUsage sessionId="s-1" />
    </QueryClientProvider>
  )
}

afterEach(() => vi.unstubAllGlobals())

describe('SessionUsage', () => {
  it('fetches the session-scoped stats and renders per-model token lines', async () => {
    const fetchMock = mockStats({
      total: { prompt_tokens: 120, completion_tokens: 80, total_tokens: 200 },
      by_model: [
        {
          model: 'fake',
          prompt_tokens: 50,
          completion_tokens: 80,
          total_tokens: 200,
          cached_read_tokens: 40,
          cached_write_tokens: 10,
          cache_prompt_tokens: 50,
          cache_reports: 1,
          turns: 3,
        },
      ],
    })
    renderPanel()

    expect(await screen.findByText('fake · 50 in / 80 out / 200 total')).toBeTruthy()
    expect(screen.getByText('cache 40 read / 10 write · 40% hit')).toBeTruthy()
    expect(String(fetchMock.mock.calls[0][0])).toBe('/api/stats/tokens?session_id=s-1')
  })

  it('shows no cache telemetry when the agent never reported it', async () => {
    mockStats({
      total: { prompt_tokens: 120, completion_tokens: 80, total_tokens: 200 },
      by_model: [
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
    })
    renderPanel()

    expect(await screen.findByText('cache —')).toBeTruthy()
  })

  it('shows the empty state before any tokens are recorded', async () => {
    mockStats({
      total: { prompt_tokens: 0, completion_tokens: 0, total_tokens: 0 },
      by_model: [],
    })
    renderPanel()

    expect(await screen.findByText('No usage yet')).toBeTruthy()
  })
})
