import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { Users } from './Users.tsx'

const admin = {
  id: 'u-admin',
  email: 'admin@example.com',
  display_name: 'Admin',
  role: 'admin',
  created_at: '2026-01-01T00:00:00Z',
}
const alice = {
  id: 'u-1',
  email: 'alice@example.com',
  display_name: 'Alice',
  role: 'user',
  created_at: '2026-01-02T00:00:00Z',
}
const nodeA = { id: 'n-1', name: 'node-a', host: 'host-a' }
const nodeB = { id: 'n-2', name: 'node-b', host: 'host-b' }

interface RecordedCall {
  method: string
  url: string
  body: unknown
}

/**
 * Routes hub requests by URL, recording every call. Bindings: Alice is bound
 * to node-a, the admin to nothing.
 */
function mockHub() {
  const calls: RecordedCall[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      const method = init?.method ?? 'GET'
      calls.push({ method, url, body: init?.body ? JSON.parse(String(init.body)) : undefined })
      let body: unknown = null
      if (url === '/api/users') body = [admin, alice]
      else if (url === '/api/nodes') body = [nodeA, nodeB]
      else if (url === '/api/users/u-1/nodes') body = [nodeA]
      else if (url === '/api/users/u-admin/nodes') body = []
      else if (url === '/api/users/u-1') body = alice
      return Promise.resolve(
        new Response(JSON.stringify(body), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      )
    })
  )
  return calls
}

function renderUsers() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={queryClient}>
      <Users />
    </QueryClientProvider>
  )
}

describe('Users', () => {
  beforeEach(() => localStorage.clear())
  afterEach(() => {
    localStorage.clear()
    vi.unstubAllGlobals()
  })

  it('shows each user’s bound nodes in the table', async () => {
    mockHub()
    renderUsers()

    expect(await screen.findByText('alice@example.com')).toBeTruthy()
    // Alice's chip; the admin row shows the empty-state marker.
    expect(await screen.findByText('node-a')).toBeTruthy()
    expect(screen.getByText('None')).toBeTruthy()
  })

  it('pre-selects the edit modal’s bindings and Save writes the full binding set', async () => {
    const calls = mockHub()
    renderUsers()

    fireEvent.click((await screen.findAllByRole('button', { name: 'Edit' }))[1])

    // Alice's current binding is pre-selected (resolved from GET …/nodes).
    expect(await screen.findByText('node-a — host-a')).toBeTruthy()

    // The option list comes from GET /api/nodes: open the multi-select.
    const selector = document.querySelector('.ant-select-multiple .ant-select-selector')
    expect(selector).toBeTruthy()
    fireEvent.mouseDown(selector!)
    expect(await screen.findByText('node-b — host-b')).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => {
      expect(
        calls.some(
          (call) =>
            call.method === 'PUT' &&
            call.url === '/api/users/u-1/nodes' &&
            JSON.stringify(call.body) === JSON.stringify({ node_ids: ['n-1'] })
        )
      ).toBe(true)
    })
    // The user PATCH precedes the binding replace.
    const patchIndex = calls.findIndex(
      (call) => call.method === 'PATCH' && call.url === '/api/users/u-1'
    )
    const putIndex = calls.findIndex(
      (call) => call.method === 'PUT' && call.url === '/api/users/u-1/nodes'
    )
    expect(patchIndex).toBeGreaterThanOrEqual(0)
    expect(putIndex).toBeGreaterThan(patchIndex)
  })
})
