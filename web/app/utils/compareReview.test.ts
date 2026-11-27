import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { candidateSelection, compareRunOf, heldVerdict, reviewHold, selectedReview, selectionReview, selectionWaitsWords, stopSelectionWords } from './compareSelect'
import type { Comparison } from './compare'
import { firstReviewCandidate, latestOfSelected, pickerLabel, reviewDefault } from './review'

// Review the selected candidate (Program item CP1, docs/plans/compare-crew.md, PR-8b): in RV1's view the picker labels a
// comparison's candidates "selected" and "not selected" and opens on the selected one; every other run's picker is as RV1 made it.

const RUN = 'crew-0000aaaa-11112222'
const A = '1f3a5c7e9b2d4f6a8c0e1f3a5c7e9b2d4f6a8c0e'
const B = '4b825dc642cb6eb9a060e54bf8d69288fbee4904'
const R = '9'.repeat(40)
const receipt = (at: string) => [{ startedAt: at }]
/** The ticket's runs as the Review view reads them: approach-a's result the newest, approach-b's selected, then its repair newer still. */
const runs = [
  {
    run: RUN,
    label: 'compare',
    startedAt: '2026-11-16T09:00:00Z',
    tasks: [
      { task: 'approach-a', kind: 'work', candidates: [{ attempt: 1, commit: A, state: 'succeeded', standing: true, withdrawn: false, receipts: receipt('2026-11-16T09:30:00Z') }] },
      { task: 'approach-b', kind: 'work', candidates: [{ attempt: 2, commit: B, state: 'succeeded', standing: true, withdrawn: false, receipts: receipt('2026-11-16T09:20:00Z') }] },
    ],
  },
]
const comparison = (sel: Comparison['selection']): Pick<Comparison, 'candidates' | 'selection'> => ({
  candidates: [{ task: 'approach-a' }, { task: 'approach-b' }] as Comparison['candidates'],
  selection: sel,
})
const applied = comparison({ decision: 'dr-9', state: 'applied', options: [], applied: { how: 'selected', selected: { task: 'approach-b', run: RUN, attempt: 2, commit: B }, words: 'approach-b selected at 4b825dc, by Nate' } })

describe("the Review view's picker on a comparison", () => {
  it('labels each candidate selected or not selected once the selection is applied, after its result', () => {
    const b = { value: `${RUN}/approach-b/2@${B}`, label: 'approach-b · attempt 2', standing: true, selection: candidateSelection(applied, { task: 'approach-b', attempt: 2, commit: B }) }
    const a = { value: `${RUN}/approach-a/1@${A}`, label: 'approach-a · attempt 1', standing: true, selection: candidateSelection(applied, { task: 'approach-a', attempt: 1, commit: A }) }
    expect(pickerLabel(b)).toBe('approach-b · attempt 2 · result · selected')
    expect(pickerLabel(a)).toBe('approach-a · attempt 1 · result · not selected')
    // The repair of the selected candidate, a later attempt of its task: neither, as the comparison still shows the selected commit.
    expect(pickerLabel({ value: `${RUN}/approach-b/3@${R}`, label: 'approach-b · attempt 3', standing: true, selection: candidateSelection(applied, { task: 'approach-b', attempt: 3, commit: R }) })).toBe('approach-b · attempt 3 · result')
  })

  it('opens on the selected candidate, though another is newer, and on none while the comparison is read', () => {
    expect(firstReviewCandidate(runs)).toBe(`${RUN}/approach-a/1@${A}`)
    expect(reviewDefault(runs, true, { run: RUN, task: 'approach-b', attempt: 2, commit: B })).toBe(`${RUN}/approach-b/2@${B}`)
    expect(reviewDefault(runs, true, undefined)).toBe('')
    // No selection applied, or None of these: the view's own first.
    expect(reviewDefault(runs, true, null)).toBe(firstReviewCandidate(runs))
    expect(reviewDefault(runs, true, 'none')).toBe(firstReviewCandidate(runs))
  })

  it("leaves every other run's picker as RV1 made it: no label, the newest standing result first", () => {
    expect(pickerLabel({ value: 'x', label: 'build · attempt 1', standing: true })).toBe('build · attempt 1 · result')
    expect(pickerLabel({ value: 'x', label: 'build · attempt 2', standing: false })).toBe('build · attempt 2')
    expect(reviewDefault(runs, false, { run: RUN, task: 'approach-b', attempt: 2, commit: B })).toBe(firstReviewCandidate(runs))
  })
})

describe("Review the selected candidate on the ticket's Decisions", () => {
  const record = { origin: 'compare' as const, state: 'applied' as const, applied: { at: '', by: '', run: RUN, response: 1, validated: true, how: 'selected' as const, selected: { task: 'approach-b', run: RUN, attempt: 2, commit: B }, words: '' } }
  it('opens the Review view at the applied candidate, and is offered for nothing else', () => {
    expect(selectedReview(record)).toEqual({ query: { view: 'review', candidate: `${RUN}/approach-b/2@${B}` } })
    expect(selectedReview({ ...record, applied: { ...record.applied, selected: undefined, none: true } })).toBeNull()
    expect(selectedReview({ ...record, state: 'answered' as never })).toBeNull()
    expect(selectedReview({ ...record, origin: 'agent' })).toBeNull()
  })

  it("is offered on the comparison's own selection once applied with a candidate, and not before or after None of these", () => {
    expect(selectionReview(applied.selection)).toEqual({ query: { view: 'review', candidate: `${RUN}/approach-b/2@${B}` } })
    expect(selectionReview({ ...applied.selection!, state: 'answered' })).toBeNull()
    expect(selectionReview({ ...applied.selection!, applied: { how: 'selected', none: true } })).toBeNull()
    expect(selectionReview(null)).toBeNull()
  })

  it('is drawn on the record row and the picker reads its labels', () => {
    const decisions = readFileSync(new URL('../components/DispatchDecisions.vue', import.meta.url), 'utf8')
    expect(decisions).toMatch(/v-if="reviewOf\(r\)"[^>]*:data-ticket-decision-review="r\.id"/)
    const picker = readFileSync(new URL('../components/ReviewCandidate.vue', import.meta.url), 'utf8')
    expect(picker).toContain('label: pickerLabel(o)')
    const view = readFileSync(new URL('../components/DispatchReview.vue', import.meta.url), 'utf8')
    expect(view).toContain('reviewDefault(props.candidates.runs.value, compareRun.value ? true : compareOf.value.known ? false : undefined, selected.value, props.continues)')
  })
})

describe("the comparison's run, the live store's or a record's", () => {
  const compare = { candidates: [{ task: 'approach-a' }, { task: 'approach-b' }] }
  const PLAIN = 'crew-0000bbbb-33334444'
  it('is found from a record the live store leaves out, as from a run it holds', () => {
    // A record after a restart: only its own read gives it, with its compare part.
    const info = (id: string) => (id === RUN ? { id, record: true, compare } : id === PLAIN ? { id } : undefined)
    expect(compareRunOf([PLAIN, RUN], undefined, info)).toEqual({ run: RUN, known: true })
    expect(compareRunOf([PLAIN], undefined, info)).toEqual({ run: '', known: true })
    // The route's candidate's run first, even when it is no run the ticket's candidates list.
    expect(compareRunOf([PLAIN], RUN, info)).toEqual({ run: RUN, known: true })
  })

  it('is not known while a run has neither a place in the live store nor a read that answered, and the view then waits', () => {
    const info = (id: string) => (id === PLAIN ? { id } : undefined)
    expect(compareRunOf([PLAIN, RUN], undefined, info)).toEqual({ run: '', known: false })
    expect(reviewDefault(runs, undefined, null)).toBe('')
    // Known and no comparison: the view's own first, as RV1 made it.
    expect(reviewDefault(runs, false, null)).toBe(firstReviewCandidate(runs))
  })

  it("is the view's and the run card's: the card tells the candidates list, the view reads a run the store does not hold", () => {
    const card = readFileSync(new URL('../components/DispatchRunCard.vue', import.meta.url), 'utf8')
    expect(card).toContain(':compare="!!(runCompare(run) || runCompare(summary))"')
    const list = readFileSync(new URL('../components/DispatchCandidates.vue', import.meta.url), 'utf8')
    expect(list).toContain('props.compare || runCompare(attention.runOf(props.runId))')
    const view = readFileSync(new URL('../components/DispatchReview.vue', import.meta.url), 'utf8')
    expect(view).toContain('attention.refreshRun(id)')
    expect(view).toContain('compareOf.value.known ? false : undefined')
  })
})

describe("the Review view after the selected candidate's repair", () => {
  const NEXT = 'crew-0000aaaa-33334444'
  const OTHER = 'crew-0000cccc-55556666'
  const at = (run: string, task: string, attempt: number, commit: string, t: string, o: { withdrawn?: boolean } = {}) => ({
    run,
    label: run,
    startedAt: '2026-11-16T09:00:00Z',
    tasks: [{ task, kind: 'work', candidates: [{ attempt, commit, state: 'succeeded', standing: true, withdrawn: !!o.withdrawn, receipts: receipt(t) }] }],
  })
  const sel = { run: RUN, task: 'approach-b', attempt: 2, commit: B }
  const chain = new Map([[NEXT, RUN]])

  it("opens on the selected lane's latest attempt: its repair in the run that continues it, once it has submitted", () => {
    const withRepair = [...runs, at(NEXT, 'approach-b', 3, R, '2026-11-16T10:00:00Z')]
    expect(reviewDefault(withRepair, true, sel, chain)).toBe(`${NEXT}/approach-b/3@${R}`)
    expect(latestOfSelected(withRepair, sel, chain)).toBe(`${NEXT}/approach-b/3@${R}`)
    // A later attempt in the selected candidate's own run is one too.
    const sameRun = [{ ...runs[0]!, tasks: [runs[0]!.tasks[0]!, { ...runs[0]!.tasks[1]!, candidates: [...runs[0]!.tasks[1]!.candidates, { attempt: 3, commit: R, state: 'succeeded', standing: true, withdrawn: false, receipts: receipt('2026-11-16T10:00:00Z') }] }] }]
    expect(reviewDefault(sameRun, true, sel)).toBe(`${RUN}/approach-b/3@${R}`)
  })

  it('opens on the selected candidate itself before a repair, and never on another lane, another chain or a withdrawn attempt', () => {
    expect(reviewDefault(runs, true, sel, chain)).toBe(`${RUN}/approach-b/2@${B}`)
    // A newer approach-a, a newer approach-b of another comparison (a chain of its own), a withdrawn repair: none of them.
    const others = [...runs, at(NEXT, 'approach-a', 3, R, '2026-11-16T10:00:00Z'), at(OTHER, 'approach-b', 1, R, '2026-11-16T11:00:00Z'), at(NEXT, 'approach-b', 4, A, '2026-11-16T12:00:00Z', { withdrawn: true })]
    expect(reviewDefault(others, true, sel, chain)).toBe(`${RUN}/approach-b/2@${B}`)
    // Not among the runs read: the applied selection's own candidate.
    expect(reviewDefault([], true, sel, chain)).toBe(`${RUN}/approach-b/2@${B}`)
  })
})

describe('Request changes on a candidate of a comparison, held before anything is written', () => {
  const cmp = (sel: Comparison['selection'], state?: string) => ({ ...comparison(sel), state })
  it("is held on a lane not selected, in the server's words", () => {
    expect(reviewHold(applied, 'approach-a')).toEqual({ title: 'approach-a was not selected', words: 'approach-a was not selected in dr-9: run a new trial to try it again' })
  })

  it('is free on the selected lane, its repair among them, and on a task that is no lane (a review), and while nothing is read', () => {
    expect(reviewHold(applied, 'approach-b')).toBeNull()
    expect(reviewHold(applied, 'review-b')).toBeNull()
    expect(reviewHold(null, 'approach-a')).toBeNull()
  })

  it('is held after None of these, and before the selection is applied', () => {
    const none = cmp({ decision: 'dr-9', state: 'applied', options: [], applied: { how: 'selected', none: true } })
    expect(reviewHold(none, 'approach-b')).toEqual({ title: 'No candidate was selected', words: 'no candidate was selected: run a new trial' })
    expect(reviewHold(cmp(null, 'ended'), 'approach-b')!.words).toBe('no candidate was selected: run a new trial')
    for (const s of [cmp({ decision: 'dr-9', state: 'open', options: [] }), cmp(null, 'running')])
      expect(reviewHold(s, 'approach-b')).toEqual({ title: 'The selection is not applied yet', words: 'select a candidate first: a repair before the selection would make the candidates unlike' })
  })

  it('says a selection answered and not applied yet as that, never asking the person who answered it to select', () => {
    const answered = reviewHold(cmp({ decision: 'dr-9', state: 'answered', options: [] }, 'answered'), 'approach-a')!
    expect(answered).toEqual({
      title: 'The selection is answered, not applied yet',
      words: 'dr-9 is answered and not applied yet: Yardmaster applies it next, or when the run is continued if it is stopped; a repair waits for it',
    })
    expect(answered.words).not.toContain('select a candidate first')
  })

  it('is said in the draft, above its notes, and holds the Request changes verdict and Send review', () => {
    const draft = readFileSync(new URL('../components/ReviewDraft.vue', import.meta.url), 'utf8')
    expect(draft).toContain("disabled: !!props.hold")
    expect(draft.indexOf('data-review-hold>')).toBeLessThan(draft.indexOf('<ol v-if="notes.length"'))
    const view = readFileSync(new URL('../components/DispatchReview.vue', import.meta.url), 'utf8')
    expect(view).toContain(':hold="hold"')
    expect(view).toContain("if (hold.value && toSend.value.verdict === 'request_changes') p.unshift(")
  })
})

describe("the run's Stop, with a comparison's selection waiting on it", () => {
  it('says the selection stays, open or answered, and is applied when the run is continued', () => {
    expect(stopSelectionWords({ id: 'dr-2', state: 'open' })).toBe('Its selection, dr-2, stays open: you can answer it on the stopped run, and the answer is applied when the run is continued. Stopping selects nothing and cancels nothing.')
    expect(stopSelectionWords({ id: 'dr-2', state: 'answered' })).toBe('Its selection, dr-2, is answered and not applied yet: the answer is applied when the run is continued. Stopping changes nothing of it.')
    expect(stopSelectionWords({ id: 'dr-2', state: 'applied' })).toBe('')
    expect(stopSelectionWords(null)).toBe('')
  })
})

describe("the comparison's top line while its selection waits", () => {
  it('says a selection waits only while it is open, and names it', () => {
    expect(selectionWaitsWords({ decision: 'dr-9', state: 'open' })).toBe('A selection waits for your answer (dr-9): it is at the end, after what it is answered from.')
    for (const state of ['answered', 'applied', 'superseded']) expect(selectionWaitsWords({ decision: 'dr-9', state })).toBe('')
    expect(selectionWaitsWords(null)).toBe('')
  })
})

describe('the verdict of a draft while Request changes is held', () => {
  const hold = { title: 'approach-a was not selected', words: 'approach-a was not selected in dr-9: run a new trial to try it again' }
  it('is Comment in place of Request changes, so the verdict checked can be sent; any other verdict stays', () => {
    expect(heldVerdict('request_changes', hold)).toBe('comment')
    expect(heldVerdict('approve', hold)).toBe('approve')
    expect(heldVerdict('comment', hold)).toBe('comment')
    expect(heldVerdict('request_changes', null)).toBe('request_changes')
  })

  it("is set by the view while the hold stands, and the hold's words keep the candidate's name as written", () => {
    const view = readFileSync(new URL('../components/DispatchReview.vue', import.meta.url), 'utf8')
    expect(view).toContain('const want = heldVerdict(v, h)')
    const draft = readFileSync(new URL('../components/ReviewDraft.vue', import.meta.url), 'utf8')
    expect(draft).toContain('data-review-hold-words>{{ hold.words }}.</span>')
  })
})
