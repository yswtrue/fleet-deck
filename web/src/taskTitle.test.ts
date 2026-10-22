import { describe, expect, it } from 'vitest'
import { deriveTaskTitle } from './taskTitle.ts'

describe('deriveTaskTitle', () => {
  it('uses the first non-empty line, trimmed', () => {
    expect(deriveTaskTitle('\n  Update flake inputs\nand rebuild')).toBe('Update flake inputs')
  })

  it('caps the title at 80 characters without splitting a surrogate pair', () => {
    const title = deriveTaskTitle('🙂'.repeat(100))
    expect(Array.from(title)).toHaveLength(81)
    expect(title.endsWith('…')).toBe(true)
  })

  it('returns an empty string for a blank prompt', () => {
    expect(deriveTaskTitle('  \n\t ')).toBe('')
  })
})
