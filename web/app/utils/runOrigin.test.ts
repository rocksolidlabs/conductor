import { describe, expect, it } from 'vitest'
import type { RunOrigin } from '~/composables/useSessions'
import { cutForEye, ORIGIN_LABEL_SHOWN, ORIGIN_SUBJECT_SHOWN, runOriginLine, shownText, subjectHref } from './runOrigin'

const sha = 'a1b2c3d4e5f60718293a4b5c6d7e8f9012345678'
const base: RunOrigin = { provider: 'example', ref: 'example:42', label: 'Nightly checks' }

describe('runOriginLine', () => {
  it('is null for a run no provider launched', () => {
    expect(runOriginLine(undefined)).toBeNull()
    expect(runOriginLine(null)).toBeNull()
  })

  it('names the label alone, then the subject, then the start commit, short', () => {
    expect(runOriginLine(base)).toEqual({ text: 'Launched by Nightly checks', lead: 'Launched by', label: 'Nightly checks', subject: undefined, href: undefined, commit: undefined })
    const all = runOriginLine({ ...base, subject: 'change:git.example.com/acme/web#12@' + sha, subjectUrl: 'https://git.example.com/acme/web/pull/12', startCommit: sha })!
    expect(all.text).toBe(`Launched by Nightly checks for change:git.example.com/acme/web#12@${sha} at ${sha}`)
    expect(all.commit).toBe('a1b2c3d')
    expect(all.href).toBe('https://git.example.com/acme/web/pull/12')
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

  it('keeps markup as text', () => {
    const l = runOriginLine({ ...base, label: '<img src=x onerror=alert(1)>', subject: 'example:<b>' })!
    expect(l.label).toBe('<img src=x onerror=alert(1)>')
    expect(l.text).toBe('Launched by <img src=x onerror=alert(1)> for example:<b>')
  })
})
