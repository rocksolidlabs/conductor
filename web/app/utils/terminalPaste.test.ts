import { describe, expect, it } from 'vitest'
import { liveAttention } from './attention'
import type { Attention } from './protocol'
import { holdsBreak, pasteHeld, pasteKeys, startupShown } from './terminalPaste'

describe('the terminal’s paste control (#902)', () => {
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

  it('is offered for every attention but a startup question with choices, answered in the terminal or not', () => {
    const choices = [{ label: 'Yes, I trust this folder', input: '\r' }, { label: 'No, exit', input: '\u001b[B\r' }]
    const trust: Attention = { state: 'needs_input', source: 'trust', kind: 'prompt', id: 'q1', message: 'Trust this folder?', options: choices }
    const signIn: Attention = { state: 'needs_input', source: 'trust', kind: 'prompt', id: 'q2', message: "Browser didn't open? Use the url below to sign in" }
    // Held: a trust question, an update dialog or a numbered choice (a startup question with choices), and the same answered by keys
    // typed in the terminal while its screen is still drawn.
    const held: Attention[] = [trust, { ...trust, options: [{ label: 'Update now', input: '1' }] }, { ...trust, answered: 'terminal' }]
    for (const a of held) expect(pasteHeld(a), JSON.stringify(a)).toBe(true)
    // Its live form reads as no question at all (liveAttention): the reason the control is judged on the attention as received.
    expect(pasteHeld(liveAttention({ ...trust, answered: 'terminal' }))).toBe(false)
    // Offered: a startup question with no choices (the sign-in screen that asks for a code), answered in the terminal or not; every
    // attention of any other source, kind or state; none at all.
    const offered: Array<Attention | undefined> = [
      signIn,
      { ...signIn, answered: 'terminal' },
      { ...signIn, options: [] },
      ...(['api', 'admin', 'bell', 'osc', 'pattern', 'question', 'submit', 'input', 'hook'] as const).map((source): Attention => ({ ...trust, source })),
      { ...trust, source: 'question', kind: 'permission', options: undefined },
      { ...trust, state: 'working' },
      { ...trust, state: 'done' },
      { ...trust, state: '' },
      { state: '', source: 'input', answered: 'terminal' },
      undefined,
    ]
    for (const a of offered) expect(pasteHeld(a), JSON.stringify(a)).toBe(false)
  })

  it('names the startup question showing, to tell when another takes its place', () => {
    expect(startupShown({ state: 'needs_input', source: 'trust', id: 'q1' })).toBe('q1')
    expect(startupShown({ state: 'needs_input', source: 'trust', id: 'q1', answered: 'terminal' } as Attention)).toBe('q1')
    expect(startupShown({ state: 'needs_input', source: 'hook', id: 'q2' })).toBe('')
    expect(startupShown({ state: 'working', source: 'trust', id: 'q3' })).toBe('')
    expect(startupShown(undefined)).toBe('')
  })
})
