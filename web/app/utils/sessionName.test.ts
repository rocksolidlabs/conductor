import { describe, expect, it } from 'vitest'
import { MAX_SESSION_NAME } from './protocol'
import { cleanSessionName, nameBytes, renameDraft } from './sessionName'

const controls = String.fromCharCode(0x00, 0x1b, 0x7f, 0x85, 0x9b)

describe('a session name as the server keeps it (#318)', () => {
  it('loses its control characters, C0, DEL and C1, and the space around it', () => {
    expect(cleanSessionName('  docs sweep \t')).toBe('docs sweep')
    expect(cleanSessionName(`a${controls}b\nc\r`)).toBe('abc')
    expect(cleanSessionName(` \t\n${controls} `)).toBe('')
    expect(cleanSessionName('日本語の名前')).toBe('日本語の名前')
    expect(cleanSessionName('😀 ok')).toBe('😀 ok')
  })

  it('is counted in UTF-8 bytes, as the server bounds it', () => {
    expect(nameBytes('abc')).toBe(3)
    expect(nameBytes('é')).toBe(2)
    expect(nameBytes('名')).toBe(3)
    expect(nameBytes('😀')).toBe(4)
  })

  it('cannot be sent with nothing left, or past the bound, which the server would cut', () => {
    expect(renameDraft('  new name ')).toEqual({ name: 'new name', bytes: 8 })
    expect(renameDraft(` \t${controls} `)).toMatchObject({ name: '', problem: 'empty' })
    expect(renameDraft('a'.repeat(MAX_SESSION_NAME))).toEqual({ name: 'a'.repeat(MAX_SESSION_NAME), bytes: MAX_SESSION_NAME })
    expect(renameDraft('a'.repeat(MAX_SESSION_NAME + 1))).toMatchObject({ problem: 'long', bytes: MAX_SESSION_NAME + 1 })
    // Forty three-byte characters fit exactly; a forty-first does not.
    expect(renameDraft('名'.repeat(40)).problem).toBeUndefined()
    expect(renameDraft('名'.repeat(41))).toMatchObject({ problem: 'long', bytes: 123 })
    // The space trimmed off does not count.
    expect(renameDraft(`  ${'a'.repeat(MAX_SESSION_NAME)}  `).problem).toBeUndefined()
  })
})
