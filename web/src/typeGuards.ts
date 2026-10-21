/**
 * isRecord narrows unknown JSON data to a plain object. Arrays are excluded:
 * every wire payload this UI inspects is an object, and treating an array as a
 * record would push the failure one field check later.
 */
export function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
