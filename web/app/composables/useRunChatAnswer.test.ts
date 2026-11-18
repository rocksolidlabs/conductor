import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ChatMessage } from '~/utils/protocol'
import { ReplyError } from '~/utils/reply'
import { runChatAsker, useRunChatAnswer, type RunChatAsker } from './useRunChatAnswer'

// A member's question answered from a run's chat (#1009): the choice goes to the running session of the member the line is on,
// naming the question, with the page's credential; a line no running member asked, or a page that may not answer, offers no
// button and sends nothing; a press that sends nothing says why. useQuickReply and useToast are Nuxt's auto-imports: stubbed.

const question = (on: string | undefined, id = 'q1'): ChatMessage => ({
  t: 'chat',
  id,
  at: '2026-11-18T10:16:05Z',
  scope: 'run',
  kind: 'question',
  by: { id: 'agent', name: 'codex', role: 'agent' },
  text: 'Update available 0.167.2 → 0.170.0',
  on,
  options: [
    { label: 'Skip this update', input: '\u001b' },
    { label: 'Skip until next version', input: '3' },
  ],
})

const cand = (name: string, id?: string): RunChatAsker => ({ name, session: id ? { id, kind: 'server' } : undefined })

let sends: Array<{ session: { id: string }; input: string; opts: Record<string, unknown> }>
let outcome: () => Promise<boolean>
let toasts: Array<{ title: string; description?: string }>
beforeEach(() => {
  sends = []
  toasts = []
  outcome = () => Promise.resolve(true)
  vi.stubGlobal('useQuickReply', () => ({
    send: (session: { id: string }, input: string, opts: Record<string, unknown>) => {
      sends.push({ session, input, opts })
      return outcome()
    },
  }))
  vi.stubGlobal('useToast', () => ({ add: (t: { title: string; description?: string }) => toasts.push(t) }))
})
afterEach(() => vi.unstubAllGlobals())

describe('runChatAsker', () => {
  it('is the one member of the name the line is on, with a running session', () => {
    const members = [cand('cand-a', 's-a'), cand('cand-b', 's-b')]
    expect(runChatAsker(question('cand-b'), members)?.session?.id).toBe('s-b')
  })
  it("is none for a line on an attempt's label, a member without a session, two of one name, or no question", () => {
    const members = [cand('cand-a', 's-a'), cand('cand-b'), cand('dup', 's-1'), cand('dup', 's-2')]
    expect(runChatAsker(question('cand-a · approach-a #1'), members)).toBeUndefined()
    expect(runChatAsker(question('cand-b'), members)).toBeUndefined()
    expect(runChatAsker(question('dup'), members)).toBeUndefined()
    expect(runChatAsker(question(undefined), members)).toBeUndefined()
    expect(runChatAsker({ ...question('cand-a'), kind: 'message' }, members)).toBeUndefined()
  })
})

describe('useRunChatAnswer', () => {
  it("sends the choice's keys to the member's session, naming the question, with the page's credential", async () => {
    const a = useRunChatAnswer({ members: () => [cand('cand-b', 's-b')], may: () => true, via: () => ({ token: 'link', server: 'yard.example' }) })
    expect(a.answerable(question('cand-b'))).toBe(true)
    await a.answer(question('cand-b'), 0)
    expect(sends).toEqual([{ session: { id: 's-b', kind: 'server' }, input: '\u001b', opts: { token: 'link', server: 'yard.example', question: 'q1' } }])
    expect(toasts).toEqual([])
  })

  it('offers nothing and sends nothing where the page may not answer (a view link)', async () => {
    const a = useRunChatAnswer({ members: () => [cand('cand-b', 's-b')], may: () => false })
    expect(a.answerable(question('cand-b'))).toBe(false)
    await a.answer(question('cand-b'), 0)
    expect(sends).toEqual([])
    expect(toasts.map((t) => t.title)).toEqual(['Not sent'])
  })

  it('says so when no running member asked the line, its session gone meanwhile', async () => {
    const members = [cand('cand-b', 's-b')]
    const a = useRunChatAnswer({ members: () => members, may: () => true })
    expect(a.answerable(question('cand-b'))).toBe(true)
    members[0] = cand('cand-b')
    await a.answer(question('cand-b'), 1)
    expect(sends).toEqual([])
    expect(toasts).toEqual([{ title: 'Not sent', description: "This page cannot reach cand-b's session to answer its question, so nothing was sent. Answer it in its terminal.", color: 'warning' }])
  })

  it('says so when nothing left the page: a reply to it went already, or is on its way', async () => {
    outcome = () => Promise.resolve(false)
    const a = useRunChatAnswer({ members: () => [cand('cand-b', 's-b')], may: () => true })
    await a.answer(question('cand-b'), 0)
    expect(toasts).toEqual([{ title: 'Already sent', description: "Your reply to cand-b's question went from this page, or is on its way. Nothing more was sent.", color: 'warning' }])
  })

  it("says the session's refusal as the session page does: answered meanwhile", async () => {
    outcome = () => Promise.reject(new ReplyError('question_answered', 'the question was answered by Priya: nothing was sent'))
    const a = useRunChatAnswer({ members: () => [cand('cand-b', 's-b')], may: () => true })
    await a.answer(question('cand-b'), 0)
    expect(toasts.map((t) => [t.title, t.description])).toEqual([["Already answered", "cand-b's question was answered by Priya. Nothing was sent. If the agent still asks, answer it in its terminal."]])
  })
})
