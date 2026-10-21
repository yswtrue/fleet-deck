import { describe, expect, it, vi, beforeAll, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { DirectoryPicker } from './DirectoryPicker.tsx'

const fetchMock = vi.fn()

beforeAll(() => {
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  fetchMock.mockReset()
})

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

function renderPicker(onSelect: (path: string) => void) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <DirectoryPicker
        open
        nodeId="node-1"
        initialPath="/home/me"
        onCancel={() => {}}
        onSelect={onSelect}
      />
    </QueryClientProvider>
  )
}

describe('DirectoryPicker', () => {
  it('lists node directories, descends, and selects the resolved folder', async () => {
    fetchMock
      .mockResolvedValueOnce(
        json({
          path: '/home/me',
          parent: '/home',
          entries: [{ name: 'code', path: '/home/me/code' }],
        })
      )
      .mockResolvedValueOnce(
        json({
          path: '/home/me/code',
          parent: '/home/me',
          entries: [{ name: 'app', path: '/home/me/code/app' }],
        })
      )
    const onSelect = vi.fn()
    renderPicker(onSelect)

    expect(await screen.findByText('code')).toBeTruthy()
    expect(fetchMock.mock.calls[0][0]).toBe('/api/nodes/node-1/fs?path=%2Fhome%2Fme')

    fireEvent.click(screen.getByRole('button', { name: /code/ }))
    expect(await screen.findByText('app')).toBeTruthy()
    expect(fetchMock.mock.calls[1][0]).toBe('/api/nodes/node-1/fs?path=%2Fhome%2Fme%2Fcode')

    fireEvent.click(screen.getByRole('button', { name: 'Use this folder' }))
    expect(onSelect).toHaveBeenCalledWith('/home/me/code')
  })

  it('surfaces an offline node instead of an empty listing', async () => {
    fetchMock.mockResolvedValue(json({ error: 'node is offline' }, 503))
    renderPicker(vi.fn())

    expect(await screen.findByText(/node is offline/)).toBeTruthy()
    const select = screen.getByRole('button', { name: 'Use this folder' })
    expect((select as HTMLButtonElement).disabled).toBe(true)
  })

  it('starts from the node home when no initial path is given', async () => {
    fetchMock.mockResolvedValueOnce(json({ path: '/home/me', parent: '/home', entries: [] }))
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    render(
      <QueryClientProvider client={client}>
        <DirectoryPicker open nodeId="node-1" onCancel={() => {}} onSelect={vi.fn()} />
      </QueryClientProvider>
    )
    await waitFor(() => expect(fetchMock).toHaveBeenCalled())
    expect(fetchMock.mock.calls[0][0]).toBe('/api/nodes/node-1/fs')
  })
})
