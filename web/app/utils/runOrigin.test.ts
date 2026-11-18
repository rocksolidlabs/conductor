import { describe, expect, it } from 'vitest'
import type { RunOrigin } from '~/composables/useSessions'
import { cutForEye, ORIGIN_LABEL_SHOWN, ORIGIN_SUBJECT_SHOWN, runOriginLine, shownText, subjectHref, timeLimitPassed } from './runOrigin'

const sha = 'a1b2c3d4e5f60718293a4b5c6d7e8f9012345678'
const base: RunOrigin = { provider: 'example', ref: 'example:42', label: 'Nightly checks' }

// Characters that draw nothing, each one the task file's quoting replaces with U+FFFD.
const HIDDEN_CHARS = ['\u{200b}', '\u{200c}', '\u{200d}', '\u{2060}', '\u{feff}', '\u{ad}', '\u{2028}', '\u{2029}', '\u{180e}', '\u{3164}', '\u{115f}', '\u{fe0f}', '\u{e0041}', '\u{e0001}', '\u{e000}', '\u{34f}', '\u{202e}', '\u{2066}', '\u{7}', '\u{9b}']

describe('timeLimitPassed', () => {
  it('is past the deadline only, and never without one', () => {
    const deadline = '2026-11-18T10:00:00Z'
    expect(timeLimitPassed({ ...base, deadline }, Date.parse(deadline) - 1)).toBe(false)
    expect(timeLimitPassed({ ...base, deadline }, Date.parse(deadline))).toBe(true)
    expect(timeLimitPassed(base, Date.parse(deadline) + 1e12)).toBe(false)
    expect(timeLimitPassed(undefined, 0)).toBe(false)
    expect(timeLimitPassed({ ...base, deadline: 'not a time' }, 0)).toBe(false)
  })
})

describe('runOriginLine', () => {
  it('is null for a run no provider launched', () => {
    expect(runOriginLine(undefined)).toBeNull()
    expect(runOriginLine(null)).toBeNull()
  })

  it('names the label alone, then the subject, then the start commit, short', () => {
    expect(runOriginLine(base)).toEqual({ text: 'Launched by Nightly checks', lead: 'Launched by', label: 'Nightly checks', subject: undefined, href: undefined, host: undefined, commit: undefined })
    const all = runOriginLine({ ...base, subject: 'change:git.example.com/acme/web#12@' + sha, subjectUrl: 'https://git.example.com/acme/web/pull/12', startCommit: sha })!
    expect(all.text).toBe(`Launched by Nightly checks for change:git.example.com/acme/web#12@${sha} (its link opens https://git.example.com/acme/web/pull/12) at ${sha}`)
    expect(all.commit).toBe('a1b2c3d')
    expect(all.href).toBe('https://git.example.com/acme/web/pull/12')
    expect(all.host).toBe('git.example.com')
    const noSubject = runOriginLine({ ...base, startCommit: sha })!
    expect(noSubject.text).toBe(`Launched by Nightly checks at ${sha}`)
    expect(noSubject.subject).toBeUndefined()
  })

  it('says a continuation continues a run a provider launched', () => {
    const l = runOriginLine({ ...base, continued: true, subject: 'example:42' })!
    expect(l.lead).toBe('Continues a run launched by')
    expect(l.text).toBe('Continues a run launched by Nightly checks for example:42')
  })

  it('offers no link without a subject, and none for a link the page may not open', () => {
    expect(runOriginLine({ ...base, subjectUrl: 'https://example.com/42' })!.href).toBeUndefined()
    for (const url of [
      'javascript:alert(1)',
      'data:text/html,<b>x</b>',
      'http://example.com/42',
      'https://u:p@example.com/42',
      'https://u@example.com/42',
      'https://example.com/42?token=abc',
      'https://example.com/42?',
      'https://example.com/42#frag',
      'HTTPS://example.com/42',
      'https:///42',
      'https://example.com\\@evil.example/42',
      'https://example.com/a b',
      'https://example.com/' + 'a'.repeat(2048),
    ]) {
      const l = runOriginLine({ ...base, subject: 'example:42', subjectUrl: url })!
      expect(l.href, url).toBeUndefined()
      expect(l.text, url).not.toContain(url)
    }
    expect(subjectHref('https://example.com:8443/a/b%20c')).toBe('https://example.com:8443/a/b%20c')
  })

  it('cuts long parts for the eye and keeps them whole in the text', () => {
    const label = 'é'.repeat(60)
    const subject = 'example:' + 'x'.repeat(504)
    const l = runOriginLine({ ...base, label, subject })!
    expect(Array.from(l.label)).toHaveLength(ORIGIN_LABEL_SHOWN)
    expect(l.label.endsWith('…')).toBe(true)
    expect(Array.from(l.subject!)).toHaveLength(ORIGIN_SUBJECT_SHOWN)
    expect(l.text).toContain(label)
    expect(l.text).toContain(subject)
    expect(cutForEye('short', 40)).toBe('short')
    expect(cutForEye('😀'.repeat(41), 40)).toBe('😀'.repeat(39) + '…')
  })

  it('shows a bidirectional or control character as a mark, never applied', () => {
    const l = runOriginLine({ ...base, label: 'abc\u202edef', subject: 'example:\u2066@x\u2069@' })!
    expect(l.label).toBe('abc\ufffddef')
    expect(l.subject).toBe('example:\ufffd@x\ufffd@')
    expect(l.text).not.toMatch(/[\u061c\u200e\u200f\u202a-\u202e\u2066-\u2069]/)
    expect(shownText('a\u0007b\u009bc')).toBe('a\ufffdb\ufffdc')
  })

  it('names the host a link opens, whatever host its reference names', () => {
    const l = runOriginLine({ ...base, subject: 'change:git.example.com/acme/web#12@' + sha, subjectUrl: 'https://other.example:8443/x' })!
    expect(l.host).toBe('other.example:8443')
    expect(l.text).toContain('(its link opens https://other.example:8443/x)')
    expect(runOriginLine({ ...base, subject: 'example:42', subjectUrl: 'http://other.example/x' })!.host).toBeUndefined()
  })

  it('marks every character the task file replaces: zero-width, invisible, separators, tags, selectors', () => {
    for (const c of HIDDEN_CHARS) {
      expect(shownText(`a${c}b`), `U+${c.codePointAt(0)!.toString(16)}`).toBe('a\ufffdb')
    }
    expect(shownText('Café · naïve — 日本 😀 tab-free')).toBe('Café · naïve — 日本 😀 tab-free')
  })

  it('keeps markup as text', () => {
    const l = runOriginLine({ ...base, label: '<img src=x onerror=alert(1)>', subject: 'example:<b>' })!
    expect(l.label).toBe('<img src=x onerror=alert(1)>')
    expect(l.text).toBe('Launched by <img src=x onerror=alert(1)> for example:<b>')
  })
})
