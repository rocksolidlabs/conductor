import { describe, expect, it } from 'vitest'
import { holdsBreak, pasteHeld, pasteKeys, startupShown } from './terminalPaste'

describe('the touch terminal’s paste control (#902)', () => {
  it('takes every control character out of the text, never turning one into a key', () => {
    expect(pasteKeys('a\nb')).toBe('ab')
    expect(pasteKeys('a\r\nb\tc\u0007d\u007fe\u009bf')).toBe('abcdef')
    expect(pasteKeys('pasted on a phone\n')).toBe('pasted on a phone')
    expect(pasteKeys('código ✓')).toBe('código ✓')
  })

  it('leaves no ESC to end a bracketed paste early: the digit after it stays inside the paste', () => {
    const keys = pasteKeys('code\u001b[201~1')
    expect(keys).toBe('code[201~1')
    expect(keys.includes('\u001b')).toBe(false)
  })

  it('says when the text holds a line break', () => {
    expect(holdsBreak('a\nb')).toBe(true)
    expect(holdsBreak('a\rb')).toBe(true)
    expect(holdsBreak('ab')).toBe(false)
  })

  it('is held while a startup question that takes no line shows, and not at the sign-in screen', () => {
    expect(pasteHeld({ state: 'needs_input', source: 'trust', options: [{ label: 'Yes', input: '1' }] })).toBe(true)
    expect(pasteHeld({ state: 'needs_input', source: 'trust' })).toBe(true)
    expect(pasteHeld({ state: 'needs_input', source: 'trust', line: true })).toBe(false)
    expect(pasteHeld({ state: 'needs_input', source: 'hook' })).toBe(false)
    expect(pasteHeld(undefined)).toBe(false)
  })

  it('names the startup question showing, to tell when another takes its place', () => {
    expect(startupShown({ state: 'needs_input', source: 'trust', id: 'q1' })).toBe('q1')
    expect(startupShown({ state: 'needs_input', source: 'hook', id: 'q2' })).toBe('')
    expect(startupShown({ state: 'working', source: 'trust', id: 'q3' })).toBe('')
  })
})
