import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { Login } from './Login.tsx'

function mockStatus(needsSetup: boolean) {
  vi.stubGlobal(
    'fetch',
    vi.fn(() =>
      Promise.resolve(
        new Response(JSON.stringify({ needs_setup: needsSetup }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      )
    )
  )
}

function renderLogin() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <Login />
      </MemoryRouter>
    </QueryClientProvider>
  )
}

describe('Login', () => {
  beforeEach(() => localStorage.clear())
  afterEach(() => {
    localStorage.clear()
    vi.unstubAllGlobals()
  })

  it('offers first-run setup (with confirm field) when the hub has no users', async () => {
    mockStatus(true)
    renderLogin()

    expect(await screen.findByText('Create the first admin account to set up this hub.')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Create admin account' })).toBeTruthy()
    expect(screen.getByText('Display name')).toBeTruthy()
    expect(screen.getByText('Confirm password')).toBeTruthy()
  })

  it('offers plain sign-in (no setup fields) once users exist', async () => {
    mockStatus(false)
    renderLogin()

    expect(await screen.findByRole('button', { name: 'Sign in' })).toBeTruthy()
    expect(screen.getByText('Sign in to your FleetDeck account.')).toBeTruthy()
    expect(screen.queryByText('Display name')).toBeNull()
    expect(screen.queryByText('Confirm password')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Create admin account' })).toBeNull()
  })
})
