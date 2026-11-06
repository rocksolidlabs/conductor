import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ref, type Ref } from 'vue'
import { ReplyError } from '~/utils/reply'
import { useQuickReply } from './useQuickReply'

// What became of an answer, as the decision page keeps it (#647, review round 1): the owner's own word (`taken`), an older owner
// that never confirms (`unconfirmed`: the keys went, no word will come), and a reply that never left this tab (`not_sent`). The
// composable reads Nuxt's auto-imports as globals: each is stubbed, the transport a fake whose answer the test settles.

let states: Map<string, Ref<unknown>>
/** How the fake owner answers: its word (true), an older owner's silence (false), or a refusal; `held` keeps it until let go. */
let word: () => Promise<boolean>
beforeEach(() => {
  states = new Map()
  word = () => Promise.resolve(true)
  vi.stubGlobal('useState', <T>(key: string, init: () => T) => {
    if (!states.has(key)) states.set(key, ref(init()))
    return states.get(key) as Ref<T>
  })
  vi.stubGlobal('useWorkbenchToken', () => ({ token: ref('t') }))
  vi.stubGlobal('useAttentionStore', () => ref({ sessions: new Map() }))
  vi.stubGlobal('useTypedAnswers', () => ({ remember: () => {} }))
  vi.stubGlobal('QUESTION_ANSWERED', 'question_answered')
  vi.stubGlobal('useTerminalTransport', () => ({
    create: () => ({
      connect: () => Promise.resolve({ t: 'welcome', role: 'control' }),
      answer: () => word(),
      close: () => {},
    }),
  }))
})
afterEach(() => vi.unstubAllGlobals())

let n = 0
const fresh = () => `q${++n}`

describe("an answer's outcome", () => {
  it("is taken only on the owner's own word", async () => {
    const quick = useQuickReply()
    expect(await quick.answer({ id: 's1', kind: 'server' }, { input: '1' }, { question: fresh(), reqId: 'd1' })).toBe('taken')
  })

  it('is unconfirmed through an older owner, whose welcome says no answer: the keys went and no word comes', async () => {
    word = () => Promise.resolve(false)
    const quick = useQuickReply()
    expect(await quick.answer({ id: 's2', kind: 'hosted' }, { text: 'go on' }, { question: fresh(), reqId: 'd2' })).toBe('unconfirmed')
  })

  it('is not sent while a reply to the session is on its way from this tab, nor twice to one question', async () => {
    let release: (v: boolean) => void = () => {}
    word = () => new Promise((r) => (release = r))
    const quick = useQuickReply()
    const q = fresh()
    const first = quick.answer({ id: 's3', kind: 'server' }, { input: '1' }, { question: q, reqId: 'd3' })
    await new Promise((r) => setTimeout(r, 0))
    expect(await quick.answer({ id: 's3', kind: 'server' }, { input: '2' }, { question: fresh(), reqId: 'd4' })).toBe('not_sent')
    release(true)
    expect(await first).toBe('taken')
    // The question this tab answered: a second press sends nothing.
    expect(await quick.answer({ id: 's3', kind: 'server' }, { input: '2' }, { question: q, reqId: 'd5' })).toBe('not_sent')
  })

  it("rejects with the owner's refusal or the lost connection, for the page to judge (answerFailure)", async () => {
    word = () => Promise.reject(new ReplyError('closed', 'the connection closed before the session said whether it took the reply'))
    const quick = useQuickReply()
    await expect(quick.answer({ id: 's4', kind: 'hosted' }, { input: '1' }, { question: fresh(), reqId: 'd6' })).rejects.toMatchObject({ code: 'closed' })
  })
})
