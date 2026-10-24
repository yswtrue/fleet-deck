import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  apiClient,
  authenticatedWsUrl,
  clearToken,
  getStoredToken,
  shouldRedirectToLogin,
  storeToken,
} from './apiClient.ts'

const jsonResponse = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

function mockFetch(impl: (url: string, init?: RequestInit) => Response | Promise<Response>) {
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) =>
    Promise.resolve(impl(String(input), init))
  )
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

describe('token store', () => {
  beforeEach(() => localStorage.clear())
  afterEach(() => localStorage.clear())

  it('round-trips the token and expiry through localStorage', () => {
    storeToken('tok-1', '2026-10-28T00:00:00Z')
    expect(getStoredToken()).toBe('tok-1')
    expect(localStorage.getItem('fleetdeck.token')).toBe('tok-1')
    expect(localStorage.getItem('fleetdeck.expires_at')).toBe('2026-10-28T00:00:00Z')

    clearToken()
    expect(getStoredToken()).toBeNull()
    expect(localStorage.getItem('fleetdeck.token')).toBeNull()
    expect(localStorage.getItem('fleetdeck.expires_at')).toBeNull()
  })

  it('treats an expired token as absent and clears it', () => {
    storeToken('tok-old', '2020-01-01T00:00:00Z')
    expect(getStoredToken()).toBeNull()
    expect(localStorage.getItem('fleetdeck.token')).toBeNull()
  })

  it('attaches the token to the WebSocket URL and drops it when signed out', () => {
    clearToken()
    expect(authenticatedWsUrl()).not.toContain('token=')
    storeToken('tok/2', '2026-10-28T00:00:00Z')
    expect(authenticatedWsUrl()).toContain('?token=tok%2F2')
  })
})

describe('401 redirect policy', () => {
  it('redirects to /login except from /login itself (no loop)', () => {
    expect(shouldRedirectToLogin('/')).toBe(true)
    expect(shouldRedirectToLogin('/workspaces/w-1')).toBe(true)
    expect(shouldRedirectToLogin('/login')).toBe(false)
  })
})

describe('fetchJson auth behavior', () => {
  beforeEach(() => {
    localStorage.clear()
    // Keep jsdom on /login so the 401 handler never attempts real navigation.
    window.history.replaceState({}, '', '/login')
  })
  afterEach(() => {
    localStorage.clear()
    window.history.replaceState({}, '', '/')
    vi.unstubAllGlobals()
  })

  it('sends the bearer token when one is stored', async () => {
    storeToken('tok-3', '2026-10-28T00:00:00Z')
    const fetchMock = mockFetch(() => jsonResponse({ id: 'u-1' }))

    await apiClient.me()

    const [url, init] = fetchMock.mock.calls[0]
    const headers = (init?.headers ?? {}) as Record<string, string>
    expect(url).toBe('/api/auth/me')
    expect(headers.Authorization).toBe('Bearer tok-3')
  })

  it('omits the Authorization header when signed out', async () => {
    const fetchMock = mockFetch(() => jsonResponse({ status: 'ok' }))

    await apiClient.getHealth()

    const headers = (fetchMock.mock.calls[0][1]?.headers ?? {}) as Record<string, string>
    expect(headers.Authorization).toBeUndefined()
  })

  it('clears the token and surfaces the error body on 401', async () => {
    storeToken('tok-stale', '2026-10-28T00:00:00Z')
    mockFetch(() => jsonResponse({ error: 'unauthorized' }, 401))

    await expect(apiClient.me()).rejects.toThrow('unauthorized')
    expect(getStoredToken()).toBeNull()
  })

  it('surfaces backend guard messages (409) for the UI to toast', async () => {
    mockFetch(() => jsonResponse({ error: 'cannot remove the last admin' }, 409))

    await expect(apiClient.deleteUser('u-9')).rejects.toThrow('cannot remove the last admin')
  })

  it('resolves empty-body responses (204) without JSON parsing', async () => {
    storeToken('tok-4', '2026-10-28T00:00:00Z')
    mockFetch(() => new Response(null, { status: 204 }))

    await expect(apiClient.logout()).resolves.toBeUndefined()
  })
})

describe('user ↔ node bindings', () => {
  beforeEach(() => {
    localStorage.clear()
    window.history.replaceState({}, '', '/login')
  })
  afterEach(() => {
    localStorage.clear()
    window.history.replaceState({}, '', '/')
    vi.unstubAllGlobals()
  })

  it('lists the nodes bound to a user', async () => {
    const fetchMock = mockFetch(() => jsonResponse([{ id: 'n-1', name: 'alpha' }]))

    await expect(apiClient.listUserNodes('u-1')).resolves.toEqual([{ id: 'n-1', name: 'alpha' }])

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/users/u-1/nodes')
    expect(init?.method).toBeUndefined() // plain GET
  })

  it('replaces the whole binding set with PUT and returns the resulting nodes', async () => {
    const fetchMock = mockFetch(() => jsonResponse([{ id: 'n-2', name: 'beta' }]))

    await expect(apiClient.setUserNodes('u-1', ['n-1', 'n-2'])).resolves.toEqual([
      { id: 'n-2', name: 'beta' },
    ])

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/users/u-1/nodes')
    expect(init?.method).toBe('PUT')
    expect(init?.body).toBe(JSON.stringify({ node_ids: ['n-1', 'n-2'] }))
  })

  it('clears all bindings when the node set is empty (full-set replace)', async () => {
    const fetchMock = mockFetch(() => jsonResponse([]))

    await expect(apiClient.setUserNodes('u-1', [])).resolves.toEqual([])

    const [, init] = fetchMock.mock.calls[0]
    expect(init?.method).toBe('PUT')
    expect(init?.body).toBe(JSON.stringify({ node_ids: [] }))
  })
})

describe('workspace lifecycle', () => {
  beforeEach(() => {
    localStorage.clear()
    window.history.replaceState({}, '', '/login')
  })
  afterEach(() => {
    localStorage.clear()
    window.history.replaceState({}, '', '/')
    vi.unstubAllGlobals()
  })

  it('renames via PATCH {name} and deletes via DELETE', async () => {
    const fetchMock = mockFetch((_url, init) =>
      init?.method === 'DELETE'
        ? new Response(null, { status: 204 })
        : jsonResponse({ id: 'w-1', name: 'Renamed' })
    )

    await expect(apiClient.renameWorkspace('w-1', 'Renamed')).resolves.toMatchObject({
      name: 'Renamed',
    })
    expect(fetchMock.mock.calls[0][0]).toBe('/api/workspaces/w-1')
    expect(fetchMock.mock.calls[0][1]).toMatchObject({
      method: 'PATCH',
      body: JSON.stringify({ name: 'Renamed' }),
    })

    await expect(apiClient.deleteWorkspace('w-1')).resolves.toBeUndefined()
    expect(fetchMock.mock.calls[1][0]).toBe('/api/workspaces/w-1')
    expect(fetchMock.mock.calls[1][1]).toMatchObject({ method: 'DELETE' })
  })
})

describe('task lifecycle', () => {
  beforeEach(() => {
    localStorage.clear()
    window.history.replaceState({}, '', '/login')
  })
  afterEach(() => {
    localStorage.clear()
    window.history.replaceState({}, '', '/')
    vi.unstubAllGlobals()
  })

  it('creates backlog tasks with auto_start: false', async () => {
    const fetchMock = mockFetch(() => jsonResponse({ id: 't-1' }))

    await apiClient.createTask({
      workspace_id: 'w-1',
      title: 'Backlog item',
      agent_type: 'fake',
      auto_start: false,
    })

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/tasks')
    expect(init?.method).toBe('POST')
    expect(init?.body).toBe(
      JSON.stringify({
        workspace_id: 'w-1',
        title: 'Backlog item',
        agent_type: 'fake',
        auto_start: false,
      })
    )
  })

  it('starts a waiting task via POST /api/tasks/{id}/start', async () => {
    const fetchMock = mockFetch(() => jsonResponse({ id: 't-1', status: 'running' }))

    await expect(apiClient.startTask('t-1')).resolves.toEqual({ id: 't-1', status: 'running' })

    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/tasks/t-1/start')
    expect(init?.method).toBe('POST')
  })

  it('archives via PATCH {archived} and lists only archived rows with ?archived=1', async () => {
    const fetchMock = mockFetch(() => jsonResponse({ id: 't-1' }))

    await apiClient.patchTask('t-1', { archived: true })
    expect(fetchMock.mock.calls[0][0]).toBe('/api/tasks/t-1')
    expect(fetchMock.mock.calls[0][1]).toMatchObject({
      method: 'PATCH',
      body: JSON.stringify({ archived: true }),
    })

    await apiClient.listTasks('w-1', undefined, { archived: true })
    expect(fetchMock.mock.calls[1][0]).toBe('/api/tasks?workspace_id=w-1&archived=1')

    await apiClient.listTasksByNode('n-1', { archived: true })
    expect(fetchMock.mock.calls[2][0]).toBe('/api/tasks?node_id=n-1&archived=1')
  })

  it('keeps the default list unfiltered and preserves the q search path', async () => {
    const fetchMock = mockFetch(() => jsonResponse([]))

    await apiClient.listTasks('w-1')
    expect(fetchMock.mock.calls[0][0]).toBe('/api/tasks?workspace_id=w-1')

    await apiClient.listTasks('w-1', 'flaky')
    expect(fetchMock.mock.calls[1][0]).toBe('/api/tasks?workspace_id=w-1&q=flaky')

    await apiClient.listTasksByNode('n-1')
    expect(fetchMock.mock.calls[2][0]).toBe('/api/tasks?node_id=n-1')
  })
})

describe('session messages', () => {
  beforeEach(() => {
    localStorage.clear()
    window.history.replaceState({}, '', '/login')
  })
  afterEach(() => {
    localStorage.clear()
    window.history.replaceState({}, '', '/')
    vi.unstubAllGlobals()
  })

  it('reads the whole transcript by default; a limit windows the newest rows', async () => {
    const fetchMock = mockFetch(() => jsonResponse([]))

    await apiClient.getMessages('s-1')
    expect(fetchMock.mock.calls[0][0]).toBe('/api/sessions/s-1/messages')

    await apiClient.getMessages('s-1', 5)
    expect(fetchMock.mock.calls[1][0]).toBe('/api/sessions/s-1/messages?limit=5')
  })
})
