import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import {
  RATIONALE_OPTIONAL,
  RATIONALE_REQUIRED,
  SELECTION_FINAL,
  SELECTION_FINAL_NONE,
  answerAsk,
  askForMore,
  askedWhere,
  authorizesLine,
  comparisonPath,
  confirmAuthorizes,
  decisionWords,
  detailsOpen,
  optionSubject,
  rationaleRequired,
  recommended,
  subjectWords,
  type Decision,
} from '../utils/decisions'

// DR1's card for a comparison's selection (Program item CP1, docs/plans/compare-crew.md, PR-7: "What this row changes in DR1's card"):
// a record of origin `compare` shows each option's subject (the candidate's task and short commit, linked to its comparison) and asks
// for its rationale, the confirm held until it holds text; every other origin renders as DR1 made it. The app's vitest runs in node
// with no component mounted, so the card is tested through the functions it draws from (answerAsk, optionSubject, subjectWords) and
// its sources are checked for the bindings that draw them; what a browser shows is compare-select.spec.ts's.

const RUN = 'crew-0000aaaa-11112222'
const A = 'a'.repeat(40)
const B = '4b825dc642cb6eb9a060e54bf8d69288fbee4904'

/** A selection as Yardmaster opens it (docs/plans/compare-crew.md, PR-6a, "Which record"). */
function selection(over: Partial<Decision> = {}): Decision {
  return {
    id: 'dr-9',
    run: RUN,
    openedIn: RUN,
    origin: 'compare',
    type: 'approval',
    request: {
      question: 'Which approach does CND-12 continue with?',
      options: [
        { label: 'approach-a at aaaaaaa', tradeoffs: 'checks 2/2 passed, comparable; 3 files, +40 −2', subject: { kind: 'candidate', run: RUN, task: 'approach-a', n: 1, commit: A } },
        { label: 'approach-b at 4b825dc', tradeoffs: 'checks 2/2 passed, comparable; 12 files, +340 −90', subject: { kind: 'candidate', run: RUN, task: 'approach-b', n: 2, commit: B } },
        { label: 'None of these' },
      ],
      subject: { kind: 'comparison', run: RUN, base: 'c'.repeat(40), digest: 'd'.repeat(64) },
      // A recommendation the server never writes (it is always -1): the card still marks none.
      recommendation: 1,
      authorizes: 'Continuing the selected candidate into review and repair, and delivering it. Final: the candidates not selected are kept and cannot be continued; a new comparison tries them again.',
    },
    policy: { revision: 'local/1', approver: 'operator' },
    state: 'open',
    rev: 1,
    createdAt: '2026-11-16T10:00:00Z',
    ...over,
  }
}

/** An agent's approval of DR1's, with the same options' shape and no subjects. */
const agents = (): Decision => ({ ...selection(), origin: 'agent', asker: { task: 'build', attempt: 1, live: true }, request: { question: 'May build commit?', options: [{ label: 'Approve' }, { label: 'Reject' }], recommendation: 0, authorizes: 'the commit' } })
const plans = (): Decision => ({ ...selection(), origin: 'plan', gate: 'ship', request: { question: 'Ship?', options: [{ label: 'Approve' }, { label: 'Reject' }], authorizes: 'Approve lets deploy start; Reject cancels it.' } })

describe("a selection's options", () => {
  it("say each candidate's task, attempt and short commit, linked to the ticket's comparison when its key is known, else to the run's task", () => {
    const d = selection()
    const opts = d.request.options!
    expect(optionSubject(opts[1], 'CND-12')).toEqual({ text: 'the result of approach-b, attempt 2 (4b825dc)', to: `/dispatch/CND/12?view=compare&run=${RUN}` })
    expect(optionSubject(opts[0], '')).toEqual({ text: 'the result of approach-a, attempt 1 (aaaaaaa)', to: `/runs/${RUN}?view=tasks&task=approach-a` })
    // None of these names no candidate.
    expect(optionSubject(opts[2], 'CND-12')).toBeNull()
    // An agent's options have no subject: nothing is drawn for them.
    expect(agents().request.options!.map((o) => optionSubject(o, 'CND-12'))).toEqual([null, null])
  })

  it('link the comparison only through a ticket key and a run: what is no key links nowhere new', () => {
    expect(comparisonPath('CND-12', RUN)).toBe(`/dispatch/CND/12?view=compare&run=${RUN}`)
    expect(comparisonPath('', RUN)).toBe('')
    expect(comparisonPath('not a key', RUN)).toBe('')
    expect(comparisonPath('CND-12', '')).toBe('')
  })

  it("say the record's own subject, the comparison, at its base", () => {
    expect(subjectWords(selection().request.subject, { ticket: 'CND-12' })).toEqual({ text: 'the comparison at base ccccccc, digest dddddddddddd', to: `/dispatch/CND/12?view=compare&run=${RUN}` })
    expect(subjectWords(selection().request.subject)).toEqual({ text: 'the comparison at base ccccccc, digest dddddddddddd', to: `/runs/${RUN}?view=tasks` })
  })

  it('mark none recommended, whatever the record says', () => {
    expect(recommended(selection())).toBe(-1)
    expect(recommended(selection({ request: { ...selection().request, recommendation: 0 } }))).toBe(-1)
    // Every other origin keeps its recommendation.
    expect(recommended(agents())).toBe(0)
  })
})

describe("a selection's answer", () => {
  it('needs a rationale: the confirm is held while the rationale is empty or only spaces, and the field says it is required', () => {
    const d = selection()
    expect(rationaleRequired(d)).toBe(true)
    expect(answerAsk(d, 1, '').ready).toBe(false)
    expect(answerAsk(d, 1, '   \n').ready).toBe(false)
    expect(answerAsk(d, 2, '').ready).toBe(false)
    expect(answerAsk(d, 1, 'b keeps the API').ready).toBe(true)
    expect(answerAsk(d, 1, '').placeholder).toBe(RATIONALE_REQUIRED)
  })

  it("names the candidate and says final on its confirm, and says before it that the others cannot be continued", () => {
    const d = selection()
    expect(answerAsk(d, 1, 'why')).toMatchObject({ confirm: 'Select approach-b (final)', final: SELECTION_FINAL })
    expect(SELECTION_FINAL).toBe('Final: the candidates not selected cannot be continued; a new comparison tries them again.')
    expect(answerAsk(d, 2, 'why')).toMatchObject({ confirm: 'Answer None of these (final)', final: SELECTION_FINAL_NONE })
    // Nothing picked: nothing to say final of.
    expect(answerAsk(d, undefined, '').final).toBe('')
  })

  it("is asked of the comparison, never of an agent, with Details open and the server's authorizes as the selection's", () => {
    const d = selection()
    expect(askedWhere(d)).toBe('the comparison')
    expect(askForMore(d)).toEqual({ enabled: false, why: "dr-9 is the comparison's: no agent asked it, comment instead" })
    expect(detailsOpen(d)).toBe(true)
    expect(authorizesLine(d)).toEqual({ lead: 'A selection authorizes: ', words: d.request.authorizes })
    // The confirm says final in the selection's own words, not the approval's line a second time.
    expect(confirmAuthorizes(d, 1)).toBe('')
  })

  it("is shown applied in the server's words, in neutral ink, and recorded on a stopped run as waiting for the run to be continued", () => {
    const applied = selection({ state: 'applied', applied: { at: '2026-11-16T10:05:00Z', by: 'yardmaster', run: RUN, response: 1, validated: true, how: 'selected', selected: { task: 'approach-b', run: RUN, attempt: 2, commit: B }, words: 'approach-b selected at 4b825dc, by Nate' } })
    expect(decisionWords(applied)).toMatchObject({ state: 'Applied: approach-b selected at 4b825dc, by Nate', tone: 'default' })
    expect(decisionWords(selection({ state: 'answered' }), { runHeld: false }).state).toBe('Recorded: waiting for the run to be continued')
    expect(decisionWords(selection()).tone).toBe('warning')
  })
})

describe('every other origin', () => {
  it("answers as DR1 made it: the rationale optional, the confirm 'Answer <label>', no finality words", () => {
    for (const d of [agents(), plans()]) {
      expect(rationaleRequired(d)).toBe(false)
      expect(answerAsk(d, 0, '')).toEqual({ confirm: 'Answer Approve', final: '', placeholder: RATIONALE_OPTIONAL, ready: true })
    }
    expect(confirmAuthorizes(plans(), 0)).toBe('A yes authorizes: Approve lets deploy start; Reject cancels it.')
    expect(confirmAuthorizes(agents(), 1)).toBe('This approval authorizes: the commit')
    expect(askedWhere(agents())).toBe("build's attempt 1")
    expect(detailsOpen(agents())).toBe(false)
  })
})

describe("the card's sources", () => {
  const respond = readFileSync(new URL('./DecisionRespond.vue', import.meta.url), 'utf8')
  const card = readFileSync(new URL('./DecisionCard.vue', import.meta.url), 'utf8')

  it("draw the confirm from answerAsk, held while it is not ready, and send nothing without the rationale", () => {
    expect(respond).toContain(':label="ask.confirm"')
    // Held while it is not ready, whatever else holds it (the answering's lock and what it carries, utils/answering.ts).
    expect(respond).toMatch(/:disabled="!ask\.ready( \|\| [^"]+)?" data-decision-confirm/)
    expect(respond).toMatch(/if \(busy\.value \|\| !ask\.value\.ready( \|\| [^)]+)?\) return/)
    expect(respond).toContain(':placeholder="ask.placeholder"')
    expect(respond).toContain('data-decision-final')
  })

  it("draw each option's subject under the buttons and in the card's option rows", () => {
    expect(respond).toContain(':data-decision-option-subject="t.i"')
    expect(card).toContain(':data-decision-option-subject="i"')
  })
})
