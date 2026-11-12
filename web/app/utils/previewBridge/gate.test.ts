import { describe, expect, it } from 'vitest'
import { MAX_PREVIEW_TO_WORKBENCH, PREVIEW_MARK, previewMeasure } from '../protocol'
import { createGate, originsOf, type Answer } from './gate'

const WORKBENCH = 'http://127.0.0.1:8080'
const NONCE = 'n0nce-of-the-hello-1234'
const PAGE = { url: 'http://127.0.0.1:5173/', title: '', viewport: { width: 1, height: 1, dpr: 1, scrollX: 0, scrollY: 0, colorScheme: 'light' as const, userAgent: '' }, epoch: 0, revision: '' }

function setup() {
  const posts: Array<{ message: unknown; target: string }> = []
  const parent = { postMessage: (message: unknown, target: string) => posts.push({ message, target }) }
  const foreign = { postMessage: () => { throw new Error('a foreign window is never posted to') } }
  const gate = createGate([WORKBENCH, 'http://localhost:8080'], parent)
  const m = (t: string, extra: Record<string, unknown> = {}, nonce = NONCE) => ({ conductor: PREVIEW_MARK, t, nonce, ...extra })
  // An event as the browser delivers it: trusted, from the parent, at the workbench's origin, unless the case says otherwise.
  const ev = (data: unknown, e: { origin?: string; source?: unknown; isTrusted?: boolean } = {}) => ({ isTrusted: true, source: parent, origin: WORKBENCH, data, ...e })
  return { posts, parent, foreign, gate, m, ev }
}

describe('the bridge gate', () => {
  it('takes a hello from the parent at a workbench origin, and answers it there alone', () => {
    const { posts, gate, m, ev } = setup()
    expect(gate.answer({ t: 'cancelled' })).toBe(false)
    expect(posts).toEqual([]) // nothing before a hello
    expect(gate.take(ev(m('hello')))?.t).toBe('hello')
    expect(gate.take(ev(m('select')))?.t).toBe('select')
    expect(gate.answer({ t: 'cancelled' })).toBe(true)
    expect(posts).toEqual([{ message: { t: 'cancelled', conductor: PREVIEW_MARK, nonce: NONCE }, target: WORKBENCH }])
    for (const p of posts) expect(p.target).not.toBe('*')
  })

  it('drops what another origin, another window, a page-made event, a wrong nonce, a big message or an unknown type sends, and keeps its state', () => {
    const { posts, foreign, gate, m, ev } = setup()
    const refused = [
      ev(m('hello'), { origin: 'http://127.0.0.1:5173' }),
      // The origin compared exactly: no prefix, no case folded, no other port, no path.
      ev(m('hello'), { origin: 'http://127.0.0.1:80800' }),
      ev(m('hello'), { origin: 'HTTP://127.0.0.1:8080' }),
      ev(m('hello'), { origin: 'http://127.0.0.1' }),
      ev(m('hello'), { origin: WORKBENCH + '/' }),
      ev(m('hello'), { origin: 'null' }),
      ev(m('hello'), { source: foreign }),
      ev(m('hello'), { source: null }),
      // An event the page's own script made says what it likes of its origin and source; the browser marks it untrusted.
      ev(m('hello'), { isTrusted: false }),
    ]
    for (const e of refused) expect(gate.take(e)).toBeNull()
    expect(gate.take(ev(m('select')))).toBeNull() // before any hello
    expect(gate.take(ev(m('hello')))).not.toBeNull()
    for (const data of [
      m('select', {}, 'another-nonce-0000000'),
      { conductor: PREVIEW_MARK, t: 'select' },
      m('describe', { selector: 'a'.repeat(5000) }),
      m('eval', { code: '1' }),
      // An answer's type sent to the bridge is no request.
      m('cancelled'),
      m('ready', { version: 1, hints: [], ...PAGE }),
      'hello',
      // A value JSON cannot hold is dropped, never thrown.
      m('select', { x: 1n }),
    ])
      expect(gate.take(ev(data))).toBeNull()
    // A refused hello, whatever its nonce, leaves the exchange as it was: the first hello's nonce and origin still answer.
    for (const e of refused) gate.take({ ...e, data: m('hello', {}, 'another-nonce-0000000') })
    expect(gate.take(ev(m('look')))?.t).toBe('look')
    expect(gate.take(ev(m('look', {}, 'another-nonce-0000000')))).toBeNull()
    // The other workbench origin may not speak inside this hello's exchange; its own hello starts another.
    expect(gate.take(ev(m('select'), { origin: 'http://localhost:8080' }))).toBeNull()
    expect(posts).toEqual([])
    expect(gate.take(ev(m('hello', {}, 'another-nonce-0000000'), { origin: 'http://localhost:8080' }))).not.toBeNull()
    gate.answer({ t: 'cancelled' })
    expect(posts.map((p) => p.target)).toEqual(['http://localhost:8080'])
  })

  it("posts only a message the workbench's parser takes, within the way's bound", () => {
    const { posts, gate, m, ev } = setup()
    gate.take(ev(m('hello')))
    expect(gate.answer({ t: 'looked', ...PAGE })).toBe(true)
    // A message its writer got wrong is not posted: a title past its bound, a page past the way's bound, a report not an element's.
    expect(gate.answer({ t: 'looked', ...PAGE, title: 'x'.repeat(201) })).toBe(false)
    const big = { t: 'looked', ...PAGE, url: 'x'.repeat(MAX_PREVIEW_TO_WORKBENCH) } as Answer
    expect(previewMeasure(big)).toBeGreaterThan(MAX_PREVIEW_TO_WORKBENCH)
    expect(gate.answer(big)).toBe(false)
    expect(gate.answer({ t: 'selected', ...PAGE, element: {} } as unknown as Answer)).toBe(false)
    expect(posts).toHaveLength(1)
  })

  it('ends the exchange before an accepted hello takes its nonce, and only for an accepted hello', () => {
    const { m, ev } = setup()
    const seen: string[] = []
    const parent = { postMessage: (message: unknown) => seen.push((message as { nonce: string }).nonce) }
    let gate: ReturnType<typeof createGate> | null = null
    gate = createGate([WORKBENCH], parent, () => (seen.push('ending'), gate!.answer({ t: 'cancelled' })))
    const at = (e: ReturnType<typeof ev>) => ({ ...e, source: parent })
    gate.take(at(ev(m('hello'))))
    gate.take(at(ev(m('hello', {}, 'another-nonce-0000000'), { isTrusted: false })))
    gate.take(at(ev(m('select'))))
    gate.take(at(ev(m('hello', {}, 'another-nonce-0000000'))))
    // What ending did was of the exchange it ended: the first hello found none, the second ended the first's.
    expect(seen).toEqual(['ending', 'ending', NONCE])
  })

  it('takes only the origins the server wrote in', () => {
    expect(originsOf(undefined)).toEqual([])
    expect(originsOf('http://127.0.0.1:8080')).toEqual([])
    expect(originsOf([WORKBENCH, '*', 'null', '', 7, 'x'.repeat(3000)])).toEqual([WORKBENCH])
  })
})
