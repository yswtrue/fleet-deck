import { useState } from 'react'
import type { SessionCommand } from '../gen/protocol/index.ts'

/** Completion rows shown at once; typing a prefix narrows the list. */
const MAX_MATCHES = 8

/** The composer holds a bare `/prefix`: no whitespace after the slash yet. */
const SLASH_PREFIX = /^\/([^\s]*)$/

interface Options {
  /** Current composer text. */
  value: string
  /** Commands the agent advertised (live session or last-known workspace list). */
  commands: SessionCommand[]
  /** False while the composer is answering a question instead of running a turn. */
  enabled: boolean
  /** Receives `/name ` when a command is picked. */
  onPick: (text: string) => void
}

export interface SlashCommands {
  open: boolean
  matches: SessionCommand[]
  activeIndex: number
  setActiveIndex: (index: number) => void
  pick: (command?: SessionCommand) => void
  /** Returns true when the key was consumed (navigation or a pick). */
  handleKeyDown: (event: React.KeyboardEvent<HTMLTextAreaElement>) => boolean
}

/**
 * Slash-command completion shared by the session composer and the new-task
 * prompt. It only inserts `/name ` text: a command is a normal prompt, so the
 * picker never sends on its own and never invokes anything itself.
 *
 * Escape dismisses the menu for the current text; any edit opens it again.
 * Both the dismissal and the highlighted row are keyed by the value, so no
 * effect has to reset state after the caller changes the text.
 */
export function useSlashCommands({ value, commands, enabled, onPick }: Options): SlashCommands {
  // dismissedForValue remembers which text the operator dismissed with Escape.
  const [dismissedForValue, setDismissedForValue] = useState<string | null>(null)
  // nav remembers the highlighted row and the text it belongs to; an edit
  // (new value) starts again at the first match.
  const [nav, setNav] = useState<{ value: string; index: number }>({ value: '', index: 0 })

  const query = SLASH_PREFIX.exec(value)?.[1].toLowerCase() ?? null
  const matches =
    enabled && dismissedForValue !== value && query !== null
      ? commands
          .filter((command) => command.name.toLowerCase().startsWith(query))
          .slice(0, MAX_MATCHES)
      : []
  const activeIndex = nav.value === value ? Math.min(nav.index, Math.max(matches.length - 1, 0)) : 0

  const setActiveIndex = (index: number) => setNav({ value, index })

  const pick = (command?: SessionCommand) => {
    if (!command) return
    const text = `/${command.name} `
    onPick(text)
    setDismissedForValue(text)
    setNav({ value: text, index: 0 })
  }

  const handleKeyDown = (event: React.KeyboardEvent<HTMLTextAreaElement>): boolean => {
    if (matches.length === 0) return false
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      setActiveIndex((activeIndex + 1) % matches.length)
      return true
    }
    if (event.key === 'ArrowUp') {
      event.preventDefault()
      setActiveIndex((activeIndex - 1 + matches.length) % matches.length)
      return true
    }
    if (event.key === 'Escape') {
      event.preventDefault()
      // Keyed by the current text: typing reopens the menu.
      setDismissedForValue(value)
      return true
    }
    if (event.key === 'Tab' || (event.key === 'Enter' && !event.shiftKey)) {
      event.preventDefault()
      pick(matches[activeIndex])
      return true
    }
    return false
  }

  return {
    open: matches.length > 0,
    matches,
    activeIndex,
    setActiveIndex,
    pick,
    handleKeyDown,
  }
}
