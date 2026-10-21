import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MessageBubble } from './MessageBubble.tsx'
import type { Message } from '../types.ts'

const base = { id: 'm-1', session_id: 'sess-1', created_at: '2026-09-28T00:00:00Z' }

describe('MessageBubble', () => {
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

  it('renders ordinary content verbatim and labels the roles', () => {
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
