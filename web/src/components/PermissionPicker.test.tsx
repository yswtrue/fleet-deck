import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { PermissionPicker } from './PermissionPicker.tsx'
import type { PermissionRequest } from '../permission.ts'

const request: PermissionRequest = {
  question: 'Allow the agent to edit /tmp/x.go?',
  options: [
    { id: 'allow-once', label: 'Allow' },
    { id: 'allow-always', label: 'Allow always' },
    { id: 'deny', label: 'Deny' },
  ],
}

describe('PermissionPicker', () => {
  it('renders the question and one button per option', () => {
    render(<PermissionPicker request={request} onPick={() => {}} />)
    expect(screen.getByText('Allow the agent to edit /tmp/x.go?')).toBeTruthy()
    for (const option of request.options) {
      expect(screen.getByRole('button', { name: option.label })).toBeTruthy()
    }
  })

  it('reports the chosen option id, not its label', () => {
    const onPick = vi.fn()
    render(<PermissionPicker request={request} onPick={onPick} />)
    fireEvent.click(screen.getByRole('button', { name: 'Allow always' }))
    expect(onPick).toHaveBeenCalledExactlyOnceWith('allow-always')
    fireEvent.click(screen.getByRole('button', { name: 'Deny' }))
    expect(onPick).toHaveBeenLastCalledWith('deny')
  })

  it('marks deny-ish options with the danger styling', () => {
    render(<PermissionPicker request={request} onPick={() => {}} />)
    expect(screen.getByRole('button', { name: 'Deny' }).className).toContain('ant-btn-dangerous')
    expect(screen.getByRole('button', { name: 'Allow' }).className).not.toContain('ant-btn-dangerous')
  })
})
