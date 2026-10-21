/**
 * Elicitation wire convention (ACP `elicitation/create`, form mode). The
 * adapter in internal/adapters/acp/elicitation.go pauses the turn and sends
 * UserInputRequested with:
 *
 *   <message>\n\n[elicitation_request] schema={"type":"object",…}
 *
 * The web replies through the normal SendMessage path with exactly:
 *
 *   [elicitation_response] action="accept" content={"field":"value"}
 *   [elicitation_response] action="decline"
 *
 * The adapter unwraps the marker and validates `content` against the schema;
 * anything it cannot validate is answered with `cancel`, never forwarded as
 * consent. Free text must not stand in for a structured reply.
 */

import { isRecord } from './typeGuards.ts'

export type ElicitationAction = 'accept' | 'decline' | 'cancel'

export interface ElicitationOption {
  value: string
  label: string
}

export type ElicitationFieldKind =
  'string' | 'number' | 'integer' | 'boolean' | 'select' | 'multiselect' | 'unsupported'

export interface ElicitationField {
  name: string
  title: string
  description?: string
  required: boolean
  kind: ElicitationFieldKind
  format?: string
  default?: string | number | boolean | string[]
  minLength?: number
  maxLength?: number
  minimum?: number
  maximum?: number
  minItems?: number
  maxItems?: number
  options?: ElicitationOption[]
}

export interface ElicitationRequest {
  message: string
  fields: ElicitationField[]
}

export type ElicitationFieldError = 'required' | 'invalid' | 'range' | 'option' | 'unsupported'

const REQUEST_MARKER = '[elicitation_request] schema='
const RESPONSE_RE =
  /^\s*\[elicitation_response\]\s+action="(accept|decline|cancel)"(?:\s+content=(\{[\s\S]*\}))?\s*$/

/**
 * parseElicitationRequest extracts the message and renderable fields from a
 * UserInputRequested prompt. Returns null for prompts without the marker or
 * with an unparseable/non-object schema; those fall through to the free-text
 * composer (the adapter will refuse an unstructured reply).
 */
export function parseElicitationRequest(prompt: string): ElicitationRequest | null {
  const markerAt = prompt.indexOf(REQUEST_MARKER)
  if (markerAt < 0) return null
  const message = prompt.slice(0, markerAt).trim()
  let schema: unknown
  try {
    schema = JSON.parse(prompt.slice(markerAt + REQUEST_MARKER.length).trim())
  } catch {
    return null
  }
  if (!isRecord(schema)) return null
  if (schema.type !== undefined && schema.type !== 'object') return null

  const properties = schema.properties
  if (properties !== undefined && properties !== null && !isRecord(properties)) return null
  const required = Array.isArray(schema.required)
    ? schema.required.filter((name): name is string => typeof name === 'string')
    : []

  const fields = Object.entries(isRecord(properties) ? properties : {}).map(([name, raw]) =>
    normalizeField(name, raw, required.includes(name))
  )
  return { message, fields }
}

function normalizeField(name: string, raw: unknown, required: boolean): ElicitationField {
  const base: ElicitationField = {
    name,
    title: '',
    required,
    kind: 'unsupported',
  }
  if (!isRecord(raw)) return base

  const title = typeof raw.title === 'string' ? raw.title : ''
  const field: ElicitationField = { ...base, title }
  if (typeof raw.description === 'string' && raw.description) field.description = raw.description
  if (typeof raw.format === 'string' && raw.format) field.format = raw.format
  const minLength =
    typeof raw.minLength === 'number' && Number.isFinite(raw.minLength) ? raw.minLength : undefined
  const maxLength =
    typeof raw.maxLength === 'number' && Number.isFinite(raw.maxLength) ? raw.maxLength : undefined
  const minimum =
    typeof raw.minimum === 'number' && Number.isFinite(raw.minimum) ? raw.minimum : undefined
  const maximum =
    typeof raw.maximum === 'number' && Number.isFinite(raw.maximum) ? raw.maximum : undefined
  const minItems =
    typeof raw.minItems === 'number' && Number.isFinite(raw.minItems) ? raw.minItems : undefined
  const maxItems =
    typeof raw.maxItems === 'number' && Number.isFinite(raw.maxItems) ? raw.maxItems : undefined
  if (minLength !== undefined) field.minLength = minLength
  if (maxLength !== undefined) field.maxLength = maxLength
  if (minimum !== undefined) field.minimum = minimum
  if (maximum !== undefined) field.maximum = maximum
  if (minItems !== undefined) field.minItems = minItems
  if (maxItems !== undefined) field.maxItems = maxItems

  switch (raw.type) {
    case 'string': {
      const options = readOptions(raw)
      if (options.length > 0) {
        field.kind = 'select'
        field.options = options
      } else {
        field.kind = 'string'
      }
      if (typeof raw.default === 'string') field.default = raw.default
      return field
    }
    case 'number':
    case 'integer': {
      field.kind = raw.type
      if (typeof raw.default === 'number' && Number.isFinite(raw.default)) {
        field.default = raw.default
      }
      return field
    }
    case 'boolean': {
      field.kind = 'boolean'
      if (typeof raw.default === 'boolean') field.default = raw.default
      return field
    }
    case 'array': {
      const items = isRecord(raw.items) ? raw.items : null
      const options = items ? readOptions(items) : []
      if (items && (items.type === undefined || items.type === 'string') && options.length > 0) {
        field.kind = 'multiselect'
        field.options = options
        if (Array.isArray(raw.default)) {
          const defaults = raw.default.filter((value): value is string => typeof value === 'string')
          field.default = defaults
        }
      }
      return field
    }
    default:
      return field
  }
}

/** readOptions reads the enum or oneOf shape used by single/multi select. */
function readOptions(raw: Record<string, unknown>): ElicitationOption[] {
  if (Array.isArray(raw.enum)) {
    return raw.enum.flatMap((value): ElicitationOption[] =>
      typeof value === 'string' ? [{ value, label: value }] : []
    )
  }
  const entries = Array.isArray(raw.oneOf) ? raw.oneOf : Array.isArray(raw.anyOf) ? raw.anyOf : []
  return entries.flatMap((entry): ElicitationOption[] => {
    if (!isRecord(entry) || typeof entry.const !== 'string') return []
    const label = typeof entry.title === 'string' ? entry.title : entry.const
    return [{ value: entry.const, label }]
  })
}

/** formatElicitationReply builds the reply line the adapter unwraps. */
export function formatElicitationReply(
  action: ElicitationAction,
  content?: Record<string, unknown>
): string {
  const base = `[elicitation_response] action="${action}"`
  if (action !== 'accept' || content === undefined) return base
  return `${base} content=${JSON.stringify(content)}`
}

/** parseElicitationReply recognizes a structured elicitation reply. */
export function parseElicitationReply(
  content: string
): { action: ElicitationAction; content?: Record<string, unknown> } | null {
  const match = RESPONSE_RE.exec(content)
  if (!match) return null
  const action = match[1] as ElicitationAction
  if (!match[2]) return { action }
  try {
    const parsed: unknown = JSON.parse(match[2])
    return isRecord(parsed) ? { action, content: parsed } : { action }
  } catch {
    return { action }
  }
}

/**
 * buildElicitationContent validates the form values and returns the payload
 * the adapter expects. Errors are codes so the UI can localize them; an accept
 * with any error is never sent.
 */
export function buildElicitationContent(
  request: ElicitationRequest,
  values: Record<string, unknown>
): { content: Record<string, unknown>; errors: Record<string, ElicitationFieldError> } {
  const content: Record<string, unknown> = {}
  const errors: Record<string, ElicitationFieldError> = {}

  for (const field of request.fields) {
    const value = values[field.name]
    if (value === undefined || value === null || value === '') {
      if (field.required) errors[field.name] = 'required'
      continue
    }
    if (field.kind === 'unsupported') {
      errors[field.name] = 'unsupported'
      continue
    }
    if (field.kind === 'string' || field.kind === 'select') {
      if (typeof value !== 'string') {
        errors[field.name] = 'invalid'
        continue
      }
      if (field.kind === 'select' && !field.options?.some((option) => option.value === value)) {
        errors[field.name] = 'option'
        continue
      }
      if (
        (field.minLength !== undefined && value.length < field.minLength) ||
        (field.maxLength !== undefined && value.length > field.maxLength)
      ) {
        errors[field.name] = 'range'
        continue
      }
      content[field.name] = value
      continue
    }
    if (field.kind === 'number' || field.kind === 'integer') {
      if (typeof value !== 'number' || !Number.isFinite(value)) {
        errors[field.name] = 'invalid'
        continue
      }
      if (field.kind === 'integer' && !Number.isInteger(value)) {
        errors[field.name] = 'invalid'
        continue
      }
      if (
        (field.minimum !== undefined && value < field.minimum) ||
        (field.maximum !== undefined && value > field.maximum)
      ) {
        errors[field.name] = 'range'
        continue
      }
      content[field.name] = value
      continue
    }
    if (field.kind === 'boolean') {
      if (typeof value !== 'boolean') {
        errors[field.name] = 'invalid'
        continue
      }
      content[field.name] = value
      continue
    }
    if (field.kind === 'multiselect') {
      if (!Array.isArray(value) || value.some((item) => typeof item !== 'string')) {
        errors[field.name] = 'invalid'
        continue
      }
      const selected = value as string[]
      if (selected.length === 0) {
        // Required means the property is present; an empty array is a valid
        // answer unless the schema asks for a minimum.
        if (!field.required) continue
        if (field.minItems !== undefined && field.minItems > 0) {
          errors[field.name] = 'range'
        } else {
          content[field.name] = selected
        }
        continue
      }
      if (selected.some((item) => !field.options?.some((option) => option.value === item))) {
        errors[field.name] = 'option'
        continue
      }
      if (
        (field.minItems !== undefined && selected.length < field.minItems) ||
        (field.maxItems !== undefined && selected.length > field.maxItems)
      ) {
        errors[field.name] = 'range'
        continue
      }
      content[field.name] = selected
    }
  }

  return { content, errors }
}
