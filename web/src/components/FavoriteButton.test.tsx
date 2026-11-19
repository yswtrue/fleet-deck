import { afterEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { FavoriteButton } from './FavoriteButton.tsx'
import type { Workspace } from '../types.ts'

const workspace: Workspace = {
  id: 'w-1',
  node_id: 'n-1',
  name: 'Repo',
  path: '/home/me/repo',
  auto_allow_scope: 'none',
  created_at: '2026-01-01T00:00:00Z',
}

/** Stateful stub: the favorites list reflects the last PUT/DELETE. */
function mockHub() {
  let favorites: Workspace[] = []
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    if (url.startsWith('/api/workspaces/favorites')) {
      return Promise.resolve(
        new Response(JSON.stringify(favorites), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      )
    }
    if (url.endsWith('/favorite')) {
      favorites = init?.method === 'PUT' ? [workspace] : []
      return Promise.resolve(new Response(null, { status: 204 }))
    }
    return Promise.resolve(new Response('{}', { status: 200 }))
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function renderButton() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={queryClient}>
      <FavoriteButton workspace={workspace} />
    </QueryClientProvider>
  )
}

afterEach(() => vi.unstubAllGlobals())

describe('FavoriteButton', () => {
  it('toggles the bookmark and persists both directions', async () => {
    const fetchMock = mockHub()
    renderButton()

    fireEvent.click(await screen.findByRole('button', { name: 'Add to favorites' }))
    expect(await screen.findByRole('button', { name: 'Remove from favorites' })).toBeTruthy()
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/workspaces/w-1/favorite',
      expect.objectContaining({ method: 'PUT' })
    )

    fireEvent.click(screen.getByRole('button', { name: 'Remove from favorites' }))
    expect(await screen.findByRole('button', { name: 'Add to favorites' })).toBeTruthy()
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/workspaces/w-1/favorite',
      expect.objectContaining({ method: 'DELETE' })
    )
  })
})
