import { describe, expect, it } from 'vitest'
import type { AttentionOption } from './protocol'
import { tileAskOf } from './tileAsk'

const options: AttentionOption[] = [
  { label: 'One pull request', input: '1' },
  { label: 'Two, the API first', input: '2' },
]
const question = 'Ship the refund split as one pull request, or two (the API first, then the storefront)?'
const yes = () => true

describe('tileAskOf', () => {
  it('says who asks, the question whole and its choices, for a question with choices the page may answer', () => {
    const a = tileAskOf('review', { agentId: 'claude', attention: { state: 'needs_input', message: question, id: 'q1', options } }, yes)
    expect(a).not.toBeNull()
    expect(a!.who).toBe('review asks:')
    expect(a!.question).toBe(question)
    expect(a!.line).toMatchObject({ kind: 'question', id: 'q1', on: 'review', options })
  })

  it('draws nothing where the page may not answer, or the member does not ask with choices', () => {
    const asks = { agentId: 'claude', attention: { state: 'needs_input' as const, message: question, id: 'q1', options } }
    expect(tileAskOf('review', asks, () => false)).toBeNull()
    expect(tileAskOf('review', { agentId: 'claude', attention: { state: 'working' } }, yes)).toBeNull()
    expect(tileAskOf('review', undefined, yes)).toBeNull()
    expect(tileAskOf('review', { agentId: 'claude', attention: { state: 'needs_input', message: question, id: 'q1' } }, yes)).toBeNull()
  })
})
