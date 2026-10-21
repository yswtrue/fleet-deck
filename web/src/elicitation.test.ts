import { describe, expect, it } from 'vitest'
import {
  buildElicitationContent,
  formatElicitationReply,
  parseElicitationReply,
  parseElicitationRequest,
} from './elicitation.ts'

const schema = {
  type: 'object',
  properties: {
    value: { type: 'string', enum: ['allow', 'deny'] },
    note: { type: 'string', title: 'Note', minLength: 2, maxLength: 5 },
    retries: { type: 'integer', minimum: 0, maximum: 3, default: 1 },
    force: { type: 'boolean', default: false },
    tags: {
      type: 'array',
      items: {
        anyOf: [
          { const: 'a', title: 'A' },
          { const: 'b', title: 'B' },
        ],
      },
      minItems: 1,
    },
  },
  required: ['value', 'tags'],
}

const prompt = `How should I proceed?\n\n[elicitation_request] schema=${JSON.stringify(schema)}`

describe('elicitation wire', () => {
  it('parses the message and every supported field shape', () => {
    const request = parseElicitationRequest(prompt)
    expect(request).not.toBeNull()
    expect(request?.message).toBe('How should I proceed?')

    const byName = new Map(request?.fields.map((field) => [field.name, field]))
    expect(byName.get('value')).toMatchObject({
      kind: 'select',
      required: true,
      options: [
        { value: 'allow', label: 'allow' },
        { value: 'deny', label: 'deny' },
      ],
    })
    expect(byName.get('note')).toMatchObject({
      kind: 'string',
      title: 'Note',
      minLength: 2,
      maxLength: 5,
    })
    expect(byName.get('retries')).toMatchObject({ kind: 'integer', default: 1, minimum: 0 })
    expect(byName.get('force')).toMatchObject({ kind: 'boolean', default: false })
    expect(byName.get('tags')).toMatchObject({
      kind: 'multiselect',
      required: true,
      minItems: 1,
      options: [
        { value: 'a', label: 'A' },
        { value: 'b', label: 'B' },
      ],
    })
  })

  it('returns null for prompts without the marker or with a broken schema', () => {
    expect(parseElicitationRequest('plain question')).toBeNull()
    expect(parseElicitationRequest('q\n\n[elicitation_request] schema=not-json')).toBeNull()
    expect(parseElicitationRequest('q\n\n[elicitation_request] schema=[]')).toBeNull()
    expect(parseElicitationRequest('q\n\n[elicitation_request] schema={"type":"string"}')).toBe(
      null
    )
  })

  it('marks unknown property types as unsupported instead of guessing a control', () => {
    const request = parseElicitationRequest(
      `q\n\n[elicitation_request] schema=${JSON.stringify({
        type: 'object',
        properties: { nested: { type: 'object', properties: {} } },
      })}`
    )
    expect(request?.fields[0]).toMatchObject({ name: 'nested', kind: 'unsupported' })
  })

  it('formats and parses replies without drift', () => {
    expect(formatElicitationReply('accept', { value: 'allow' })).toBe(
      '[elicitation_response] action="accept" content={"value":"allow"}'
    )
    expect(formatElicitationReply('decline')).toBe('[elicitation_response] action="decline"')
    expect(parseElicitationReply('[elicitation_response] action="accept" content={"a":1}')).toEqual(
      { action: 'accept', content: { a: 1 } }
    )
    expect(parseElicitationReply('  [elicitation_response] action="cancel"  ')).toEqual({
      action: 'cancel',
    })
    expect(parseElicitationReply('not a reply')).toBeNull()
  })
})

describe('buildElicitationContent', () => {
  const request = parseElicitationRequest(prompt)!
  const valid = { value: 'allow', note: 'ok', retries: 2, force: false, tags: ['a'] }

  it('returns schema-conforming content', () => {
    const result = buildElicitationContent(request, valid)
    expect(result.errors).toEqual({})
    expect(result.content).toEqual(valid)
  })

  it('rejects missing required fields and empty selections', () => {
    expect(buildElicitationContent(request, { tags: ['a'] }).errors).toEqual({
      value: 'required',
    })
    expect(buildElicitationContent(request, { value: 'allow', tags: [] }).errors).toEqual({
      tags: 'range',
    })
  })

  it('rejects values outside the offered enum or constraints', () => {
    expect(buildElicitationContent(request, { ...valid, value: 'evil' }).errors).toEqual({
      value: 'option',
    })
    expect(buildElicitationContent(request, { ...valid, note: 'x' }).errors).toEqual({
      note: 'range',
    })
    expect(buildElicitationContent(request, { ...valid, retries: 9 }).errors).toEqual({
      retries: 'range',
    })
    expect(buildElicitationContent(request, { ...valid, retries: 1.5 }).errors).toEqual({
      retries: 'invalid',
    })
    expect(buildElicitationContent(request, { ...valid, tags: ['z'] }).errors).toEqual({
      tags: 'option',
    })
  })

  it('keeps false and zero as real values', () => {
    const result = buildElicitationContent(request, {
      value: 'deny',
      retries: 0,
      force: false,
      tags: ['b'],
    })
    expect(result.errors).toEqual({})
    expect(result.content).toMatchObject({ retries: 0, force: false })
  })
})
