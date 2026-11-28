import { describe, expect, it } from 'vitest'
import type { Attention, AttentionOption } from './protocol'
import { ANSWER_IN_ITS_TERMINAL, OPEN_TO_ANSWER, tileAskOf } from './tileAsk'

const options: AttentionOption[] = [
  { label: 'One pull request', input: '1' },
  { label: 'Two, the API first', input: '2' },
]
const question = 'Ship the refund split as one pull request, or two (the API first, then the storefront)?'
const yes = () => true
const asks = (a: Partial<Attention>) => ({ agentId: 'claude', attention: { state: 'needs_input', message: question, id: 'q1', ...a } as Attention })

describe('tileAskOf', () => {
  it('says who asks, the question whole and its choices, for a question with choices the page may answer', () => {
    const a = tileAskOf('review', asks({ options }), yes)
    expect(a).toMatchObject({ who: 'review asks:', question, kind: 'choices' })
    expect(a?.kind === 'choices' && a.line).toMatchObject({ kind: 'question', id: 'q1', on: 'review', options })
  })

  it("reads a person's mark with no words as one, and a mark with words from the workbench as a question", () => {
    expect(tileAskOf('review', asks({ source: 'admin', message: undefined, id: undefined }), yes)).toMatchObject({ who: 'review needs you.', question: 'Marked by a person.', kind: 'terminal' })
    expect(tileAskOf('review', asks({ source: 'admin' }), yes)).toMatchObject({ who: 'review asks:', question })
  })

  it('says where a question the tile cannot answer is answered, from the same cases as a reply field', () => {
    // No choices, a startup screen, a permission dialog whose choices were not read, a line typed and not entered: its terminal.
    // (A line typed and not entered carries no choices: the server posts it with none.)
    for (const a of [asks({}), asks({ source: 'trust' }), asks({ kind: 'permission', source: 'hook' }), asks({ source: 'submit' })]) {
      expect(tileAskOf('review', a, yes)).toMatchObject({ who: 'review asks:', question, kind: 'terminal', words: ANSWER_IN_ITS_TERMINAL })
    }
    expect(ANSWER_IN_ITS_TERMINAL).toBe('Answer it in its terminal.')
    // A durable decision's question: its card, by its id.
    expect(tileAskOf('review', asks({ options, decision: 'dr-7' }), yes)).toMatchObject({ kind: 'decision', decision: 'dr-7', words: 'Answer dr-7' })
    // A chat's permission request: the member's own view.
    expect(tileAskOf('review', asks({ kind: 'permission', source: 'protocol' }), yes)).toMatchObject({ kind: 'open', words: OPEN_TO_ANSWER })
    expect(OPEN_TO_ANSWER).toBe('Open it to answer')
    // A member that runs as a chat: its question with no choices is answered in its Chat, never in a terminal it does not show.
    expect(tileAskOf('review', { ...asks({}), way: 'protocol' as const }, yes)).toMatchObject({ kind: 'open', words: OPEN_TO_ANSWER })
    expect(tileAskOf('review', { ...asks({ options }), way: 'protocol' as const }, () => false)).toMatchObject({ kind: 'open' })
    // Its choices the page may press stay choices, and a decision's question stays its decision's.
    expect(tileAskOf('review', { ...asks({ options }), way: 'protocol' as const }, yes)).toMatchObject({ kind: 'choices' })
    expect(tileAskOf('review', { ...asks({ options, decision: 'dr-7' }), way: 'protocol' as const }, yes)).toMatchObject({ kind: 'decision' })
    // Choices the page may not press: answered in its terminal.
    expect(tileAskOf('review', asks({ options }), () => false)).toMatchObject({ kind: 'terminal' })
  })

  it('draws nothing while the member does not ask', () => {
    expect(tileAskOf('review', { agentId: 'claude', attention: { state: 'working' } as Attention }, yes)).toBeNull()
    expect(tileAskOf('review', undefined, yes)).toBeNull()
  })
})
