/**
 * Board label derived from the prompt: its first non-empty line, capped at 80
 * characters. Mirrors taskTitleFromPrompt on the hub, which derives the title
 * when a caller omits it.
 */
export function deriveTaskTitle(prompt: string, max = 80): string {
  const line = prompt
    .split('\n')
    .map((l) => l.trim())
    .find((l) => l.length > 0)
  if (!line) return ''
  const chars = Array.from(line)
  return chars.length > max ? `${chars.slice(0, max).join('')}…` : line
}
