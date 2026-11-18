import type { CaptureDocument, CaptureSendAnswer } from './capture'
import { CAPTURE_ID, elementSummary, hitWords } from './capture'
import type { DecisionEntry } from './decisions'

/**
 * The captures sent to a decision, as the decision's card draws them where it is answered (PV1 PR-11, docs/plans/browser-evidence.md;
 * the card is DecisionCard, the rows DecisionCaptures). Who reads what is the server's rule, and the card asks nothing it would refuse:
 *
 * - The operator (the workbench token) may read every capture route: the sends to the decision (`GET /api/decisions/{did}/captures`),
 *   each send's frozen document (`GET /api/captures/{id}/sends/{send}`), the capture's screenshot (`GET /api/captures/{id}/image`) and
 *   the capture as it is now (`GET /api/captures/{id}`). Its card draws each capture's screenshot small, its note, the element's name
 *   and Open, which shows the capture whole.
 * - A decision link's holder and a decide grant's principal are refused every capture route. A link's holder is given the decision's
 *   history over the lane, whose comments name each capture sent (the server's own words, `capture:<id>/<send>`): its card lists those
 *   references as words and asks nothing. A decide grant's principal is given no history at all (the decide view): its card (DecideCard)
 *   draws nothing of the captures.
 *
 * Pure: DecisionCaptures draws it and vitest proves it.
 */

/** Who reads the card: the operator, or a reader every capture route refuses (a decision link's holder). */
export type CaptureReader = 'operator' | 'refused'

/** The most captures a card reads whole (its frozen document and its screenshot): the newest; the others are counted. */
export const MAX_CARD_CAPTURES = 8

/** The note a row shows, at most, in characters (the whole note is in Open). */
export const ROW_NOTE_CHARS = 280

/** A reference to a capture's send, as the decision's history names it: the capture, the send, and the revision sent. */
export interface CaptureRef {
  capture: string
  send: string
  rev: number
  by?: string
  at: string
}

/** The routes a card may ask for the captures, for its reader: the operator's are every capture route's; a refused reader asks none. */
export function captureReads(reader: CaptureReader): { sends: boolean; documents: boolean; screenshots: boolean; capture: boolean } {
  const may = reader === 'operator'
  return { sends: may, documents: may, screenshots: may, capture: may }
}

/** A send's id and a capture's id: 16 lower-case hex characters. */
const ID = CAPTURE_ID

/**
 * The comment a send to a decision makes, in the server's words (internal/api/capture_decision.go, decisionRequestOf and
 * decisionComment): "Capture <id> is attached at revision <rev>: what was sent is capture:<id>/<send>." Nothing else is taken.
 */
const SENT = /^Capture ([0-9a-f]{16}) is attached at revision ([1-9][0-9]{0,8}): what was sent is capture:([0-9a-f]{16})\/([0-9a-f]{16})\.$/

/**
 * The captures the decision's history says were sent to it, newest first, one per send: each comment in the server's own words whose
 * two capture ids agree. What a reader the capture routes refuse is already given, and nothing more.
 */
export function historyCaptureRefs(history: readonly DecisionEntry[] | undefined): CaptureRef[] {
  const out: CaptureRef[] = []
  const seen = new Set<string>()
  for (const h of history ?? []) {
    if (h.kind !== 'commented') continue
    const text = typeof h.detail?.text === 'string' ? h.detail.text : ''
    const m = SENT.exec(text)
    if (!m || m[1] !== m[3] || !ID.test(m[4]!) || seen.has(m[4]!)) continue
    seen.add(m[4]!)
    out.push({ capture: m[1]!, send: m[4]!, rev: Number(m[2]), ...(h.by ? { by: h.by } : {}), at: h.at })
  }
  return out.reverse()
}

/** The sends the card lists for the operator: those sent, newest first, the first MAX_CARD_CAPTURES read whole and the rest counted. */
export function cardSends(sends: readonly CaptureSendAnswer[]): { shown: CaptureSendAnswer[]; more: number } {
  const sent = sends.filter((s) => s.state === 'sent').sort((a, b) => b.createdAt.localeCompare(a.createdAt))
  return { shown: sent.slice(0, MAX_CARD_CAPTURES), more: Math.max(0, sent.length - MAX_CARD_CAPTURES) }
}

/** Whether the card asks the capture's screenshot for a send's frozen document: only when the capture was kept with one. */
export function asksScreenshot(doc: Pick<CaptureDocument, 'image'> | undefined): boolean {
  return !!doc && doc.image.how !== 'none' && !!doc.image.sha256
}

/** A screenshot as the card holds it: being read, shown (its object URL), never taken (why), or not read (the route refused it). */
export type CaptureShot = { kind: 'reading' } | { kind: 'shown'; src: string } | { kind: 'none'; why: string } | { kind: 'unread' }

/**
 * The screenshot a row draws, from the send's frozen document and what the screenshot's read gave: an image only once its bytes are
 * read (never an address the browser would ask without the bearer header), else the words why there is none. A document that could
 * not be read is given as `read` failed.
 */
export function captureRowShot(doc: Pick<CaptureDocument, 'image'> | undefined, read: { src?: string; failed?: boolean } | undefined): CaptureShot {
  if (!doc) return read?.failed ? { kind: 'unread' } : { kind: 'reading' }
  if (!asksScreenshot(doc)) return { kind: 'none', why: doc.image.why || 'no screenshot was kept' }
  if (read?.src) return { kind: 'shown', src: read.src }
  if (read?.failed) return { kind: 'unread' }
  return { kind: 'reading' }
}

/** The words for a screenshot that is not drawn. */
export function captureShotWords(s: CaptureShot): string {
  switch (s.kind) {
    case 'none':
      return `No screenshot: ${s.why}`
    case 'unread':
      return 'The screenshot could not be read: the capture may have been deleted since'
    case 'reading':
      return 'Reading the screenshot…'
    default:
      return ''
  }
}

/** What a row says of a capture as it was sent: the page, the first annotation's element and hit test, its note, and how many more. */
export interface CaptureRowWords {
  page: string
  element: string
  hit: string
  note: string
  cut: boolean
  others: number
}

/** A row's words from the send's frozen document: the first annotation's, its note cut at ROW_NOTE_CHARS on a word. */
export function captureRowWords(doc: Pick<CaptureDocument, 'title' | 'url' | 'annotations'>): CaptureRowWords {
  const a = doc.annotations[0]
  const el = a?.element
  const note = (a?.note ?? '').trim()
  const chars = [...note]
  let shown = note
  let cut = false
  if (chars.length > ROW_NOTE_CHARS) {
    const head = chars.slice(0, ROW_NOTE_CHARS).join('')
    const space = head.lastIndexOf(' ')
    shown = `${(space > ROW_NOTE_CHARS / 2 ? head.slice(0, space) : head).trimEnd()}…`
    cut = true
  }
  return {
    page: doc.title || capturePageAddress(doc.url),
    element: el ? elementSummary(el) : a ? 'a region of the page' : '',
    hit: el ? hitWords(el) : '',
    note: shown,
    cut,
    others: Math.max(0, doc.annotations.length - 1),
  }
}

/** A page's address without its query and fragment (where a tree's secret may be put in). */
export function capturePageAddress(url: string): string {
  try {
    const u = new URL(url)
    return u.origin + u.pathname
  } catch {
    return ''
  }
}

/** A reference's words for a reader the capture routes refuse: the capture, its revision, who sent it. */
export function captureRefWords(r: Pick<CaptureRef, 'rev' | 'by'>): string {
  return `revision ${r.rev}${r.by ? ` · sent by ${r.by}` : ''}`
}

/** What a refused reader is told under the references, once. */
export const REFUSED_WORDS = "A capture's screenshot and notes open on the operator's workbench."
