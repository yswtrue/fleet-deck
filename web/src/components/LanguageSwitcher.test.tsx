import { describe, expect, it, afterEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { LanguageSwitcher } from './LanguageSwitcher.tsx'
import { i18n } from '../i18n/index.ts'

afterEach(() => {
  void i18n.changeLanguage('en')
  window.localStorage.removeItem('fleetdeck.lang')
})

describe('LanguageSwitcher', () => {
  it('switches the active language and persists the choice', async () => {
    render(<LanguageSwitcher />)

    fireEvent.click(screen.getByRole('button', { name: 'Language' }))
    fireEvent.click(await screen.findByText('中文'))

    expect(i18n.resolvedLanguage).toBe('zh-CN')
    expect(window.localStorage.getItem('fleetdeck.lang')).toBe('zh-CN')
    // The switcher itself must re-render in the new language.
    expect(await screen.findByRole('button', { name: '语言' })).toBeTruthy()
  })
})
