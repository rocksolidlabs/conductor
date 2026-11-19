import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as vue from 'vue'
import { carried, DECISION_ANSWERING, replyKept, replyWords, type DecisionAnswering } from './answering'
import type { Decision, ResponseBody } from './decisions'
import type { DeliveryStep } from './inbox'
import type { ShellAnswer, ShellDraft, ShellItem, ShellProvider, ShellReply } from './shell'
import { answeringState, SHELL_COMMENT_WORDS, shellAnswering } from './transport/shell'

// The shell's answering, as DecisionRespond reaches it (docs/plans/phone-shell.md, OSS-2b, Tests per surface,
// shell-transport.test.ts). The app's vitest runs in node with no component mounted, so the component's rules are held through the
// functions it draws them from (carried, replyKept, replyWords) and the answering it is handed; the component drawn is
// shell-decisions.spec.ts's, and the component with nothing injected the decision page's own specs'.

const ROWS: DeliveryStep[] = [
  { step: 'available', state: 'done', words: '' },
  { step: 'recorded', state: 'waiting', words: '' },
  { step: 'validated', state: 'waiting', words: '' },
  { step: 'resumed', state: 'waiting', words: '' },
]
const decision = (type: Decision['type'], rev = 2) => ({ id: 'dr-7', type, state: 'open', rev, request: { question: 'q', options: [{ label: 'A' }, { label: 'B' }] } }) as unknown as Decision
const item = (type: Decision['type'], rev = 2): ShellItem => ({ id: 'i1', decision: decision(type, rev), rows: ROWS, owner: { name: 'desk' }, asOf: '', offline: false })

function provider(type: Decision['type']) {
  const sent: ShellAnswer[] = []
  const drafts = new Map<string, ShellDraft>()
  let reply: ShellReply = { outcome: 'kept', words: 'Kept', item: item(type) }
  let read: ShellItem | null = item(type)
  const p: Pick<ShellProvider, 'inbox' | 'drafts'> = {
    inbox: {
      list: async () => ({ items: [], asOf: '' }),
      item: async () => {
        if (!read) throw { code: 'offline', words: 'offline' }
        return structuredClone(read)
      },
      respond: async (_id, a) => (sent.push(a), structuredClone(reply)),
      answerOf: async () => ({ state: 'none' }),
      resolve: async () => ({ id: 'i1' }),
      watch: () => () => {},
    },
    drafts: {
      get: async (id) => drafts.get(id) ?? null,
      list: async () => [...drafts.values()],
      put: async (d) => (drafts.set(d.id, d), { ok: true }),
      remove: async (id) => void drafts.delete(id),
    },
  }
  let n = 0
  const made = shellAnswering(p, 'Desk relay', { mint: () => `draft-${++n}`, now: () => 0 })
  return { ...made, answering: made.answeringFor('i1'), sent, drafts, setReply: (r: ShellReply) => (reply = r), setRead: (i: ShellItem | null) => (read = i) }
}

/** A body as DecisionRespond builds it: its own responseId and the rev the card showed, which the shell's answering does not send. */
const body = (b: Partial<ResponseBody>): ResponseBody => ({ responseId: 'minted-by-the-component', expectedRev: 99, ...b })

describe("the shell's answering", () => {
  for (const type of ['clarification', 'approval', 'assignment'] as const) {
    it(`maps ${type}'s answer onto respond field for field, with the draft's responseId and rev`, async () => {
      const t = provider(type)
      await t.machine.open('i1', item(type))
      const c = type === 'clarification' ? { text: 'in words', rationale: 'why' } : { choice: 1, rationale: 'why' }
      t.answering.draft!.changed('dr-7', c)
      await vi.waitFor(() => expect(t.machine.view('i1').draft).toBeTruthy())
      const r = await t.answering.respond('dr-7', body(c))
      expect(t.sent).toEqual([{ responseId: 'draft-1', expectedRev: 2, ...c }])
      expect(replyKept(r)).toBe(true)
      expect(r.receipt).toMatchObject({ decision: 'dr-7', status: 'accepted', words: 'Kept' })
    })
  }

  it('passes back every outcome and its words: refused, waiting, unknown; only kept with a receipt runs the success path', async () => {
    const t = provider('approval')
    await t.machine.open('i1', item('approval'))
    t.setReply({ outcome: 'refused', code: 'not_approver', words: 'Not yours to answer' })
    let r = await t.answering.respond('dr-7', body({ choice: 0 }))
    expect(r.outcome).toBe('refused')
    expect(replyKept(r)).toBe(false)
    expect(t.answering.state!('dr-7')).toMatchObject({ lock: false, words: 'Not yours to answer' })
    t.setReply({ outcome: 'unknown' })
    r = await t.answering.respond('dr-7', body({ choice: 0 }))
    expect(r.outcome).toBe('unknown')
    expect(t.answering.state!('dr-7')).toMatchObject({ lock: true, actions: ['confirm'] })
    expect(replyKept({ outcome: 'kept' })).toBe(false)
    expect(replyWords({ outcome: 'waiting' })).toBe('Not sent yet: waiting.')

  })

  it('a read that fails offline leaves the answer waiting, as typed, locked with Cancel', async () => {
    const t = provider('clarification')
    await t.machine.open('i1', item('clarification'))
    t.setRead(null)
    const r = await t.answering.respond('dr-7', body({ text: 'as typed' }))
    expect(r.outcome).toBe('waiting')
    expect(replyKept(r)).toBe(false)
    expect(t.answering.state!('dr-7')).toMatchObject({ lock: true, actions: ['cancel'] })
    expect(t.answering.draft!.initial('dr-7')).toEqual({ text: 'as typed' })
    expect(t.sent).toEqual([])
  })

  it('carries respond alone: Comment and Ask for more drawn disabled with its words, and never reaching it', async () => {
    const t = provider('clarification')
    expect(carried(t.answering, 'respond')).toEqual({ on: true, words: '' })
    expect(carried(t.answering, 'comment')).toEqual({ on: false, words: SHELL_COMMENT_WORDS })
    expect(carried(t.answering, 'more_info')).toEqual({ on: false, words: SHELL_COMMENT_WORDS })
    await expect(t.answering.comment('dr-7', { kind: 'comment', text: 'x', expectedRev: 2 })).rejects.toThrow(SHELL_COMMENT_WORDS)
    // With nothing injected, every control.
    expect(carried(null, 'comment')).toEqual({ on: true, words: '' })
    expect(carried({ respond: async () => ({ outcome: 'kept' }), comment: async () => ({}) as never } as DecisionAnswering, 'more_info')).toEqual({ on: true, words: '' })
  })

  it("restores the draft's fields from initial and writes them through changed; a draft locked from the first frame, with Confirm and Cancel", async () => {
    const t = provider('approval')
    t.drafts.set('i1', { id: 'i1', responseId: 'r9', rev: 2, choice: 1, rationale: 'kept', state: 'waiting', savedAt: new Date(0).toISOString() })
    await t.machine.open('i1', item('approval'))
    expect(t.answering.draft!.initial('dr-7')).toEqual({ choice: 1, rationale: 'kept' })
    expect(t.answering.state!('dr-7')).toMatchObject({ lock: true, actions: ['cancel'] })
    await t.machine.resume('i1')
    expect(t.answering.state!('dr-7')).toMatchObject({ lock: true, actions: ['confirm', 'cancel'] })
    // A change while locked writes no draft.
    t.answering.draft!.changed('dr-7', { choice: 0 })
    await new Promise((r) => setTimeout(r, 0))
    expect(t.drafts.get('i1')?.choice).toBe(1)
    await t.answering.cancel!('dr-7')
    expect(t.answering.state!('dr-7')).toBeNull()
    // Confirm, offered no longer, sends nothing: awaited to its end.
    await t.answering.confirm!('dr-7')
    expect(t.sent).toEqual([])
  })

  it('states a kept answer as kept, with its receipt, whichever press sent it', async () => {
    const t = provider('clarification')
    t.drafts.set('i1', { id: 'i1', responseId: 'r9', rev: 2, text: 'as typed', state: 'waiting', savedAt: new Date(0).toISOString() })
    await t.machine.open('i1', item('clarification'))
    await t.machine.resume('i1')
    await t.answering.confirm!('dr-7')
    expect(t.sent).toEqual([{ responseId: 'r9', expectedRev: 2, text: 'as typed' }])
    expect(t.answering.state!('dr-7')).toMatchObject({ lock: false, kept: true, receipt: { decision: 'dr-7', status: 'accepted', words: 'Kept' } })
    // Answer pressed again after it sends nothing: the answer stands as kept.
    const r = await t.answering.respond('dr-7', body({ text: 'as typed' }))
    expect(r.outcome).toBe('kept')
    expect(t.sent).toHaveLength(1)
  })

  it('states a step with no lock as null, and a waiting or unknown one as locked', () => {
    expect(answeringState(undefined)).toBeNull()
    expect(answeringState({ step: 'editing', words: '', lock: false, actions: [], noAnswer: false, draft: null, notSaved: '' })).toBeNull()
    expect(answeringState({ step: 'waiting', words: 'w', lock: true, actions: ['cancel'], noAnswer: false, draft: null, notSaved: '' })).toEqual({ lock: true, words: 'w', actions: ['cancel'] })
    expect(answeringState({ step: 'editing', words: 'elsewhere', lock: false, actions: [], noAnswer: true, draft: null, notSaved: '' })).toEqual({ lock: true, words: 'elsewhere', actions: [] })
  })
})

describe('DecisionRespond with nothing injected', () => {
  beforeEach(() => vi.stubGlobal('inject', vue.inject))
  afterEach(() => vi.unstubAllGlobals())

  it('finds no answering, so it posts through the server’s routes as before, and the key is a symbol of its own', () => {
    const app = vue.createApp({ render: () => null })
    expect(app.runWithContext(() => vue.inject(DECISION_ANSWERING, null))).toBeNull()
    expect(typeof DECISION_ANSWERING).toBe('symbol')
  })
})

describe("an answering made for a page's item", () => {
  /** A provider whose inbox holds two items from two servers, both holding the record dr-3. */
  function twoServers() {
    const sent: Array<{ id: string } & ShellAnswer> = []
    const of = (id: string, question: string): ShellItem => ({ id, decision: { ...decision('clarification', 2), id: 'dr-3', request: { question, options: [{ label: 'A' }, { label: 'B' }] } } as unknown as Decision, rows: ROWS, owner: { name: id === 'X' ? 'server A' : 'server B' }, asOf: '', offline: false })
    const items: Record<string, ShellItem> = { X: of('X', 'Ship A?'), Y: of('Y', 'Ship B?') }
    const p: Pick<ShellProvider, 'inbox' | 'drafts'> = {
      inbox: {
        list: async () => ({ items: Object.values(items), asOf: '' }),
        item: async (id) => structuredClone(items[id]!),
        respond: async (id, a) => (sent.push({ id, ...a }), { outcome: 'kept', item: structuredClone(items[id]!) }),
        answerOf: async () => ({ state: 'none' }),
        resolve: async () => ({ id: 'X' }),
        watch: () => () => {},
      },
      drafts: { get: async () => null, list: async () => [], put: async () => ({ ok: true }), remove: async () => {} },
    }
    let n = 0
    return { ...shellAnswering(p, 'Desk relay', { mint: () => `r${++n}`, now: () => 0 }), items, sent }
  }

  it("sends the page's answer to the page's item, though another item holding the same record was opened after it", async () => {
    const t = twoServers()
    const onY = t.answeringFor('Y')
    await t.machine.open('Y', t.items.Y!)
    // The first page's read answers late, after the second page opened.
    await t.machine.open('X', t.items.X!)
    onY.draft!.changed('dr-3', { choice: 1 })
    await vi.waitFor(() => expect(t.machine.view('Y').draft?.choice).toBe(1))
    expect(t.machine.view('X').draft).toBeNull()
    const r = await onY.respond('dr-3', body({ choice: 1 }))
    expect(replyKept(r)).toBe(true)
    expect(t.sent.map((s) => s.id)).toEqual(['Y'])
    expect(onY.state!('dr-3')).toMatchObject({ kept: true })
    expect(t.answeringFor('X').state!('dr-3')).toBeNull()
  })

  it('refuses a call that names a record its item does not hold: nothing sent, kept or restored', async () => {
    const t = twoServers()
    const onY = t.answeringFor('Y')
    await t.machine.open('Y', t.items.Y!)
    const r = await onY.respond('dr-9', body({ choice: 0 }))
    expect(r).toMatchObject({ outcome: 'refused' })
    expect(onY.state!('dr-9')).toMatchObject({ lock: true })
    onY.draft!.changed('dr-9', { choice: 0 })
    await new Promise((res) => setTimeout(res, 0))
    expect(t.machine.view('Y').draft).toBeNull()
    expect(onY.draft!.initial('dr-9')).toBeNull()
    await onY.confirm!('dr-9')
    expect(t.sent).toEqual([])
    // An item not opened yet holds no record: refused too.
    expect(await t.answeringFor('X').respond('dr-3', body({ choice: 0 }))).toMatchObject({ outcome: 'refused' })
    expect(t.sent).toEqual([])
  })
})

describe("the provider's calls, bounded", () => {
  it('a send that does not answer within its wait is not known, and the draft stays locked', async () => {
    vi.useFakeTimers()
    try {
      const sent: string[] = []
      const p: Pick<ShellProvider, 'inbox' | 'drafts'> = {
        inbox: {
          list: async () => ({ items: [], asOf: '' }),
          item: async () => item('clarification'),
          respond: (id) => (sent.push(id), new Promise(() => {})),
          answerOf: () => new Promise(() => {}),
          resolve: async () => ({ id: 'i1' }),
          watch: () => () => {},
        },
        drafts: { get: async () => null, list: async () => [], put: async () => ({ ok: true }), remove: async () => {} },
      }
      const t = shellAnswering(p, 'Desk relay', { mint: () => 'r1', now: () => 0, wait: 1000 })
      await t.machine.open('i1', item('clarification'))
      const r = t.answeringFor('i1').respond('dr-7', body({ choice: 0 }))
      await vi.advanceTimersByTimeAsync(1000)
      await vi.advanceTimersByTimeAsync(1000)
      expect((await r).outcome).toBe('unknown')
      expect(t.answeringFor('i1').state!('dr-7')).toMatchObject({ lock: true })
      expect(sent).toEqual(['i1'])
    } finally {
      vi.useRealTimers()
    }
  })
})
