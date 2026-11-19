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
  return { ...made, sent, drafts, setReply: (r: ShellReply) => (reply = r), setRead: (i: ShellItem | null) => (read = i) }
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
      await vi.waitFor(() => expect(t.drafts.get('i1')).toBeTruthy())
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
    t.answering.cancel!('dr-7')
    await vi.waitFor(() => expect(t.answering.state!('dr-7')).toBeNull())
    t.answering.confirm!('dr-7')
    expect(t.sent).toEqual([])
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
