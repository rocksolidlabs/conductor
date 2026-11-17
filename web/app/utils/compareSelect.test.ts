import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { candidateMark, cellWords, verdictWords, type CheckCell, type ComparedCandidate, type ComparedCheck, type Comparison, type ComparisonSelection } from './compare'
import { appliedSelection, brakemanWords, candidateAt, candidateSelection, claimSource, laneSelection, reviewSource, selectionAnswerable, selectionFor, verdictWord, selectionPanelWords, SELECTION_WORDS } from './compareSelect'
import { answerAsk, recommended, SELECTION_FINAL, type Decision } from './decisions'

// A comparison's selection and its labels (Program item CP1, docs/plans/compare-crew.md, PR-7): no option preselected whatever
// `supporting` says; the rationale required before Send; the finality words and Send's label; the measured and supporting panels kept
// apart, a cell's colour read only from `checks`; "selected" and "not selected" read from the applied selection alone.

const RUN = 'crew-0000aaaa-11112222'
const A = '1f3a5c7e9b2d4f6a8c0e1f3a5c7e9b2d4f6a8c0e'
const B = '4b825dc642cb6eb9a060e54bf8d69288fbee4904'

const candidate = (task: string, commit: string, attempt = 1): ComparedCandidate => ({ task, member: task, agent: 'worker-cli', args: [], approach: '', state: 'ready', run: RUN, attempt, commit, start: 'c'.repeat(40), selectable: true, attempts: attempt, acceptedAnyway: false, shared: [], equivalent: [], manifestChanged: false })
const cell = (task: string, o: Partial<CheckCell> = {}): CheckCell => ({ task, attempt: 1, k: 1, state: 'finished', exit: 0, signal: '', timedOut: false, durationMs: 1000, where: 'native', manifest: '', stable: true, receipt: `check:${RUN}/${task}/1/unit/1`, ...o })
const check = (): ComparedCheck => ({ phase: 'check', id: 'unit', argv: ['go', 'test'], argvCut: false, argvDigest: 'e'.repeat(64), timeoutSeconds: 60, cells: [cell('approach-a'), cell('approach-b', { exit: 1 })], verdict: 'comparable', why: [] })

const options: ComparisonSelection['options'] = [
  { label: 'approach-a at 1f3a5c7', subject: { kind: 'candidate', run: RUN, task: 'approach-a', n: 1, commit: A } },
  { label: 'approach-b at 4b825dc', subject: { kind: 'candidate', run: RUN, task: 'approach-b', n: 2, commit: B } },
  { label: 'None of these' },
]

function comparison(o: Partial<Comparison> = {}): Comparison {
  return { run: RUN, base: 'c'.repeat(40), objective: 'make it faster', criteria: [{ id: 'speed', text: 'faster' }], candidates: [candidate('approach-a', A), candidate('approach-b', B, 2)], checks: [check()], settled: true, ...o }
}
const applied = (selected: { task: string; attempt: number; commit: string } | null): ComparisonSelection => ({
  decision: 'dr-9',
  state: 'applied',
  options,
  chosen: selected ? 1 : 2,
  applied: selected ? { how: 'selected', selected: { ...selected, run: RUN }, words: `${selected.task} selected at ${selected.commit.slice(0, 7)}, by Nate` } : { how: 'selected', none: true, words: 'none of these, by Nate' },
})

/** Supporting information that all favours approach-a: the agents, its review and Brakeman. */
const favouringA = {
  claims: [{ task: 'approach-a', attempt: 1, summary: 'approach-a is clearly the one to select.' }],
  reviews: [{ task: 'approach-a', attempt: 1, verdict: 'approved', findings: [] }, { task: 'approach-b', attempt: 2, verdict: 'request_changes', findings: ['too large'] }],
  brakeman: { request: 'br-3', state: 'briefed', brief: 'br-3' },
}

/** The selection's record, as Yardmaster opens it. */
const record = (over: Partial<Decision> = {}): Decision => ({
  id: 'dr-9',
  run: RUN,
  openedIn: RUN,
  origin: 'compare',
  type: 'approval',
  request: { question: 'Which approach does CND-12 continue with?', options, recommendation: -1, authorizes: 'Final.' },
  policy: { approver: 'operator' },
  state: 'open',
  rev: 1,
  createdAt: '2026-11-16T10:00:00Z',
  ...over,
})

describe('the labels', () => {
  it('say nothing until a selection is applied, open or answered alike', () => {
    for (const state of ['open', 'answered', 'superseded'] as const) {
      const c = comparison({ selection: { decision: 'dr-9', state, options, chosen: 1 } })
      expect(appliedSelection(c.selection)).toBeNull()
      expect(laneSelection(c, 'approach-b')).toBeUndefined()
      expect(candidateSelection(c, { task: 'approach-b', attempt: 2, commit: B })).toBeUndefined()
    }
    expect(laneSelection(comparison(), 'approach-a')).toBeUndefined()
    // The state decides, not a part: a selection not in state applied labels nothing, whatever else it carries.
    const half = comparison({ selection: { ...applied({ task: 'approach-b', attempt: 2, commit: B }), state: 'answered' } })
    expect(laneSelection(half, 'approach-a')).toBeUndefined()
  })

  it('mark the selected lane selected and every other not selected, and every lane not selected after None of these', () => {
    const c = comparison({ selection: applied({ task: 'approach-b', attempt: 2, commit: B }) })
    expect(laneSelection(c, 'approach-b')).toBe('selected')
    expect(laneSelection(c, 'approach-a')).toBe('not-selected')
    // A task that is no lane's candidate (a review) has no label.
    expect(laneSelection(c, 'review-a')).toBeUndefined()
    const none = comparison({ selection: applied(null) })
    expect([laneSelection(none, 'approach-a'), laneSelection(none, 'approach-b')]).toEqual(['not-selected', 'not-selected'])
    expect(SELECTION_WORDS).toEqual({ selected: 'selected', 'not-selected': 'not selected' })
  })

  it('mark only the applied candidate selected: another attempt of its lane, or a repair made after it, is neither', () => {
    const c = comparison({ selection: applied({ task: 'approach-b', attempt: 2, commit: B }) })
    expect(candidateSelection(c, { task: 'approach-b', attempt: 2, commit: B })).toBe('selected')
    expect(candidateSelection(c, { task: 'approach-b', attempt: 1, commit: 'f'.repeat(40) })).toBeUndefined()
    expect(candidateSelection(c, { task: 'approach-b', attempt: 2, commit: 'f'.repeat(40) })).toBeUndefined()
    expect(candidateSelection(c, { task: 'approach-b', attempt: 3, commit: '9'.repeat(40) })).toBeUndefined()
    expect(candidateSelection(c, { task: 'approach-a', attempt: 1, commit: A })).toBe('not-selected')
  })

  it('are read from the applied selection alone, whatever the agents, the reviews and Brakeman favoured', () => {
    const c = comparison({ selection: applied({ task: 'approach-b', attempt: 2, commit: B }), supporting: favouringA })
    expect(laneSelection(c, 'approach-b')).toBe('selected')
    expect(laneSelection(c, 'approach-a')).toBe('not-selected')
    // Before the selection, what supports approach-a selects nothing.
    expect(laneSelection(comparison({ supporting: favouringA }), 'approach-a')).toBeUndefined()
  })
})

describe('nothing preselected, the rationale required, the answer final', () => {
  it('whatever `supporting` says: no option is recommended and nothing is picked before a person picks', () => {
    const d = record({ request: { ...record().request, recommendation: 0 } })
    expect(recommended(d)).toBe(-1)
    expect(answerAsk(d, undefined, '')).toMatchObject({ final: '', ready: false })
  })

  it('holds the confirm until the rationale holds text, names the candidate and says final', () => {
    expect(answerAsk(record(), 1, '')).toEqual({ confirm: 'Select approach-b (final)', final: SELECTION_FINAL, placeholder: 'Why (required: the record of this selection)…', ready: false })
    expect(answerAsk(record(), 1, 'smaller diff').ready).toBe(true)
  })

  it('answers only an open record on the panel: an answered, applied or closed one is shown, never answered again', () => {
    const sel = { state: 'open' } as const
    expect(selectionAnswerable(sel, record())).toBe(true)
    expect(selectionAnswerable(sel, record({ state: 'answered' }))).toBe(false)
    expect(selectionAnswerable({ state: 'superseded' }, record())).toBe(false)
    expect(selectionAnswerable(sel, record({ state: 'superseded' }))).toBe(false)
    expect(selectionAnswerable(null, record())).toBe(false)
    expect(selectionAnswerable(sel, null)).toBe(false)
  })
})

describe("the panel's words", () => {
  it('say when a selection opens, that a closed one is closed and why, and a comparison ended with none', () => {
    expect(selectionPanelWords(comparison({ settled: false }), null)).toBe('The selection opens when every lane has ended. Nothing is selected by default.')
    expect(selectionPanelWords(comparison({ state: 'ended', settled: true }), null)).toMatch(/^This comparison ended with no candidate to select/)
    const closed = comparison({ settled: false, selection: { decision: 'dr-9', state: 'superseded', options } })
    expect(selectionPanelWords(closed, record({ state: 'superseded', why: 'approach-a was retried: dr-9 no longer lists what is compared' }))).toBe(
      'dr-9 is closed: approach-a was retried: dr-9 no longer lists what is compared. A new selection opens when every lane has ended; answer that one.',
    )
    // An open record speaks for itself (DR1's words).
    expect(selectionPanelWords(comparison({ selection: { decision: 'dr-9', state: 'open', options } }), record())).toBe('')
  })

  it("say whom it is for once the record names a person, and none before", () => {
    expect(selectionFor({ for: { label: 'role:architecture', person: 'ana', responsibility: 'architecture' } })).toBe('for ana, the architecture owner')
    expect(selectionFor({})).toBe('')
  })

  it("say Brakeman's state: not asked, why there is none, queued, drafting, briefed", () => {
    expect(brakemanWords(null).words).toBe('Brakeman was not asked to compare these candidates.')
    expect(brakemanWords({ why: 'Brakeman is not set up' })).toEqual({ words: 'Brakeman is not set up', open: false, briefed: false })
    expect(brakemanWords({ request: 'br-3', state: 'queued' })).toEqual({ words: "Brakeman's draft is queued (br-3)", open: true, briefed: false })
    // The live store's request moves first: running, then briefed, before the comparison is read again.
    expect(brakemanWords({ request: 'br-3', state: 'queued' }, { state: 'running', config: undefined as never })).toMatchObject({ open: true, words: 'Brakeman is drafting (br-3)' })
    expect(brakemanWords({ request: 'br-3', state: 'queued' }, { state: 'briefed', hasBrief: true, config: undefined as never })).toMatchObject({ briefed: true })
    expect(brakemanWords({ request: 'br-3', state: 'over_budget', why: "over today's budget" }).words).toBe("No draft from br-3: over today's budget")
  })

  it('mark each statement with who said it', () => {
    expect(claimSource('approach-a', 1)).toBe("said by approach-a's agent, attempt 1")
    expect(reviewSource('approach-b', 2)).toBe("approach-b's review, attempt 2")
    expect(candidateAt('approach-b', B)).toBe('approach-b at 4b825dc')
    expect([verdictWord('approved'), verdictWord('needs_changes'), verdictWord('request_changes')]).toEqual(['approved', 'asked for changes', 'asked for changes'])
  })
})

describe('the measured part and the supporting panel', () => {
  it("are kept apart: a cell's mark and a column's mark read the same whatever the supporting panel says", () => {
    const plain = comparison()
    const said = comparison({ supporting: favouringA })
    const measured = (c: Comparison) => ({ cells: c.checks.map((k) => k.cells.map(cellWords)), verdicts: c.checks.map(verdictWords), columns: c.candidates.map(candidateMark) })
    expect(measured(said)).toEqual(measured(plain))
    // The failed cell of approach-b is failed, though its review asked only for changes and approach-a's agent called itself best.
    expect(measured(said).cells[0]![1]!.state).toBe('failed')
  })

  it('are two components: the supporting panel never receives the checks, and draws what agents wrote as text', () => {
    const src = readFileSync(new URL('../components/CompareSupporting.vue', import.meta.url), 'utf8')
    expect(src).toMatch(/defineProps<\{ supporting: ComparisonSupporting \| null \| undefined; run: string \}>/)
    expect(src).not.toMatch(/checks|v-html|MarkdownPreview/)
  })
})
