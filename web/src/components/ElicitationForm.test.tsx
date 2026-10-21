import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { ElicitationForm } from './ElicitationForm.tsx'
import type { ElicitationRequest } from '../elicitation.ts'

const request: ElicitationRequest = {
  message: 'How should I proceed?',
  fields: [
    {
      name: 'note',
      title: 'Note',
      required: true,
      kind: 'string',
      minLength: 2,
    },
    {
      name: 'force',
      title: 'Force',
      required: false,
      kind: 'boolean',
      default: false,
    },
  ],
}

describe('ElicitationForm', () => {
  it('keeps the user on the form until required fields validate', async () => {
    const onSubmit = vi.fn()
    render(<ElicitationForm request={request} onSubmit={onSubmit} />)

    fireEvent.click(screen.getByRole('button', { name: 'Submit' }))
    await waitFor(() => expect(screen.getByText('This field is required')).toBeTruthy())
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it('submits only the values that pass schema validation', async () => {
    const onSubmit = vi.fn()
    render(<ElicitationForm request={request} onSubmit={onSubmit} />)

    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'go ahead' } })
    fireEvent.click(screen.getByRole('button', { name: 'Submit' }))

    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith('accept', { note: 'go ahead', force: false })
    )
  })

  it('binds a single-select enum to the form value', async () => {
    const onSubmit = vi.fn()
    render(
      <ElicitationForm
        request={{
          message: 'Run the command?',
          fields: [
            {
              name: 'value',
              title: 'Decision',
              required: true,
              kind: 'select',
              options: [
                { value: 'allow', label: 'Allow' },
                { value: 'deny', label: 'Deny' },
              ],
            },
          ],
        }}
        onSubmit={onSubmit}
      />
    )

    fireEvent.mouseDown(screen.getByRole('combobox'))
    fireEvent.click(await screen.findByText('Allow'))
    fireEvent.click(screen.getByRole('button', { name: 'Submit' }))

    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith('accept', { value: 'allow' }))
  })

  it('submits false for a required boolean without an explicit default', async () => {
    const onSubmit = vi.fn()
    render(
      <ElicitationForm
        request={{
          message: 'Approve?',
          fields: [{ name: 'value', title: 'Approve', required: true, kind: 'boolean' }],
        }}
        onSubmit={onSubmit}
      />
    )

    fireEvent.click(screen.getByRole('button', { name: 'Submit' }))
    await waitFor(() => expect(onSubmit).toHaveBeenCalledWith('accept', { value: false }))
  })

  it('always offers decline and cancel without submitting content', () => {
    const onSubmit = vi.fn()
    render(<ElicitationForm request={request} onSubmit={onSubmit} />)

    fireEvent.click(screen.getByRole('button', { name: 'Decline' }))
    expect(onSubmit).toHaveBeenLastCalledWith('decline')
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onSubmit).toHaveBeenLastCalledWith('cancel')
  })
})
