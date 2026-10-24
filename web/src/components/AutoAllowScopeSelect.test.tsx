import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { AutoAllowScopeSelect } from './AutoAllowScopeSelect.tsx'

describe('AutoAllowScopeSelect', () => {
  it('shows the stored range and reports the picked one', async () => {
    const onChange = vi.fn()
    render(<AutoAllowScopeSelect value="none" onChange={onChange} />)

    expect(
      screen.getByText('Do not auto-allow', { selector: '.ant-select-selection-item' })
    ).toBeTruthy()

    fireEvent.mouseDown(screen.getByRole('combobox', { name: 'Auto-allow range' }))
    fireEvent.click(await screen.findByText('All directories'))
    // antd passes the option as a second argument; only the value matters.
    expect(onChange).toHaveBeenCalledOnce()
    expect(onChange.mock.calls[0][0]).toBe('all')
  })
})
