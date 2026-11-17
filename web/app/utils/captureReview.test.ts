import { describe, expect, it } from 'vitest'
import { annotationKs, captureNotes, reviewOffered, reviewSendProblems, reviewSendTo, reviewSendWords, type CaptureForReview } from './captureReview'
import { batchProblems, changeRequestOf, findingOf, noteWhere, repairBytes, reviewBody, MAX_REPAIR_PAYLOAD, type DraftNote } from './review'

// A capture sent to a review (PR-10): offered on a candidate's capture alone, its body as the send route takes it, the draft's notes
// Add to a review makes, and the review's batch with capture findings, whose words are the server's.

const CAND = `crew-0123abcd/build/1@${'a'.repeat(40)}`
const capture: CaptureForReview = {
  id: '0123456789abcdef',
  rev: 2,
  origin: { kind: 'candidate', candidate: CAND },
  annotations: [
    { k: 1, kind: 'element', note: 'Save is covered.', element: { tag: 'button', name: 'Save', selector: 'button.save' } },
    { k: 2, kind: 'region', note: '' },
  ],
}
const SEND = 'fedcba9876543210'

describe('a send to a review', () => {
  it("is offered on a candidate's capture with annotations, never on a worktree's", () => {
    expect(reviewOffered(capture)).toEqual({ offered: true })
    const worktree = reviewOffered({ ...capture, origin: { kind: 'attempt' } })
    expect(worktree.offered).toBe(false)
    expect(!worktree.offered && worktree.why).toContain('send it to a ticket instead')
    expect(reviewOffered({ ...capture, origin: { kind: 'candidate', candidate: 'not a candidate' } }).offered).toBe(false)
    expect(reviewOffered({ ...capture, annotations: [] }).offered).toBe(false)
  })

  it('sends Request a change with its verdict, every annotation by none named, the severity major by the server\'s default', () => {
    const form = { draft: false, ks: annotationKs(capture), severity: 'major' as const, summary: '  ' }
    expect(reviewSendTo(capture, form)).toEqual({ kind: 'review', candidate: CAND, verdict: 'request_changes' })
    expect(reviewSendTo(capture, { ...form, ks: [2], severity: 'blocker', summary: ' Fix it. ' }, true)).toEqual({
      kind: 'review',
      candidate: CAND,
      ks: [2],
      verdict: 'request_changes',
      severity: 'blocker',
      summary: 'Fix it.',
      continueRun: true,
    })
  })

  it('sends Add to a review as a draft alone: no verdict, severity, summary or word to continue', () => {
    expect(reviewSendTo(capture, { draft: true, ks: [1], severity: 'nit', summary: 'x' }, true)).toEqual({ kind: 'review', candidate: CAND, ks: [1], draft: true })
  })

  it('names what stops it: no annotation chosen, a summary past its bound, a capture not of a candidate', () => {
    expect(reviewSendProblems(capture, { draft: false, ks: [], severity: 'major', summary: 'é'.repeat(4001) })).toEqual([
      'Choose at least one annotation to make a finding of.',
      'The summary is more than 4000 characters.',
    ])
    expect(reviewSendProblems({ ...capture, origin: { kind: 'attempt' } }, { draft: true, ks: [1], severity: 'major', summary: '' })).toHaveLength(1)
  })

  it("puts one capture note per annotation chosen in the draft, naming the send, its words the person's note", () => {
    const notes = captureNotes(capture, SEND, [1])
    expect(notes).toEqual([
      {
        kind: 'capture',
        intent: 'change',
        severity: 'major',
        message: 'Save is covered.',
        capture: { capture: capture.id, send: SEND, k: 1 },
        captureName: 'capture 01234567 r2, annotation 1 (button “Save”)',
      },
    ])
    expect(captureNotes(capture, SEND, []).map((n) => n.capture?.k)).toEqual([1, 2])
  })

  it('says what became of it: the repair, the draft, a refusal, a send that waits', () => {
    expect(reviewSendWords({ send: { id: SEND, state: 'sent', target: 'r1' }, review: { id: 'r1', application: { state: 'applied', attempt: 2 } } }, false).title).toBe('Changes requested')
    expect(reviewSendWords({ send: { id: SEND, state: 'sent', target: 'draft' }, key: 'CND-1' }, true).detail).toContain('CND-1')
    expect(reviewSendWords({ send: { id: SEND, state: 'refused', why: 'the run is gone' } }, false)).toEqual({ title: 'Not sent', detail: 'the run is gone', tone: 'error' })
    expect(reviewSendWords({ send: { id: SEND, state: 'pending' } }, false).title).toBe('Waiting for its review')
  })
})

describe('a capture finding in the review', () => {
  const note: DraftNote = { key: 'n1', kind: 'capture', intent: 'change', severity: 'minor', message: 'Save is covered.', resolution: ' Move it. ', capture: { capture: capture.id, send: SEND, k: 1 } }

  it("is sent naming its capture, never its words, path or lines: the server's", () => {
    expect(findingOf({ ...note, path: 'x.vue', from: 3, to: 3, lines: ['x'] })).toEqual({
      kind: 'capture',
      intent: 'change',
      severity: 'minor',
      resolution: 'Move it.',
      capture: { capture: capture.id, send: SEND, k: 1 },
    })
  })

  it('needs no words of its own, and names its capture', () => {
    const body = reviewBody(CAND, 'b'.repeat(40), 'request_changes', '', [note])
    expect(batchProblems(body, 'Nate')).toEqual([])
    const bare = reviewBody(CAND, 'b'.repeat(40), 'request_changes', '', [{ ...note, capture: undefined }])
    expect(batchProblems(bare, 'Nate')).toEqual(['Note 1 names no capture.'])
  })

  it('is measured at its widest words and path, with its send\'s two files as evidence', () => {
    const body = reviewBody(CAND, 'b'.repeat(40), 'request_changes', '', [note, { ...note, key: 'n2', capture: { ...note.capture!, k: 2 } }])
    const cr = changeRequestOf(body, 'Nate')
    expect(cr.changes[0]!.message).toHaveLength(1000)
    expect(cr.evidence?.map((e) => [e.send, e.name.slice(-4)])).toEqual([
      [SEND, 'json'],
      [SEND, '.png'],
    ])
    expect(repairBytes(cr)).toBeLessThan(MAX_REPAIR_PAYLOAD)
    expect(repairBytes(cr)).toBeGreaterThan(repairBytes({ ...cr, evidence: undefined }))
  })

  it('says where it is: the capture, and the source the server traced', () => {
    expect(noteWhere(note)).toBe('capture 01234567, annotation 1')
    expect(noteWhere({ ...note, captureName: 'capture 01234567 r2, annotation 1' })).toBe('capture 01234567 r2, annotation 1')
    expect(noteWhere({ kind: 'capture', path: 'web/a.vue', from: 12, capture: note.capture })).toBe('capture 01234567, annotation 1, drawn by web/a.vue:12')
  })
})
