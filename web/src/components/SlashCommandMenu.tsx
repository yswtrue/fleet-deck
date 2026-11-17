import type { SessionCommand } from '../gen/protocol/index.ts'
import styles from './SlashCommandMenu.module.css'

interface SlashCommandMenuProps {
  commands: SessionCommand[]
  activeIndex: number
  /** Localized listbox label (screen readers). */
  label: string
  onPick: (command: SessionCommand) => void
  onHover: (index: number) => void
}

/**
 * Slash-command completion list, positioned by its container. Rendered by the
 * session composer and the new-task prompt; the descriptions come from the
 * agent, so only the listbox label is localized.
 */
export function SlashCommandMenu({
  commands,
  activeIndex,
  label,
  onPick,
  onHover,
}: SlashCommandMenuProps) {
  return (
    <div className={styles.menu} role="listbox" aria-label={label}>
      {commands.map((command, index) => (
        <button
          key={`${command.name}-${index}`}
          type="button"
          role="option"
          aria-selected={index === activeIndex}
          className={index === activeIndex ? `${styles.item} ${styles.itemActive}` : styles.item}
          onMouseDown={(event) => {
            // Keep focus in the textarea; mousedown would blur it before the
            // click lands.
            event.preventDefault()
            onPick(command)
          }}
          onMouseEnter={() => onHover(index)}
        >
          <span className={styles.name}>/{command.name}</span>
          {command.description && <span className={styles.description}>{command.description}</span>}
          {command.input_hint && <span className={styles.hint}>{command.input_hint}</span>}
        </button>
      ))}
    </div>
  )
}
