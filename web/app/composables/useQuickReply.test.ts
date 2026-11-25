import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ref, type Ref } from 'vue'
import { skipWords } from '~/utils/crews'
import { NOT_AT_PROMPT_WORDS } from '~/utils/protocol'
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

// A palette line (`say`, unlessWaiting) the owner refuses with not_sent: held back while the session asks, it says the broadcast's
// words; held back because the agent's screen is not its prompt, the owner's own words, which say where to look.
describe("a palette line's refusal", () => {
  /** The owner's refusal of the next line: a not_sent with its words. */
  let refusal: string
  beforeEach(() => {
    vi.stubGlobal('useTerminalTransport', () => ({
      create: () => ({
        connect: () => Promise.resolve({ t: 'welcome', role: 'control' }),
        onClose: () => {},
        onControl: () => {},
        submitAcked: () => Promise.reject(new ReplyError('not_sent', refusal)),
        close: () => {},
      }),
    }))
  })

  it("says the agent's screen is not its prompt, and where to look, in the owner's words", async () => {
    refusal = NOT_AT_PROMPT_WORDS
    const quick = useQuickReply()
    expect(await quick.say({ id: 's5', kind: 'server' }, 'now run the tests')).toEqual({ refused: NOT_AT_PROMPT_WORDS })
  })

  it("says the session asks, in the broadcast's words, for a session that asks", async () => {
    refusal = 'the agent asks a question on its screen: answer with the question\'s choices, or in its terminal'
    const quick = useQuickReply()
    expect(await quick.say({ id: 's6', kind: 'server' }, 'now run the tests')).toEqual({ refused: skipWords('needs_input') })
  })
})
