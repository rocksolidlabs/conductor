import { describe, expect, it } from 'vitest'
import { sayLine, sayOutcomeOf, sayWords } from './sayWords'

describe('the words after a line typed into a session (#322)', () => {
  it('typed, when the owner said the line was entered; the draft goes', () => {
    const r = sayWords('docs', 'looks good', { typed: true })
    expect(r.toast).toEqual({ title: 'Typed into docs', description: "looks good · entered, the session's owner says", color: 'success' })
    expect(r.keepDraft).toBe(false)
  })

  it('sent, to an older owner that says nothing', () => {
    const r = sayWords('docs', 'looks good', { typed: false })
    expect(r.toast.title).toBe('Sent to docs')
    expect(r.toast.description).toContain('nothing confirms it landed')
    expect(r.toast.color).toBe('neutral')
    expect(r.keepDraft).toBe(false)
  })

  it('not sent, with why, the line kept for the field', () => {
    const r = sayWords('docs', 'looks good', { refused: 'waiting on a prompt' })
    expect(r.toast).toEqual({ title: 'docs: not sent', description: "waiting on a prompt · the line waits in the row's field", color: 'warning' })
    expect(r.keepDraft).toBe(true)
  })

  it('not confirmed, when the fate of the line is not known: the line is not offered again', () => {
    const r = sayWords('docs', 'looks good', { unconfirmed: 'the session did not say whether it typed the line' })
    expect(r.toast).toEqual({ title: 'docs: not confirmed', description: 'the session did not say whether it typed the line · look at its terminal before typing the line again', color: 'warning' })
    expect(r.keepDraft).toBe(false)
  })

  it('tells a refusal that typed nothing from an outcome left open, by the code', () => {
    for (const code of ['not_sent', 'read_only', 'session_ended', 'too_many_requests', 'bad_frame', 'not_connected', 'too_long']) expect(sayOutcomeOf(code, 'why')).toEqual({ refused: 'why' })
    for (const code of ['input_failed', 'no_word', 'closed', 'other']) expect(sayOutcomeOf(code, 'why')).toEqual({ unconfirmed: 'why' })
  })

  it('cuts a long line in the toast', () => {
    const long = 'x'.repeat(100)
    expect(sayLine(long)).toBe(`${'x'.repeat(80)}…`)
    expect(sayWords('docs', long, { typed: true }).toast.description.startsWith(`${'x'.repeat(80)}…`)).toBe(true)
  })
})
