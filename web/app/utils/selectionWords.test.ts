import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { approverLead, offersAskForMore, type Decision } from './decisions'
import { answersOf, choicesWords } from './inbox'

// Needs you's count of choices, and the decision card's words for a comparison's selection in each of its states (open, answered and
// waiting to be applied, applied): each word true of the record as it is.

describe('the count of choices', () => {
  // A row's actions as the list's summary gives them: the labels alone, in the server's order.
  const row = (kind: string, ...actions: string[]) => ({ kind, actions })

  it('counts the answers a person can give, of every kind the page lists, never a decision’s Comment or Ask for more', () => {
    // A comparison's selection: two candidates and None of these, then Comment (as the live check saw it: four labels, three answers).
    expect(choicesWords(row('decision', 'approach-a at d156cae', 'approach-b at 77be1ab', 'None of these', 'Comment'))).toBe('3 choices')
    // An agent's approval, its asking attempt live: Approve, Reject, Ask for more, Comment.
    expect(choicesWords(row('decision', 'Approve', 'Reject', 'Ask for more information', 'Comment'))).toBe('2 choices')
    // A plan's gate: Approve, Reject, Comment.
    expect(choicesWords(row('decision', 'Approve', 'Reject', 'Comment'))).toBe('2 choices')
    // A clarification: its options and its line of words are answers.
    expect(choicesWords(row('decision', 'stub-a', 'stub-b', 'Reply with words', 'Ask for more information', 'Comment'))).toBe('3 choices')
    // A record answered and not yet applied: its one action a comment, which answers nothing.
    expect(choicesWords(row('decision', 'Comment'))).toBe('')
    // An option that happens to be called Comment is an answer: only the server's own last Comment is not.
    expect(answersOf('decision', ['Comment', 'Ask for more information', 'Comment'])).toEqual(['Comment'])
    // A session's question, a task's commands, a plan revision: every action is an answer, whatever it is called.
    expect(choicesWords(row('question', 'Yes', 'No', 'Reply with a line'))).toBe('3 choices')
    expect(choicesWords(row('task', 'Retry', 'Accept anyway', 'Cancel', 'Check again'))).toBe('4 choices')
    expect(choicesWords(row('plan', 'Approve', 'Withdraw'))).toBe('2 choices')
    expect(choicesWords(row('question', 'Comment'))).toBe('1 choice')
    expect(choicesWords(row('question'))).toBe('')
  })
})

const selection = (over: Partial<Decision> = {}): Decision =>
  ({
    id: 'dr-2',
    run: 'r1',
    openedIn: 'r1',
    origin: 'compare',
    type: 'approval',
    state: 'open',
    rev: 1,
    createdAt: '2026-11-18T00:00:00Z',
    policy: { revision: 'local/1', approver: 'operator', quorum: 1, authority: [{ kind: 'operator' }] },
    request: { question: 'Which approach?', options: [{ label: 'approach-a at d156cae' }, { label: 'None of these' }], recommendation: -1 },
    ...over,
  }) as Decision
const accepted = { k: 1, status: 'accepted', choice: 0, rationale: 'smaller', actor: 'operator', byName: 'Nate', at: '2026-11-18T00:01:00Z' }

describe("a selection's card, by its state", () => {
  it('is to be answered while open, answered once answered and once applied, and was for its approver when closed with none', () => {
    expect(approverLead(selection())).toBe('to be answered by')
    expect(approverLead(selection({ state: 'answered', rev: 2, responses: [accepted] } as Partial<Decision>))).toBe('answered by')
    expect(approverLead(selection({ state: 'applied', rev: 3, responses: [accepted] } as Partial<Decision>))).toBe('answered by')
    expect(approverLead(selection({ state: 'expired' }))).toBe('was for')
    expect(approverLead(selection({ state: 'superseded' }))).toBe('was for')
    // Every kind, not the selection alone.
    expect(approverLead(selection({ origin: 'agent' }))).toBe('to be answered by')
  })

  it('offers no Ask for more on a selection, in any state: no agent asked it; an agent’s and a plan’s record keep it', () => {
    for (const state of ['open', 'answered', 'applied'] as const) expect(offersAskForMore(selection({ state }))).toBe(false)
    expect(offersAskForMore(selection({ origin: 'agent' }))).toBe(true)
    expect(offersAskForMore(selection({ origin: 'plan' }))).toBe(true)
  })

  it('draws the approver line from approverLead, the count from choicesWords and Ask for more only where it is offered', () => {
    const card = readFileSync(new URL('../components/DecisionCard.vue', import.meta.url), 'utf8')
    const respond = readFileSync(new URL('../components/DecisionRespond.vue', import.meta.url), 'utf8')
    const page = readFileSync(new URL('../pages/decisions.vue', import.meta.url), 'utf8')
    expect(card).toContain('{{ approverLead(decision) }}')
    expect(card).not.toMatch(/· answered by <span data-decision-approver>/)
    expect(respond).toMatch(/<UButton v-if="asks" label="Ask for more"/)
    expect(respond).toMatch(/v-if="asks && !more\.enabled"[^>]*data-decision-more-why/)
    expect(page).toContain(':label="choicesWords(s)"')
    // The ticket's Decisions word a record with whether its run is held, as the card and the comparison do.
    const ticket = readFileSync(new URL('../components/DispatchDecisions.vue', import.meta.url), 'utf8')
    expect(ticket).toContain('decisionWords(r, { runHeld: runHeldOf(live.runOf(r.run)) })')
    expect(ticket).not.toMatch(/decisionWords\(r\)/)
    expect(page).not.toMatch(/s\.actions\.length\} \$\{s\.actions\.length === 1/)
  })
})
