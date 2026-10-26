import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MessageBubble } from './MessageBubble.tsx'
import type { Message } from '../types.ts'

const base = { id: 'm-1', session_id: 'sess-1', created_at: '2026-09-28T00:00:00Z' }

describe('MessageBubble', () => {
  it('labels replayed reasoning and renders its text', () => {
    render(
      <MessageBubble
        message={{ ...base, role: 'thinking', content: 'weighing the options' } as Message}
      />
    )
    expect(screen.getByText('Thinking')).toBeTruthy()
    expect(screen.getByText('weighing the options')).toBeTruthy()
  })

  it('renders a permission reply as the chosen option, never the raw marker', () => {
    render(
      <MessageBubble
        message={
          {
            ...base,
            role: 'user',
            content: '[permission_response] option_id="allow-once"',
          } as Message
        }
      />
    )
    expect(screen.getByText('Permission')).toBeTruthy()
    expect(screen.getByText('allow-once')).toBeTruthy()
    expect(screen.queryByText(/\[permission_response\]/)).toBeNull()
  })

  it('renders an elicitation reply as its action, never the raw marker', () => {
    render(
      <MessageBubble
        message={
          {
            ...base,
            role: 'user',
            content: '[elicitation_response] action="accept" content={"value":"allow"}',
          } as Message
        }
      />
    )
    expect(screen.getByText('Input')).toBeTruthy()
    expect(screen.getByText('Accepted')).toBeTruthy()
    expect(screen.queryByText(/\[elicitation_response\]/)).toBeNull()
  })

  it('renders a tool call as a labelled row with its live status', () => {
    render(
      <MessageBubble
        message={
          {
            ...base,
            role: 'tool',
            content: 'Read README.md',
            tool: {
              id: 'call-1',
              title: 'Read README.md',
              kind: 'read',
              status: 'in_progress',
            },
          } as Message
        }
      />
    )
    expect(screen.getByText('Tool')).toBeTruthy()
    expect(screen.getByText('Read README.md')).toBeTruthy()
    expect(screen.getByText('read')).toBeTruthy()
    expect(screen.getByText('Running')).toBeTruthy()
  })

  it('renders plain content and labels the roles', () => {
    const { unmount } = render(
      <MessageBubble message={{ ...base, role: 'assistant', content: 'Hello, world' } as Message} />
    )
    expect(screen.getByText('Hello, world')).toBeTruthy()
    expect(screen.getByText('Assistant')).toBeTruthy()
    unmount()

    render(
      <MessageBubble
        message={{ ...base, role: 'system', content: 'node went offline' } as Message}
      />
    )
    expect(screen.getByText('System')).toBeTruthy()
    expect(screen.getByText('node went offline')).toBeTruthy()
  })

  it('renders assistant markdown into elements, not literal syntax', () => {
    const { container } = render(
      <MessageBubble
        message={
          {
            ...base,
            role: 'assistant',
            content:
              '## Result\n\n**done** in `src/app.ts`\n\n- first\n- second\n\n| key | value |\n| --- | --- |\n| a | 1 |',
          } as Message
        }
      />
    )
    expect(screen.getByRole('heading', { level: 2 }).textContent).toBe('Result')
    expect(container.querySelector('strong')?.textContent).toBe('done')
    expect(container.querySelector('p code')?.textContent).toBe('src/app.ts')
    expect(container.querySelectorAll('ul li')).toHaveLength(2)
    expect(container.querySelector('table td')?.textContent).toBe('a')
    expect(screen.queryByText(/\*\*done\*\*/)).toBeNull()
  })

  it('opens markdown links in a new tab without leaking window.opener', () => {
    const { container } = render(
      <MessageBubble
        message={
          {
            ...base,
            role: 'assistant',
            content: '[docs](https://example.com/docs)',
          } as Message
        }
      />
    )
    const link = container.querySelector('a')
    expect(link?.getAttribute('href')).toBe('https://example.com/docs')
    expect(link?.getAttribute('target')).toBe('_blank')
    expect(link?.getAttribute('rel')).toBe('noopener noreferrer')
  })

  it('never injects raw HTML from message content', () => {
    const { container } = render(
      <MessageBubble
        message={
          {
            ...base,
            role: 'assistant',
            content: '<img src="x" onerror="alert(1)"><script>alert(1)</script>',
          } as Message
        }
      />
    )
    expect(container.querySelector('img')).toBeNull()
    expect(container.querySelector('script')).toBeNull()
  })

  it('hides the time for replayed history without timestamps', () => {
    // ACP replay carries no timestamps and none are fabricated: the bubble
    // must show content but no bogus time.
    const { container } = render(
      <MessageBubble
        message={
          {
            ...base,
            created_at: '0001-01-01T00:00:00Z',
            role: 'assistant',
            content: 'replayed answer',
          } as Message
        }
      />
    )
    expect(screen.getByText('replayed answer')).toBeTruthy()
    expect(container.querySelector('time')).toBeNull()
  })
})
