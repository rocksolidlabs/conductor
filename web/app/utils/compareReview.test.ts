import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { candidateSelection, compareRunOf, selectedReview, selectionReview } from './compareSelect'
import type { Comparison } from './compare'
import { firstReviewCandidate, pickerLabel, reviewDefault } from './review'

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
    expect(view).toContain('reviewDefault(props.candidates.runs.value, compareRun.value ? true : compareOf.value.known ? false : undefined, selected.value)')
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
