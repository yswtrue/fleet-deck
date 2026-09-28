import { afterEach, describe, expect, it, vi } from 'vitest'
import { apiClient } from './apiClient.ts'

const statsBody = {
  total: { prompt_tokens: 120, completion_tokens: 80, total_tokens: 200 },
  by_model: [
    { model: 'fake', prompt_tokens: 120, completion_tokens: 80, total_tokens: 200, turns: 3 },
  ],
}

function mockFetch() {
  const fetchMock = vi.fn((_input: RequestInfo | URL, _init?: RequestInit) =>
    Promise.resolve(
      new Response(JSON.stringify(statsBody), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    )
  )
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

afterEach(() => vi.unstubAllGlobals())

describe('getStatsTokens', () => {
  it('queries the bare endpoint and passes the body through when unfiltered', async () => {
    const fetchMock = mockFetch()

    await expect(apiClient.getStatsTokens()).resolves.toEqual(statsBody)
    expect(String(fetchMock.mock.calls[0][0])).toBe('/api/stats/tokens')
  })

  it('encodes every filter into the query string', async () => {
    const fetchMock = mockFetch()

    await apiClient.getStatsTokens({
      session_id: 's-1',
      workspace_id: 'w-1',
      node_id: 'n-1',
      from: '2026-01-01T00:00:00Z',
      to: '2026-01-31T23:59:59Z',
    })

    const url = new URL(String(fetchMock.mock.calls[0][0]), 'http://hub.test')
    expect(url.pathname).toBe('/api/stats/tokens')
    expect(Object.fromEntries(url.searchParams)).toEqual({
      session_id: 's-1',
      workspace_id: 'w-1',
      node_id: 'n-1',
      from: '2026-01-01T00:00:00Z',
      to: '2026-01-31T23:59:59Z',
    })
  })

  it('leaves absent filters out of the query string', async () => {
    const fetchMock = mockFetch()

    await apiClient.getStatsTokens({ session_id: 's-1', from: '2026-01-01T00:00:00Z' })

    const url = new URL(String(fetchMock.mock.calls[0][0]), 'http://hub.test')
    expect(url.searchParams.get('session_id')).toBe('s-1')
    expect(url.searchParams.get('from')).toBe('2026-01-01T00:00:00Z')
    expect(url.searchParams.has('workspace_id')).toBe(false)
    expect(url.searchParams.has('node_id')).toBe(false)
    expect(url.searchParams.has('to')).toBe(false)
  })
})
