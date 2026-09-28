/**
 * Permission wire convention (locked Phase 4 Decision 2, implemented by the ACP
 * adapter in internal/adapters/acp/acp.go). When the agent pauses on
 * session/request_permission, EventSink.UserInputRequested carries:
 *
 *   <question>\n\n[permission_request] options=[{"id":"allow-once","label":"Allow"},…]
 *
 * The web replies through the normal SendMessage path with exactly:
 *
 *   [permission_response] option_id="allow-once"
 *
 * The adapter unwraps that marker via regex, so the reply format must not
 * drift. Anything the adapter cannot unwrap defaults to a deny option — hence
 * the UI must never send free text in place of a structured reply.
 */

export interface PermissionOption {
  id: string
  label: string
}

export interface PermissionRequest {
  question: string
  options: PermissionOption[]
}

const REQUEST_MARKER = '[permission_request] options='

const RESPONSE_RE = /^\s*\[permission_response\]\s*option_id="([^"]*)"\s*$/

/**
 * parsePermissionRequest extracts the question and options from a
 * UserInputRequested prompt. Returns null for prompts without the
 * `[permission_request]` marker (plain questions) and for payloads whose
 * options array does not parse as JSON — both render as ordinary free-text
 * asks.
 */
export function parsePermissionRequest(prompt: string): PermissionRequest | null {
  const markerAt = prompt.indexOf(REQUEST_MARKER)
  if (markerAt < 0) return null
  const question = prompt.slice(0, markerAt).trim()
  const json = prompt.slice(markerAt + REQUEST_MARKER.length).trim()
  let parsed: unknown
  try {
    parsed = JSON.parse(json)
  } catch {
    return null
  }
  if (!Array.isArray(parsed)) return null
  const options = parsed.flatMap((raw): PermissionOption[] => {
    if (typeof raw !== 'object' || raw === null) return []
    const rec = raw as Record<string, unknown>
    if (typeof rec.id !== 'string' || typeof rec.label !== 'string') return []
    return [{ id: rec.id, label: rec.label }]
  })
  if (options.length === 0) return null
  return { question, options }
}

/** formatPermissionReply builds the reply line the adapter unwraps. */
export function formatPermissionReply(optionId: string): string {
  return `[permission_response] option_id="${optionId}"`
}

/** permissionReplyOptionID returns the chosen option id when content is
 * exactly a permission reply, else null. */
export function permissionReplyOptionID(content: string): string | null {
  const m = RESPONSE_RE.exec(content)
  return m ? m[1] : null
}
