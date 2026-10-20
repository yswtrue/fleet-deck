import { describe, expect, it, vi, beforeAll, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { AddNodeModal } from './AddNodeModal.tsx'

const fetchMock = vi.fn()

beforeAll(() => {
  vi.stubGlobal('fetch', fetchMock)
  Object.assign(navigator, { clipboard: { writeText: vi.fn().mockResolvedValue(undefined) } })
})

afterEach(() => {
  fetchMock.mockReset()
  vi.mocked(navigator.clipboard.writeText).mockClear()
})

function renderModal() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <AddNodeModal open onClose={() => {}} />
    </QueryClientProvider>
  )
}

describe('AddNodeModal', () => {
  it('mints a one-time join token and shows a copy-ready start command', async () => {
    fetchMock.mockResolvedValue(
      new Response(JSON.stringify({ token: 'join-abc', expires_at: '2026-10-20T12:00:00Z' }), {
        status: 201,
        headers: { 'Content-Type': 'application/json' },
      })
    )
    renderModal()
    fireEvent.click(screen.getByRole('button', { name: 'Generate token' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    expect(fetchMock.mock.calls[0][0]).toBe('/api/nodes/join-tokens')
    expect(fetchMock.mock.calls[0][1]).toMatchObject({ method: 'POST' })

    // The plaintext token is only ever visible in this one-time command, and it
    // travels as a flag (no environment variable).
    await screen.findByText(/join-abc/)
    expect(document.body.textContent).toContain('--join-token join-abc')
    expect(document.body.textContent).toContain('--hub-url')
    expect(document.body.textContent).toContain('/ws/node')
    expect(document.body.textContent).toContain('Application Support/fleetdeck')
    expect(document.body.textContent).not.toContain('FLEETDECK_NODE_JOIN_TOKEN')

    fireEvent.click(screen.getByRole('button', { name: 'Copy command' }))
    await waitFor(() =>
      expect(navigator.clipboard.writeText).toHaveBeenCalledWith(
        expect.stringContaining('join-abc')
      )
    )
  })

  it('surfaces a mint failure instead of showing a token', async () => {
    fetchMock.mockResolvedValue(
      new Response(JSON.stringify({ error: 'unauthorized' }), {
        status: 401,
        headers: { 'Content-Type': 'application/json' },
      })
    )
    renderModal()
    fireEvent.click(screen.getByRole('button', { name: 'Generate token' }))
    await waitFor(() =>
      expect(document.body.textContent).toContain('Could not create a join token')
    )
    expect(document.body.textContent).not.toContain('FLEETDECK_NODE_JOIN_TOKEN=')
  })
})
