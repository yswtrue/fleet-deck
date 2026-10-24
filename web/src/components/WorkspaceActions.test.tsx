import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { WorkspaceActions } from './WorkspaceActions.tsx'
import type { Workspace } from '../types.ts'

const fetchMock = vi.fn()

beforeAll(() => {
  vi.stubGlobal('fetch', fetchMock)
})

afterEach(() => {
  fetchMock.mockReset()
})

const workspace: Workspace = {
  id: 'w-1',
  node_id: 'n-1',
  name: 'Original',
  path: '/tmp/original',
  auto_allow_scope: 'none',
  created_at: '2026-10-24T00:00:00Z',
}

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

function renderActions(onDeleted = vi.fn()) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={client}>
      <WorkspaceActions workspace={workspace} variant="buttons" onDeleted={onDeleted} />
    </QueryClientProvider>
  )
  return { client, onDeleted }
}

describe('WorkspaceActions', () => {
  it('renames via PATCH and updates the workspace cache', async () => {
    fetchMock.mockResolvedValueOnce(json({ ...workspace, name: 'Renamed' }))
    const { client } = renderActions()

    fireEvent.click(screen.getByRole('button', { name: 'Rename' }))
    const input = await screen.findByRole('textbox')
    fireEvent.change(input, { target: { value: '  Renamed  ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    expect(fetchMock.mock.calls[0][0]).toBe('/api/workspaces/w-1')
    expect(fetchMock.mock.calls[0][1]).toMatchObject({
      method: 'PATCH',
      body: JSON.stringify({ name: 'Renamed' }),
    })
    await waitFor(() =>
      expect(client.getQueryData(['workspace', 'w-1'])).toMatchObject({ name: 'Renamed' })
    )
  })

  it('deletes only after confirmation and calls onDeleted', async () => {
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }))
    const { onDeleted } = renderActions()

    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText(/Files on the node are untouched/)).toBeTruthy()
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }))

    await waitFor(() => expect(onDeleted).toHaveBeenCalledTimes(1))
    expect(fetchMock.mock.calls[0][0]).toBe('/api/workspaces/w-1')
    expect(fetchMock.mock.calls[0][1]).toMatchObject({ method: 'DELETE' })
  })

  it('surfaces the active-session 409 and keeps the workspace', async () => {
    fetchMock.mockResolvedValueOnce(
      json({ error: 'workspace has active sessions; finish or cancel them first' }, 409)
    )
    const { onDeleted } = renderActions()

    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }))

    expect(
      await screen.findByText(/workspace has active sessions; finish or cancel them first/)
    ).toBeTruthy()
    expect(onDeleted).not.toHaveBeenCalled()
  })
})
