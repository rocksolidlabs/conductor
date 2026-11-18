import { describe, expect, it } from 'vitest'
import { annotationKs, captureNotes, retakeCandidate, reviewOffered, reviewSendProblems, reviewSendTo, reviewSendWords, type CaptureForReview } from './captureReview'
import { batchProblems, changeRequestOf, evidenceFiles, findingOf, noteWhere, repairBytes, reviewBody, MAX_REPAIR_PAYLOAD, type DraftNote } from './review'

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
        captureRev: 2,
        captureFiles: 1,
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

describe('the evidence files a draft hands a repair', () => {
  const note = (capture: string, send: string, rev: number, files: number, k = 1): DraftNote => ({
    key: `${capture}${send}${k}`,
    kind: 'capture',
    intent: 'change',
    severity: 'major',
    message: '',
    capture: { capture, send, k },
    captureRev: rev,
    captureFiles: files,
  })
  const id = (n: number) => n.toString(16).padStart(16, '0')

  it("counts one capture revision's files once, whichever sends carried its notes", () => {
    const notes = [note(id(1), id(10), 1, 2), note(id(1), id(11), 1, 2, 2), note(id(1), id(12), 2, 1)]
    expect(evidenceFiles(reviewBody(CAND, 'b'.repeat(40), 'request_changes', '', notes), notes)).toBe(3)
  })

  it('says, before the send, a request past the repair\'s eight files', () => {
    const notes = [1, 2, 3, 4, 5].map((n) => note(id(n), id(100 + n), 1, 2))
    const body = reviewBody(CAND, 'b'.repeat(40), 'request_changes', '', notes)
    expect(batchProblems(body, 'Nate', notes).some((p) => p.includes('10 files') && p.includes('more than 8'))).toBe(true)
    expect(batchProblems({ ...body, verdict: 'comment' }, 'Nate', notes)).toEqual([])
    expect(batchProblems(reviewBody(CAND, 'b'.repeat(40), 'request_changes', '', notes.slice(0, 4)), 'Nate', notes.slice(0, 4))).toEqual([])
  })
})

describe('the candidate Capture again opens', () => {
  const cand = (attempt: number, standing: boolean, commit = 'c'.repeat(40)) => ({ attempt, commit, standing, kind: 'work' })
  it("is the newest run's standing candidate of the capture's task, never the capture's own attempt nor an earlier one", () => {
    const runs = [
      { run: 'b', candidates: [{ task: 'docs', kind: 'work', candidates: [cand(1, true)] }, { task: 'build', kind: 'work', candidates: [cand(3, false), cand(2, true)] }] },
      { run: 'a', candidates: [{ task: 'build', kind: 'work', candidates: [cand(1, true)] }] },
    ]
    expect(retakeCandidate({ run: 'a', task: 'build', attempt: 1 }, runs)).toEqual({ run: 'b', task: 'build', attempt: 2, commit: 'c'.repeat(40) })
    expect(retakeCandidate({ run: 'a', task: 'build', attempt: 1 }, runs.slice(1))).toBeNull()
    // In its own run, only a later attempt than the capture's: attempt 1, still the task's result, is older than a capture of attempt 2.
    expect(retakeCandidate({ run: 'a', task: 'build', attempt: 2 }, runs.slice(1))).toBeNull()
    const later = [{ run: 'a', candidates: [{ task: 'build', kind: 'work', candidates: [cand(3, true)] }] }]
    expect(retakeCandidate({ run: 'a', task: 'build', attempt: 2 }, later)).toEqual({ run: 'a', task: 'build', attempt: 3, commit: 'c'.repeat(40) })
    expect(retakeCandidate({}, runs)).toBeNull()
  })

  it("is a repair run's candidate whatever its crew's task is called, the capture's own task's first when the run has one", () => {
    // The capture was taken in a run of one crew (task look); the ticket's repair is another crew's run (task fix).
    const repair = { run: 'r', startedAt: '2026-11-18T06:00:00Z', candidates: [{ task: 'fix', kind: 'work', candidates: [cand(1, true)] }] }
    const look = { run: 'a', startedAt: '2026-11-18T05:00:00Z', candidates: [{ task: 'look', kind: 'work', candidates: [cand(1, true)] }] }
    expect(retakeCandidate({ run: 'a', task: 'look', attempt: 1 }, [repair])).toEqual({ run: 'r', task: 'fix', attempt: 1, commit: 'c'.repeat(40) })
    expect(retakeCandidate({ run: 'a', task: 'look', attempt: 1 }, [repair, look])).toEqual({ run: 'r', task: 'fix', attempt: 1, commit: 'c'.repeat(40) })
    // Of several tasks, the capture's own name first, else the last work task with a standing candidate (a review's never).
    const tasks = (...names: string[]) => names.map((task) => ({ task, kind: task === 'review' ? 'review' : 'work', candidates: [{ ...cand(1, true), commit: task.padEnd(40, '0') }] }))
    expect(retakeCandidate({ run: 'a', task: 'look' }, [{ run: 'r', candidates: tasks('look', 'fix') }])?.task).toBe('look')
    expect(retakeCandidate({ run: 'a', task: 'look' }, [{ run: 'r', candidates: tasks('fix', 'test', 'review') }])?.task).toBe('test')
    // In the capture's own run, only its own task's other attempts: a sibling task is not what repairs it.
    expect(retakeCandidate({ run: 'a', task: 'look', attempt: 1 }, [{ run: 'a', candidates: tasks('look', 'fix') }])).toBeNull()
    // A run that started before the capture's own is no later candidate's: the retake's card does not point back at the first run.
    expect(retakeCandidate({ run: 'r', task: 'fix', attempt: 1 }, [repair, look])).toBeNull()
    // The capture's own moment decides when its own run is not among the ticket's (a capture of a run launched elsewhere): a run of the
    // ticket that started before it is never offered, one that started after it is.
    const elsewhere = { run: 'x', task: 'look', attempt: 1 }
    const at = (startedAt: string) => [{ run: 'r', startedAt, candidates: [{ task: 'fix', kind: 'work', candidates: [cand(1, true)] }] }]
    expect(retakeCandidate(elsewhere, at('2026-11-18T05:00:00Z'), '2026-11-18T06:00:00Z')).toBeNull()
    expect(retakeCandidate(elsewhere, at('2026-11-18T07:00:00Z'), '2026-11-18T06:00:00Z')).toEqual({ run: 'r', task: 'fix', attempt: 1, commit: 'c'.repeat(40) })
    // With no time known at all, every run of the ticket is offered as before.
    expect(retakeCandidate({ run: 'r', task: 'fix', attempt: 1 }, [{ ...repair, startedAt: undefined }, look])).toEqual({ run: 'a', task: 'look', attempt: 1, commit: 'c'.repeat(40) })
  })

  it("is a comparison's selected candidate alone, and none while no selection is applied or None of these was answered", () => {
    const tasks = (...names: string[]) => names.map((task) => ({ task, kind: task === 'review' ? 'review' : 'work', candidates: [{ ...cand(1, true), commit: task.padEnd(40, '0') }] }))
    const lanes = tasks('approach-a', 'approach-b', 'review')
    expect(retakeCandidate({ run: 'a', task: 'look' }, [{ run: 'r', selected: 'approach-a', candidates: lanes }])?.task).toBe('approach-a')
    expect(retakeCandidate({ run: 'a', task: 'look' }, [{ run: 'r', selected: 'approach-b', candidates: lanes }])?.task).toBe('approach-b')
    expect(retakeCandidate({ run: 'a', task: 'look' }, [{ run: 'r', selected: null, candidates: lanes }])).toBeNull()
    // A lane named as the capture's task is no exception: the selection decides.
    expect(retakeCandidate({ run: 'a', task: 'approach-b' }, [{ run: 'r', selected: 'approach-a', candidates: lanes }])?.task).toBe('approach-a')
    // The selected lane with no standing candidate in this run (a continuation's imported result) offers nothing here; an older run of
    // the chain, its comparison read the same, offers it.
    const imported = [{ task: 'approach-a', kind: 'work', candidates: [{ ...cand(1, false) }] }, ...tasks('approach-b')]
    expect(retakeCandidate({ run: 'a', task: 'look' }, [{ run: 'c', selected: 'approach-a', candidates: imported }])).toBeNull()
    expect(retakeCandidate({ run: 'a', task: 'look' }, [{ run: 'c', selected: 'approach-a', candidates: imported }, { run: 'r', selected: 'approach-a', candidates: lanes }])).toMatchObject({ run: 'r', task: 'approach-a' })
    // A run of no comparison keeps its rule beside it.
    expect(retakeCandidate({ run: 'a', task: 'look' }, [{ run: 'r', selected: null, candidates: lanes }, { run: 's', candidates: tasks('fix') }])).toMatchObject({ run: 's', task: 'fix' })
  })
})
