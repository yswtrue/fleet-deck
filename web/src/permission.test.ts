import { describe, expect, it } from 'vitest'
import { formatPermissionReply, parsePermissionRequest, permissionReplyOptionID } from './permission.ts'

// The permission wire convention is shared with the ACP adapter
// (internal/adapters/acp/permission.go): these tests pin the web side of the
// format so it cannot drift from the adapter's regex.

describe('parsePermissionRequest', () => {
  it('extracts question and options from an ACP-shaped prompt', () => {
    const prompt =
      'Allow the agent to edit /tmp/x.go?\n\n[permission_request] options=' +
      '[{"id":"allow-once","label":"Allow"},{"id":"deny","label":"Deny"}]'
    const got = parsePermissionRequest(prompt)
    expect(got).toEqual({
      question: 'Allow the agent to edit /tmp/x.go?',
      options: [
        { id: 'allow-once', label: 'Allow' },
        { id: 'deny', label: 'Deny' },
      ],
    })
  })

  it('returns null for plain questions without the marker', () => {
    expect(parsePermissionRequest('Should I proceed?')).toBeNull()
    expect(parsePermissionRequest('[permission_response] option_id="x"')).toBeNull()
  })

  it('returns null for unparseable options payloads', () => {
    expect(parsePermissionRequest('q?\n\n[permission_request] options=not-json')).toBeNull()
    expect(parsePermissionRequest('q?\n\n[permission_request] options={"id":"x"}')).toBeNull()
    expect(parsePermissionRequest('q?\n\n[permission_request] options=[]')).toBeNull()
  })

  it('drops options whose id or label is not a string', () => {
    const got = parsePermissionRequest(
      'q?\n\n[permission_request] options=[{"id":"ok","label":"Ok"},{"id":7,"label":"Bad"},{"id":"x"}]'
    )
    expect(got?.options).toEqual([{ id: 'ok', label: 'Ok' }])
  })
})

describe('formatPermissionReply', () => {
  it('emits exactly the marker the adapter regex unwraps', () => {
    expect(formatPermissionReply('allow-once')).toBe('[permission_response] option_id="allow-once"')
  })
})

describe('permissionReplyOptionID', () => {
  it('recognizes an exact reply line', () => {
    expect(permissionReplyOptionID('[permission_response] option_id="deny"')).toBe('deny')
    expect(permissionReplyOptionID('  [permission_response]  option_id=""')).toBe('')
  })

  it('rejects replies with surrounding free text or wrong shapes', () => {
    // The adapter's regex would still find a marker here, but the UI only
    // pretty-replies exact rows; everything else stays verbatim text.
    expect(permissionReplyOptionID('yes [permission_response] option_id="allow"')).toBeNull()
    expect(permissionReplyOptionID('[permission_request] options=[]')).toBeNull()
    expect(permissionReplyOptionID('plain text')).toBeNull()
  })
})
