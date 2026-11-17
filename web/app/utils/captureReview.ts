import { parseCandidate } from './dispatch'
import { SEVERITIES, type DraftNote, type Severity } from './review'

/**
 * A capture sent to a review (Program item PV1, docs/plans/browser-evidence.md, "Sending", PR-10): Request a change (RV1's Request
 * changes of the candidate the capture shows, one capture finding per annotation, applied by Yardmaster as a repair) and Add to a review
 * (the same findings put in the person's review draft of that candidate). Both are offered on a capture of a candidate's preview alone
 * (Decision 9): a capture of a worktree still being worked goes to a ticket. The finding's words are the server's, made from the
 * annotation; the draft shows the note as the person wrote it. Pure.
 */

/** What the send to a review reads of a capture (GET /api/captures/{id}): its revision, where it was taken, its annotations. */
export interface CaptureForReview {
  id: string
  rev: number
  origin: { kind: string; candidate?: string }
  image?: { sha256?: string }
  annotations: Array<{ k?: number; kind: string; note: string; element?: { tag?: string; name?: string; selector?: string } | null }>
}

/** The `to` of a send to a review (POST /api/captures/{id}/send, kind review). */
export interface ReviewSendTo {
  kind: 'review'
  candidate: string
  ks?: number[]
  draft?: true
  verdict?: 'request_changes'
  severity?: Severity
  summary?: string
  continueRun?: true
}

/** What the person chose in the form: Add to a review or Request a change, the annotations, a change's severity and a summary. */
export interface ReviewSendForm {
  draft: boolean
  ks: number[]
  severity: Severity
  summary: string
}

/** Whether a send to a review is offered for the capture, and why not in words. */
export function reviewOffered(c: Pick<CaptureForReview, 'origin' | 'annotations'>): { offered: true } | { offered: false; why: string } {
  if (c.origin.kind !== 'candidate' || !c.origin.candidate || !parseCandidate(c.origin.candidate)) {
    return { offered: false, why: "A review asks changes of a candidate: this capture is of a worktree still being worked, so send it to a ticket instead." }
  }
  if (!c.annotations.length) return { offered: false, why: 'The capture has no annotation to make a finding of.' }
  return { offered: true }
}

/** The annotations' places, from 1. */
export function annotationKs(c: Pick<CaptureForReview, 'annotations'>): number[] {
  return c.annotations.map((a, i) => a.k || i + 1)
}

/** The send's `to` for the form: every annotation when all are chosen (none named), the severity major left to the server's default. */
export function reviewSendTo(c: CaptureForReview, f: ReviewSendForm, continueRun = false): ReviewSendTo {
  const all = annotationKs(c)
  const ks = [...new Set(f.ks)].filter((k) => all.includes(k)).sort((a, b) => a - b)
  const to: ReviewSendTo = { kind: 'review', candidate: c.origin.candidate ?? '' }
  if (ks.length && ks.length < all.length) to.ks = ks
  if (f.draft) {
    to.draft = true
    return to
  }
  to.verdict = 'request_changes'
  if (f.severity !== 'major' && SEVERITIES.includes(f.severity)) to.severity = f.severity
  const s = f.summary.trim()
  if (s) to.summary = s
  if (continueRun) to.continueRun = true
  return to
}

/** What stops the form from being sent, in words: a capture not of a candidate, no annotation chosen, a summary past its bound. */
export function reviewSendProblems(c: CaptureForReview, f: ReviewSendForm): string[] {
  const out: string[] = []
  const o = reviewOffered(c)
  if (!o.offered) out.push(o.why)
  if (!f.ks.length) out.push('Choose at least one annotation to make a finding of.')
  if (!f.draft && [...f.summary].length > 4000) out.push('The summary is more than 4000 characters.')
  return out
}

/** The draft's notes Add to a review puts in, one per annotation chosen, naming the send: their words the annotation's note. */
export function captureNotes(c: CaptureForReview, send: string, ks: readonly number[], severity: Severity = 'major'): Omit<DraftNote, 'key'>[] {
  const all = annotationKs(c)
  const chosen = ks.length ? ks : all
  return all
    .map((k, i) => ({ k, a: c.annotations[i]! }))
    .filter(({ k }) => chosen.includes(k))
    .map(({ k, a }) => ({
      kind: 'capture' as const,
      intent: 'change' as const,
      severity,
      message: a.note,
      capture: { capture: c.id, send, k },
      captureName: `capture ${c.id.slice(0, 8)} r${c.rev}, annotation ${k}${a.element?.tag ? ` (${a.element.tag}${a.element.name ? ` “${a.element.name}”` : ''})` : ''}`,
      captureRev: c.rev,
      captureFiles: c.image?.sha256 ? 2 : 1,
    }))
}

/** A send's state, as a send to a review answers it. */
export interface ReviewSendAnswer {
  send: { id: string; state: 'pending' | 'sent' | 'refused' | 'cancelled'; target?: string; why?: string }
  key?: string
  review?: { id: string; application?: { state: string; why?: string; attempt?: number; run?: string } }
}

/** What became of the send, in words: the review it made and its application, the draft it went to, why it was refused, or pending. */
export function reviewSendWords(a: ReviewSendAnswer, draft: boolean): { title: string; detail: string; tone: 'neutral' | 'error' } {
  const s = a.send
  if (s.state === 'refused') return { title: 'Not sent', detail: s.why || 'The review was refused.', tone: 'error' }
  if (s.state === 'pending') return { title: 'Waiting for its review', detail: 'The send is kept; the server makes its review and says so here.', tone: 'neutral' }
  if (draft) return { title: 'Added to your review draft', detail: `Open the review of ${a.key || 'the ticket'} to send it with the rest of the draft.`, tone: 'neutral' }
  const app = a.review?.application
  switch (app?.state) {
    case 'applied':
      return { title: 'Changes requested', detail: `Yardmaster repairs the task: attempt ${app.attempt} has the capture's document and screenshot.`, tone: 'neutral' }
    case 'refused':
      return { title: 'Recorded, refused by Yardmaster', detail: app.why || 'Nothing was changed.', tone: 'error' }
    default:
      return { title: 'Recorded', detail: 'Yardmaster took the request; the review says when the repair is made.', tone: 'neutral' }
  }
}
