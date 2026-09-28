import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { NotificationSettingsPanel } from './NotificationSettings.tsx'
import { NOTIFY_STORAGE_KEY } from '../hooks/useTaskNotifications.ts'

class FakeNotification {
  static permission: NotificationPermission = 'default'
  static requestPermission = vi.fn().mockResolvedValue('default')
}

function renderPanel(permission?: NotificationPermission) {
  if (permission !== undefined) {
    FakeNotification.permission = permission
  }
  return render(<NotificationSettingsPanel open onClose={() => {}} />)
}

beforeEach(() => {
  vi.stubGlobal('Notification', FakeNotification)
})

afterEach(() => {
  vi.unstubAllGlobals()
  localStorage.clear()
})

function switchEl(): HTMLButtonElement {
  return screen.getByRole('switch') as HTMLButtonElement
}

describe('NotificationSettingsPanel', () => {
  it('shows the current permission state', () => {
    renderPanel('granted')
    expect(screen.getByText('Allowed')).toBeTruthy()
  })

  it('shows the unsupported message when the browser has no Notification API', () => {
    vi.stubGlobal('Notification', undefined)
    render(<NotificationSettingsPanel open onClose={() => {}} />)
    expect(screen.getByText(/not available/i)).toBeTruthy()
    expect(screen.queryByRole('switch')).toBeNull()
  })

  it('shows the denied help when permission is denied', () => {
    renderPanel('denied')
    expect(screen.getByRole('alert').textContent).toMatch(/blocked/i)
    expect(switchEl().disabled).toBe(true)
  })

  it('persists the on/off switch to localStorage', () => {
    localStorage.setItem(NOTIFY_STORAGE_KEY, 'on')
    renderPanel('granted')
    expect(switchEl().getAttribute('aria-checked')).toBe('true')
    expect(localStorage.getItem(NOTIFY_STORAGE_KEY)).toBe('on')

    fireEvent.click(switchEl())
    expect(switchEl().getAttribute('aria-checked')).toBe('false')
    expect(localStorage.getItem(NOTIFY_STORAGE_KEY)).toBe('off')

    fireEvent.click(switchEl())
    expect(switchEl().getAttribute('aria-checked')).toBe('true')
    expect(localStorage.getItem(NOTIFY_STORAGE_KEY)).toBe('on')
  })

  it('initializes the switch from localStorage', () => {
    localStorage.setItem(NOTIFY_STORAGE_KEY, 'off')
    renderPanel('granted')
    expect(switchEl().getAttribute('aria-checked')).toBe('false')
  })

  it('requests permission and enables notifications on grant', async () => {
    renderPanel('default')
    FakeNotification.requestPermission.mockResolvedValue('granted')

    fireEvent.click(screen.getByRole('button', { name: /enable notifications/i }))

    await waitFor(() => expect(FakeNotification.requestPermission).toHaveBeenCalledOnce())
    expect(localStorage.getItem(NOTIFY_STORAGE_KEY)).toBe('on')
    expect(screen.getByText('Allowed')).toBeTruthy()
    expect(switchEl().getAttribute('aria-checked')).toBe('true')
  })

  it('keeps the switch off if permission is denied after requesting', async () => {
    localStorage.setItem(NOTIFY_STORAGE_KEY, 'off')
    renderPanel('default')
    FakeNotification.requestPermission.mockResolvedValue('denied')

    fireEvent.click(screen.getByRole('button', { name: /enable notifications/i }))

    await waitFor(() => expect(FakeNotification.requestPermission).toHaveBeenCalledOnce())
    expect(localStorage.getItem(NOTIFY_STORAGE_KEY)).toBe('off')
    expect(screen.getByText('Blocked')).toBeTruthy()
  })
})
