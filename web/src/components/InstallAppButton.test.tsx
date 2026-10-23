import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Mock } from 'vitest'

interface PromptEvent extends Event {
  prompt: Mock
  userChoice: Promise<{ outcome: 'accepted' | 'dismissed'; platform: string }>
}

function promptEvent(outcome: 'accepted' | 'dismissed'): PromptEvent {
  const event = new Event('beforeinstallprompt') as PromptEvent
  event.prompt = vi.fn().mockResolvedValue(undefined)
  Object.defineProperty(event, 'userChoice', {
    value: Promise.resolve({ outcome, platform: 'web' }),
  })
  return event
}

/** A fresh module instance keeps the install store isolated per test. */
async function renderButton() {
  vi.resetModules()
  const { InstallAppButton } = await import('./InstallAppButton.tsx')
  return render(<InstallAppButton />)
}

function stubMedia(matches: (query: string) => boolean) {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: matches(query),
    media: query,
    onchange: null,
    addListener: () => {},
    removeListener: () => {},
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => false,
  }))
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('InstallAppButton', () => {
  it('renders nothing until the browser offers an install prompt', async () => {
    await renderButton()
    expect(screen.queryByRole('button', { name: 'Install app' })).toBeNull()
  })

  it('opens the deferred prompt and disappears once accepted', async () => {
    const view = await renderButton()
    const event = promptEvent('accepted')

    act(() => {
      window.dispatchEvent(event)
    })
    const button = await screen.findByRole('button', { name: 'Install app' })
    fireEvent.click(button)

    await waitFor(() => expect(event.prompt).toHaveBeenCalledOnce())
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Install app' })).toBeNull())
    view.unmount()
  })

  it('offers Add to Home Screen instructions on iOS', async () => {
    stubMedia(() => false)
    const original = navigator.userAgent
    Object.defineProperty(navigator, 'userAgent', {
      configurable: true,
      value: 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15',
    })
    try {
      await renderButton()
      fireEvent.click(screen.getByRole('button', { name: 'Install app' }))
      expect(await screen.findByText('Add FleetDeck to your Home Screen')).toBeTruthy()
      expect(screen.getByText(/Add to Home Screen/)).toBeTruthy()
    } finally {
      Object.defineProperty(navigator, 'userAgent', { configurable: true, value: original })
    }
  })

  it('stays hidden in standalone (already installed) mode', async () => {
    stubMedia((query) => query.includes('display-mode: standalone'))
    await renderButton()
    expect(screen.queryByRole('button', { name: 'Install app' })).toBeNull()
  })
})
