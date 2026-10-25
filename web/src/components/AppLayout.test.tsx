import { afterEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { AppLayout } from './AppLayout.tsx'
import { clearToken, storeToken } from '../apiClient.ts'

const adminUser = {
  id: 'u-1',
  email: 'admin@example.com',
  display_name: 'Admin',
  role: 'admin',
  created_at: '2026-01-01T00:00:00Z',
}

/** Routes hub requests: `me` returns the admin user, health reports the hub. */
function mockHub() {
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      const payload = url.startsWith('/api/auth/me')
        ? adminUser
        : url.startsWith('/api/health')
          ? { status: 'ok', version: 'v9.9.9' }
          : {}
      return Promise.resolve(
        new Response(JSON.stringify(payload), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      )
    })
  )
}

/** matchMedia stub: `small` decides whether the <=768px queries match. */
function stubMatchMedia(small: boolean) {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: query.includes('max-width: 768px') && small,
    media: query,
    onchange: null,
    addListener: () => {},
    removeListener: () => {},
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => false,
  }))
}

function renderLayout(initialEntry: string) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[initialEntry]}>
        <AppLayout>
          <div>page-content</div>
        </AppLayout>
      </MemoryRouter>
    </QueryClientProvider>
  )
}

afterEach(() => {
  vi.unstubAllGlobals()
  clearToken()
})

describe('AppLayout', () => {
  it('renders the section menu and highlights the active route', () => {
    mockHub()
    stubMatchMedia(false)
    renderLayout('/usage')

    expect(screen.getByText('page-content')).toBeTruthy()
    for (const label of ['Dashboard', 'Nodes', 'Usage']) {
      expect(screen.getByText(label)).toBeTruthy()
    }
    const selected = document.querySelector('.ant-menu-item-selected')
    expect(selected?.textContent).toBe('Usage')
  })

  it('hides the users entry when signed out', () => {
    mockHub()
    stubMatchMedia(false)
    renderLayout('/nodes')

    expect(screen.queryByText('Users')).toBeNull()
    expect(document.querySelector('.ant-menu-item-selected')?.textContent).toBe('Nodes')
  })

  it('shows the users entry for admins', async () => {
    mockHub()
    stubMatchMedia(false)
    storeToken('tok-admin', '2099-01-01T00:00:00Z')
    renderLayout('/nodes')

    expect(await screen.findByText('Users')).toBeTruthy()
  })

  it('shows the hub version at the bottom of the sidebar', async () => {
    mockHub()
    stubMatchMedia(false)
    renderLayout('/')

    expect(await screen.findByText('v9.9.9')).toBeTruthy()
    expect(screen.getByLabelText('Version: v9.9.9')).toBeTruthy()
  })

  it('opens the nav drawer from the header hamburger on small screens', async () => {
    mockHub()
    stubMatchMedia(true)
    renderLayout('/')

    // No sidebar at this width; the hamburger lives in the header.
    const hamburger = screen.getByLabelText('Open navigation')
    fireEvent.click(hamburger)

    // The drawer carries the same section menu.
    expect(await screen.findByText('Dashboard')).toBeTruthy()
    expect(screen.getByText('Nodes')).toBeTruthy()
  })

  it('resolves node, workspace and task crumbs for a session deep link', async () => {
    // A direct /sessions/{id} URL has no cached board/workspace state: the
    // header must resolve the parent chain from the snapshot + lookups.
    const snapshot = {
      session: {
        id: 's-1',
        task_id: 't-1',
        node_id: 'n-1',
        agent_type: 'fake',
        status: 'running',
        created_at: '2026-01-01T00:00:00Z',
      },
      task: {
        id: 't-1',
        workspace_id: 'w-1',
        title: 'Fix the breadcrumb',
        status: 'running',
        agent_type: 'fake',
        sort_order: 0,
        auto_start: true,
        created_at: '2026-01-01T00:00:00Z',
        updated_at: '2026-01-01T00:00:00Z',
      },
      pending_input: '',
      partial_output: '',
      partial_thought: '',
    }
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input)
        const payload = url.startsWith('/api/auth/me')
          ? adminUser
          : url.startsWith('/api/health')
            ? { status: 'ok', version: 'v9.9.9' }
            : url === '/api/sessions/s-1'
              ? snapshot
              : url === '/api/workspaces/w-1'
                ? { id: 'w-1', node_id: 'n-1', name: 'Repo' }
                : url === '/api/nodes/n-1'
                  ? { id: 'n-1', name: 'Box' }
                  : {}
        return Promise.resolve(
          new Response(JSON.stringify(payload), {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          })
        )
      })
    )
    stubMatchMedia(false)
    renderLayout('/sessions/s-1')

    const nodeCrumb = await screen.findByRole('link', { name: 'Box' })
    expect(nodeCrumb.getAttribute('href')).toBe('/nodes/n-1')
    expect(screen.getByRole('link', { name: 'Repo' }).getAttribute('href')).toBe('/workspaces/w-1')
    expect(screen.getByRole('link', { name: 'Fix the breadcrumb' }).getAttribute('href')).toBe(
      '/sessions/s-1'
    )
  })
})
