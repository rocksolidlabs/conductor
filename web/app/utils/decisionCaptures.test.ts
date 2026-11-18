import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import type { CaptureDocument, CaptureSendAnswer } from './capture'
import { MAX_CARD_CAPTURES, ROW_NOTE_CHARS, asksScreenshot, captureReads, cardSends, historyCaptureRefs, captureRefWords, captureRowShot, captureRowWords, captureShotWords } from './decisionCaptures'
import type { DecisionEntry } from './decisions'

// What a decision's card draws of the captures sent to it, for whom (utils/decisionCaptures.ts): the operator reads every capture
// route and is drawn each capture's screenshot, note and element; a reader the capture routes refuse asks none of them and is drawn
// the references the decision's history gives, as words.

const C = 'a'.repeat(16)
const S = 'b'.repeat(16)
const comment = (capture: string, send: string, rev = 1, seq = 1, by = 'Nate'): DecisionEntry => ({
  seq,
  at: `2026-11-18T00:00:0${seq}Z`,
  kind: 'commented',
  by,
  detail: { text: `Capture ${capture} is attached at revision ${rev}: what was sent is capture:${capture}/${send}.` },
})
const send = (id: string, at: string, state: CaptureSendAnswer['state'] = 'sent'): CaptureSendAnswer => ({
  id,
  capture: C,
  rev: 1,
  kind: 'decision',
  asked: {},
  digest: '',
  createdAt: at,
  actor: 'admin',
  opId: id,
  state,
  outcomes: [],
  cancels: [],
})
const doc = (over: Partial<CaptureDocument> = {}): Pick<CaptureDocument, 'title' | 'url' | 'annotations' | 'image'> => ({
  title: 'Settings',
  url: 'http://127.0.0.1:5000/settings?t=SECRET#k=SECRET',
  image: { sha256: 'f'.repeat(64), how: 'tab', width: 800, height: 600 },
  annotations: [
    {
      k: 1,
      kind: 'element',
      note: 'The save button is covered',
      rect: { x: 0, y: 0, width: 10, height: 10 },
      element: { tag: 'button', id: 'under', classes: ['under'], name: 'Save', selector: 'button#under', box: { x: 0, y: 0, width: 10, height: 10 }, state: {} as never, hit: { self: false, selector: 'span.absolute.-inset-1', tag: 'span' } },
    },
  ],
  ...over,
})

describe('who reads what', () => {
  it('the operator may ask every capture route; a refused reader asks none', () => {
    expect(captureReads('operator')).toEqual({ sends: true, documents: true, screenshots: true, capture: true })
    expect(captureReads('refused')).toEqual({ sends: false, documents: false, screenshots: false, capture: false })
  })

  it('a refused reader is drawn the references its history gives, newest first, one per send, in the server’s own words alone', () => {
    const other = 'c'.repeat(16)
    const refs = historyCaptureRefs([
      comment(C, S, 1, 1),
      { seq: 2, at: '2026-11-18T00:00:02Z', kind: 'asked' },
      comment(C, other, 2, 3, 'Ana'),
      comment(C, S, 1, 4),
      // A person's comment naming a capture is not a send's.
      { seq: 5, at: '2026-11-18T00:00:05Z', kind: 'commented', by: 'Bo', detail: { text: `see capture:${C}/${'d'.repeat(16)}` } },
      // The server's words, but two captures that disagree.
      { ...comment(C, 'e'.repeat(16), 1, 6), detail: { text: `Capture ${C} is attached at revision 1: what was sent is capture:${other}/${'e'.repeat(16)}.` } },
      { seq: 7, at: '2026-11-18T00:00:07Z', kind: 'answered', detail: { text: `Capture ${C} is attached at revision 1: what was sent is capture:${C}/${'9'.repeat(16)}.` } },
    ])
    expect(refs).toEqual([
      { capture: C, send: other, rev: 2, by: 'Ana', at: '2026-11-18T00:00:03Z' },
      { capture: C, send: S, rev: 1, by: 'Nate', at: '2026-11-18T00:00:01Z' },
    ])
    expect(historyCaptureRefs(undefined)).toEqual([])
    expect(captureRefWords(refs[0]!)).toBe('revision 2 · sent by Ana')
  })
})

describe('the operator’s rows', () => {
  it('list the sends sent, newest first, the newest few read whole and the rest counted', () => {
    const many = Array.from({ length: MAX_CARD_CAPTURES + 2 }, (_, i) => send(i.toString(16).padStart(16, '0'), `2026-11-18T00:00:${String(10 + i)}Z`))
    const got = cardSends([...many, send('f'.repeat(16), '2026-11-18T01:00:00Z', 'pending')])
    expect(got.shown).toHaveLength(MAX_CARD_CAPTURES)
    expect(got.shown[0]!.id).toBe(many[many.length - 1]!.id)
    expect(got.more).toBe(2)
    expect(cardSends([send(S, '2026-11-18T00:00:00Z', 'refused')])).toEqual({ shown: [], more: 0 })
  })

  it('draw a screenshot only once its bytes are read, and words in its place otherwise: never an image the browser would ask itself', () => {
    const d = doc()
    expect(asksScreenshot(d)).toBe(true)
    expect(captureRowShot(undefined, undefined)).toEqual({ kind: 'reading' })
    expect(captureRowShot(undefined, { failed: true })).toEqual({ kind: 'unread' })
    expect(captureRowShot(d, undefined)).toEqual({ kind: 'reading' })
    expect(captureRowShot(d, {})).toEqual({ kind: 'reading' })
    expect(captureRowShot(d, { src: 'blob:x' })).toEqual({ kind: 'shown', src: 'blob:x' })
    expect(captureRowShot(d, { failed: true })).toEqual({ kind: 'unread' })
    expect(captureShotWords({ kind: 'unread' })).toMatch(/could not be read/)
    const none = doc({ image: { how: 'none', why: 'the tab could not be shared' } })
    expect(asksScreenshot(none)).toBe(false)
    expect(captureRowShot(none, { src: 'blob:y' })).toEqual({ kind: 'none', why: 'the tab could not be shared' })
    expect(captureShotWords(captureRowShot(none, undefined))).toBe('No screenshot: the tab could not be shared')
    expect(asksScreenshot(doc({ image: { how: 'tab' } }))).toBe(false)
    expect(asksScreenshot(undefined)).toBe(false)
  })

  it('say the page, the element’s name, what a click reaches, the note, and how many more annotations', () => {
    const w = captureRowWords(doc())
    expect(w).toEqual({ page: 'Settings', element: 'button#under.under "Save"', hit: 'a click at its centre reaches span.absolute.-inset-1', note: 'The save button is covered', cut: false, others: 0 })
    const region = captureRowWords(doc({ title: '', annotations: [{ k: 1, kind: 'region', note: '', rect: { x: 0, y: 0, width: 1, height: 1 } }, { k: 2, kind: 'region', note: '', rect: { x: 0, y: 0, width: 1, height: 1 } }] }))
    expect(region).toMatchObject({ page: 'http://127.0.0.1:5000/settings', element: 'a region of the page', hit: '', others: 1 })
    expect(region.page).not.toContain('SECRET')
  })

  it('cut a long note on a word, within its bound', () => {
    const long = `${'word '.repeat(ROW_NOTE_CHARS)}end`
    const w = captureRowWords(doc({ annotations: [{ ...doc().annotations[0]!, note: long }] }))
    expect(w.cut).toBe(true)
    expect([...w.note].length).toBeLessThanOrEqual(ROW_NOTE_CHARS + 1)
    expect(w.note.endsWith('word…')).toBe(true)
  })
})

describe('the card’s source', () => {
  const card = readFileSync(new URL('../components/DecisionCard.vue', import.meta.url), 'utf8')
  const rows = readFileSync(new URL('../components/DecisionCaptures.vue', import.meta.url), 'utf8')

  it('hands the reader from the page’s `captures` alone, and asks no capture route of its own', () => {
    expect(card).toContain(`:reader="captures ? 'operator' : 'refused'"`)
    expect(card).not.toMatch(/\/api\/(captures|decisions\/[^`'"]*\/captures)/)
  })

  it('asks each capture route only through what the reader may read', () => {
    expect(rows).toMatch(/if \(!reads\.value\.sends\) return/)
    expect(rows).toMatch(/reads\.value\.documents/)
    expect(rows).toMatch(/reads\.value\.screenshots && asksScreenshot/)
    expect(rows).toMatch(/if \(!reads\.value\.capture\) return/)
    // An image is drawn from an object URL alone, never from a route's address.
    expect(rows).not.toMatch(/:src="[^"]*\/api\//)
  })
})
