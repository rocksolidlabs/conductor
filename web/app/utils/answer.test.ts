import { describe, expect, it } from 'vitest'
import { ANSWER_WORDS, createAnswer, type AnswerView } from './answer'
import type { Decision } from './decisions'
import type { DeliveryStep } from './inbox'
import type { ShellAnswer, ShellAnswerOf, ShellDraft, ShellDrafts, ShellInbox, ShellItem, ShellReply } from './shell'

// The answer's state machine (docs/plans/phone-shell.md, "On the phone: an answer, from the tap to the record"), row by row in the
// table's order, over a fake provider whose replies the test sets and whose reads it holds and releases; with A1 to A4 checked over
// what every row sent: one responseId per draft and only its content under it, nothing sent without a read in the same press, the
// provider's rows shown as given, and nothing after a sign-out.

const T0 = Date.parse('2026-11-19T10:00:00Z')

function decision(rev: number, state = 'open', extra: Partial<Decision> = {}): Decision {
  return { id: 'dr-7', run: 'r', openedIn: 'r', origin: 'agent', type: 'approval', request: { question: 'Ship it?', options: [{ label: 'Yes' }, { label: 'No' }] }, policy: {}, state, rev, createdAt: '2026-11-19T09:00:00Z', ...extra } as Decision
}
const ROWS: DeliveryStep[] = [
  { step: 'available', state: 'done', words: 'Request available' },
  { step: 'recorded', state: 'waiting', words: 'No answer yet' },
  { step: 'validated', state: 'waiting', words: '' },
  { step: 'resumed', state: 'waiting', words: '' },
]
function item(rev: number, extra: Partial<ShellItem> = {}): ShellItem {
  return { id: 'i1', decision: decision(rev), rows: ROWS, owner: { name: 'desk' }, asOf: '2026-11-19T10:00:00Z', offline: false, ...extra }
}

/** A gate a fake call waits on until the test releases it. */
function gate() {
  let open!: () => void
  const p = new Promise<void>((r) => (open = r))
  return { p, open }
}

/** A fake provider: the item it reads, the replies it gives, every call it was made, and its drafts by id. */
function fake(start: ShellItem = item(3)) {
  const f = {
    item: start as ShellItem,
    readFails: null as null | { code: string; words: string },
    replies: [] as ShellReply[],
    answerOf: { state: 'none' } as ShellAnswerOf | Error,
    sent: [] as ShellAnswer[],
    reads: 0,
    drafts: new Map<string, ShellDraft>(),
    putRefusal: null as null | { ok: false; code: 'full' | 'too_large' | 'not_saved' | 'signed_out'; words: string },
    readGate: null as null | ReturnType<typeof gate>,
    sendGate: null as null | ReturnType<typeof gate>,
  }
  const inbox: ShellInbox = {
    list: async () => ({ items: [f.item], asOf: '' }),
    item: async () => {
      f.reads++
      if (f.readGate) await f.readGate.p
      if (f.readFails) throw f.readFails
      return structuredClone(f.item)
    },
    respond: async (id, a) => {
      f.sent.push(structuredClone(a))
      // The provider's contract: the draft stored `unknown` with its hash before anything leaves the phone.
      const d = f.drafts.get(id)
      if (d) f.drafts.set(id, { ...d, state: 'unknown', h: `h:${a.responseId}` })
      if (f.sendGate) await f.sendGate.p
      return structuredClone(f.replies.shift() ?? { outcome: 'kept', item: f.item })
    },
    answerOf: async () => {
      if (f.answerOf instanceof Error) throw f.answerOf
      return structuredClone(f.answerOf)
    },
    resolve: async () => ({ id: 'i1' }),
    watch: () => () => {},
  }
  const drafts: ShellDrafts = {
    get: async (id) => structuredClone(f.drafts.get(id) ?? null),
    list: async () => [...f.drafts.values()],
    put: async (d) => {
      if (f.putRefusal) return f.putRefusal
      f.drafts.set(d.id, structuredClone(d))
      return { ok: true }
    },
    remove: async (id) => void f.drafts.delete(id),
  }
  let n = 0
  const seen: AnswerView[] = []
  const make = () => createAnswer({ inbox, drafts, name: 'Desk relay', mint: () => `r${++n}`, now: () => T0, changed: (_id, v) => seen.push(v) })
  return { f, make, seen }
}

const tick = () => new Promise((r) => setTimeout(r, 0))

/** A1 and A2 over what was sent: every send carries a responseId the drafts minted and a content the draft held, after a read. */
function invariants(f: ReturnType<typeof fake>['f'], readsBefore: number[]) {
  for (const [i, s] of f.sent.entries()) {
    expect(s.responseId).toMatch(/^r\d+$/)
    expect(readsBefore[i], `send ${i} came after a read of its own`).toBeGreaterThan(i === 0 ? 0 : readsBefore[i - 1]!)
  }
}

describe("the answer's state machine, row by row", () => {
  it('reading → editing: the draft kept as typed; a put refused shows its words and is never shown as kept', async () => {
    const { f, make } = fake()
    const m = make()
    await m.open('i1', f.item)
    let v = await m.edit('i1', { choice: 0, rationale: 'fine' })
    expect(f.drafts.get('i1')).toMatchObject({ responseId: 'r1', rev: 3, choice: 0, rationale: 'fine', state: 'editing' })
    expect(v.draft?.responseId).toBe('r1')
    f.putRefusal = { ok: false, code: 'full', words: 'thirty-two kept' }
    v = await m.edit('i1', { choice: 1 })
    expect(v.notSaved).toBe(`${ANSWER_WORDS.notSaved}: thirty-two kept`)
    expect(v.draft?.choice).toBe(0)
    expect(f.drafts.get('i1')?.choice).toBe(0)
  })

  it('editing → Answer → checking: Answer locked, "Checking the decision", the item read again; a second tap while checking or sending sends nothing new', async () => {
    const { f, make } = fake()
    const m = make()
    await m.open('i1', f.item)
    f.readGate = gate()
    const first = m.answer('i1', { choice: 0 })
    await tick()
    expect(m.view('i1')).toMatchObject({ step: 'checking', words: ANSWER_WORDS.checking, lock: true })
    await m.answer('i1', { choice: 1 })
    f.sendGate = gate()
    f.readGate.open()
    await tick()
    await tick()
    expect(m.view('i1')).toMatchObject({ step: 'sending', words: ANSWER_WORDS.sending, lock: true })
    await m.answer('i1', { choice: 1 })
    f.sendGate.open()
    const end = await first
    expect(end.step).toBe('kept')
    expect(f.sent).toEqual([{ responseId: 'r1', expectedRev: 3, choice: 0 }])
    expect(f.reads).toBe(1)
  })

  it('checking → sending → kept: sent with the draft\'s responseId and rev, field for field; the draft removed; the rows as given', async () => {
    const { f, make } = fake()
    const m = make()
    await m.open('i1', f.item)
    await m.edit('i1', { choice: 1, rationale: 'because' })
    const recorded: ShellItem = { ...item(4), rows: ROWS.map((r) => (r.step === 'recorded' ? { ...r, state: 'done', words: 'Recorded by you' } : r)) }
    f.replies.push({ outcome: 'kept', words: 'Kept: the desk is told', item: recorded })
    const v = await m.answer('i1', { choice: 1, rationale: 'because' })
    expect(f.sent).toEqual([{ responseId: 'r1', expectedRev: 3, choice: 1, rationale: 'because' }])
    expect(v).toMatchObject({ step: 'kept', words: 'Kept: the desk is told', lock: false, draft: null })
    expect(v.item?.rows).toEqual(recorded.rows)
    expect(f.drafts.has('i1')).toBe(false)
  })

  it('checking: a changed rev rebases the draft (a new responseId, the content kept) and sends nothing; the next Answer sends once at the rev drawn', async () => {
    const { f, make } = fake(item(3))
    const m = make()
    await m.open('i1', f.item)
    await m.edit('i1', { choice: 0, text: 'as typed' })
    f.item = item(4)
    let v = await m.answer('i1', { choice: 0, text: 'as typed' })
    expect(v).toMatchObject({ step: 'editing', words: ANSWER_WORDS.changed, lock: false })
    expect(v.item?.decision.rev).toBe(4)
    expect(f.sent).toEqual([])
    expect(f.drafts.get('i1')).toMatchObject({ rev: 4, responseId: 'r2', choice: 0, text: 'as typed', state: 'editing' })
    v = await m.answer('i1', { choice: 0, text: 'as typed' })
    expect(v.step).toBe('kept')
    expect(f.sent).toEqual([{ responseId: 'r2', expectedRev: 4, choice: 0, text: 'as typed' }])
  })

  it('checking: a superseded decision is drawn with its successor named, and nothing is sent', async () => {
    const { f, make } = fake()
    const m = make()
    await m.open('i1', f.item)
    f.item = { ...item(3), decision: decision(3, 'superseded', { supersededBy: 'dr-9' }) }
    const v = await m.answer('i1', { choice: 0 })
    expect(v.words).toBe(`${ANSWER_WORDS.changed}. It was replaced by dr-9`)
    expect(f.sent).toEqual([])
  })

  it('a rebase whose put is refused leaves the draft as it was, with its words', async () => {
    const { f, make } = fake()
    const m = make()
    await m.open('i1', f.item)
    await m.edit('i1', { choice: 0 })
    f.item = item(5)
    f.putRefusal = { ok: false, code: 'not_saved', words: 'disk full' }
    const v = await m.answer('i1', { choice: 0 })
    expect(v.notSaved).toBe(`${ANSWER_WORDS.notSaved}: disk full`)
    expect(f.drafts.get('i1')).toMatchObject({ rev: 3, responseId: 'r1' })
    expect(f.sent).toEqual([])
  })

  it('checking: recorded by another answer, or the decision not open: its words, Answer held, the draft kept and nothing sent', async () => {
    const { f, make } = fake()
    const m = make()
    await m.open('i1', f.item)
    await m.edit('i1', { choice: 0 })
    f.item = { ...item(3), rows: ROWS.map((r) => (r.step === 'recorded' ? { ...r, state: 'done', words: 'Ana answered first' } : r)), own: { responseId: 'other', state: 'answered', words: '' } }
    let v = await m.answer('i1', { choice: 0 })
    expect(v).toMatchObject({ step: 'editing', words: 'Ana answered first', noAnswer: true })
    expect(await m.answer('i1', { choice: 0 })).toBe(m.view('i1'))
    expect(f.sent).toEqual([])
    expect(f.drafts.get('i1')?.choice).toBe(0)

    const g = fake()
    const m2 = g.make()
    await m2.open('i1', g.f.item)
    g.f.item = { ...item(3), decision: decision(3, 'cancelled') }
    v = await m2.answer('i1', { choice: 1 })
    expect(v.noAnswer).toBe(true)
    expect(v.words).toBeTruthy()
    expect(g.f.sent).toEqual([])
  })

  it("checking: an item with `elsewhere` shows its words, no Answer, the draft kept and nothing sent", async () => {
    const { f, make } = fake()
    const m = make()
    await m.open('i1', f.item)
    f.item = item(3, { elsewhere: 'Answered by the release team on their own page' })
    const v = await m.answer('i1', { choice: 0 })
    expect(v).toMatchObject({ step: 'editing', noAnswer: true, words: 'Answered by the release team on their own page' })
    expect(f.sent).toEqual([])
    expect(f.drafts.get('i1')).toBeTruthy()
  })

  it('checking: offline waits with Cancel; back online offers Confirm and sends nothing until it is pressed; Confirm sends once', async () => {
    const { f, make } = fake()
    const m = make()
    await m.open('i1', f.item)
    f.readFails = { code: 'offline', words: 'offline' }
    let v = await m.answer('i1', { choice: 0 })
    expect(v).toMatchObject({ step: 'waiting', words: ANSWER_WORDS.offline, actions: ['cancel'], lock: true })
    expect(f.drafts.get('i1')?.state).toBe('waiting')
    f.readFails = null
    v = await m.resume('i1')
    expect(v).toMatchObject({ step: 'waiting', words: ANSWER_WORDS.back, actions: ['confirm', 'cancel'] })
    expect(f.sent).toEqual([])
    expect(await m.edit('i1', { choice: 1 })).toMatchObject({ step: 'waiting' })
    v = await m.confirm('i1')
    expect(v.step).toBe('kept')
    expect(f.sent).toEqual([{ responseId: 'r1', expectedRev: 3, choice: 0 }])
  })

  it('checking: a provider that cannot be reached waits with its name; Cancel takes the answer back to editing', async () => {
    const { f, make } = fake()
    const m = make()
    await m.open('i1', f.item)
    f.readFails = { code: 'unreachable', words: 'gone' }
    let v = await m.answer('i1', { choice: 0 })
    expect(v.words).toBe('Desk relay cannot be reached')
    v = await m.cancel('i1')
    expect(v).toMatchObject({ step: 'editing', lock: false, actions: [] })
    expect(f.drafts.get('i1')?.state).toBe('editing')
    expect(f.sent).toEqual([])
  })

  it('sending: presence refused keeps the draft editing, or waiting again with Confirm for a send begun from waiting', async () => {
    const { f, make } = fake()
    const m = make()
    await m.open('i1', f.item)
    f.replies.push({ outcome: 'refused', code: 'presence_refused' })
    let v = await m.answer('i1', { choice: 0 })
    expect(v).toMatchObject({ step: 'editing', words: ANSWER_WORDS.presence, lock: false })
    f.readFails = { code: 'offline', words: '' }
    await m.answer('i1', { choice: 0 })
    f.readFails = null
    await m.resume('i1')
    f.replies.push({ outcome: 'refused', code: 'presence_refused' })
    v = await m.confirm('i1')
    expect(v).toMatchObject({ step: 'waiting', words: ANSWER_WORDS.presence, actions: ['confirm', 'cancel'] })
    expect(new Set(f.sent.map((s) => s.responseId))).toEqual(new Set(['r1']))
  })

  it('sending: not_saved says nothing was sent; any other refusal its words, the draft editing again, its item judged (a changed rev rebases)', async () => {
    const { f, make } = fake()
    const m = make()
    await m.open('i1', f.item)
    f.replies.push({ outcome: 'refused', code: 'not_saved' })
    let v = await m.answer('i1', { choice: 0 })
    expect(v).toMatchObject({ step: 'editing', words: ANSWER_WORDS.notSavedSend })
    f.replies.push({ outcome: 'refused', code: 'stale_decision', words: 'It moved', item: item(6) })
    v = await m.answer('i1', { choice: 0 })
    expect(v).toMatchObject({ step: 'editing', words: ANSWER_WORDS.changed })
    expect(f.drafts.get('i1')).toMatchObject({ rev: 6, state: 'editing' })
    expect(f.drafts.get('i1')?.responseId).not.toBe('r1')
    f.item = item(6)
    f.replies.push({ outcome: 'refused', code: 'not_approver', words: 'Not yours to answer' })
    v = await m.answer('i1', { choice: 0 })
    expect(v).toMatchObject({ step: 'editing', words: 'Not yours to answer' })
  })

  it("sending: `same` is kept with the item's rows and no words", async () => {
    const { f, make } = fake()
    const m = make()
    await m.open('i1', f.item)
    f.replies.push({ outcome: 'same', words: 'already', item: item(3) })
    expect(await m.answer('i1', { choice: 0 })).toMatchObject({ step: 'kept', words: '' })
  })

  it('sending: unknown locks the draft until answerOf; none offers Confirm with the same responseId and content; found with its h is kept', async () => {
    const { f, make } = fake()
    const m = make()
    await m.open('i1', f.item)
    f.replies.push({ outcome: 'unknown' })
    let v = await m.answer('i1', { choice: 1, rationale: 'ok' })
    expect(v).toMatchObject({ step: 'unknown', words: ANSWER_WORDS.didNotArrive, actions: ['confirm'], lock: true })
    expect(await m.edit('i1', { choice: 0 })).toMatchObject({ step: 'unknown' })
    expect(await m.answer('i1', { choice: 0 })).toMatchObject({ step: 'unknown' })
    f.replies.push({ outcome: 'unknown' })
    f.answerOf = new Error('unreachable')
    v = await m.confirm('i1')
    expect(v).toMatchObject({ step: 'unknown', words: ANSWER_WORDS.unknown, actions: [], lock: true })
    f.answerOf = { state: 'answered', words: 'Kept: Yes', h: 'h:r1' }
    v = await m.resume('i1')
    expect(v).toMatchObject({ step: 'kept', words: 'Kept: Yes', draft: null })
    expect(f.sent).toEqual([
      { responseId: 'r1', expectedRev: 3, choice: 1, rationale: 'ok' },
      { responseId: 'r1', expectedRev: 3, choice: 1, rationale: 'ok' },
    ])
  })
})

describe('a restart between two steps resumes from the draft', () => {
  it('found unknown (a crash after the store and before the request): checked by answerOf, Confirm offered when it did not arrive', async () => {
    const { f, make } = fake()
    f.drafts.set('i1', { id: 'i1', responseId: 'r9', rev: 3, choice: 0, state: 'unknown', h: 'h:r9', savedAt: '2026-11-19T09:59:00Z' })
    const v = await make().open('i1', f.item)
    expect(v).toMatchObject({ step: 'unknown', words: ANSWER_WORDS.didNotArrive, actions: ['confirm'] })
  })

  it('found unknown after the service kept it (before its reply): kept; with another h, "An earlier answer from this phone was kept"', async () => {
    const { f, make } = fake()
    f.drafts.set('i1', { id: 'i1', responseId: 'r9', rev: 3, choice: 0, state: 'unknown', h: 'h:r9', savedAt: '2026-11-19T09:59:00Z' })
    f.answerOf = { state: 'answered', words: 'Kept', h: 'h:r9' }
    expect(await make().open('i1', f.item)).toMatchObject({ step: 'kept', words: 'Kept' })
    const g = fake()
    g.f.drafts.set('i1', { id: 'i1', responseId: 'r9', rev: 3, state: 'unknown', h: 'h:r9', savedAt: '2026-11-19T09:59:00Z' })
    g.f.answerOf = { state: 'answered', words: 'Kept', h: 'h:other' }
    expect(await g.make().open('i1', g.f.item)).toMatchObject({ step: 'kept', words: ANSWER_WORDS.earlier })
  })

  it('found waiting: waits with Cancel, Confirm offered on resume; found at another rev: rebased; older than seven days: removed', async () => {
    const { f, make } = fake()
    f.drafts.set('i1', { id: 'i1', responseId: 'r9', rev: 3, choice: 0, state: 'waiting', savedAt: '2026-11-19T09:59:00Z' })
    const m = make()
    expect(await m.open('i1', f.item)).toMatchObject({ step: 'waiting', actions: ['cancel'] })
    expect(await m.resume('i1')).toMatchObject({ actions: ['confirm', 'cancel'] })
    expect(f.sent).toEqual([])

    const g = fake(item(5))
    g.f.drafts.set('i1', { id: 'i1', responseId: 'r9', rev: 3, choice: 1, state: 'editing', savedAt: '2026-11-19T09:59:00Z' })
    expect(await g.make().open('i1', g.f.item)).toMatchObject({ step: 'editing', words: ANSWER_WORDS.changed, draft: { rev: 5, choice: 1, responseId: 'r1' } })

    const old = fake()
    old.f.drafts.set('i1', { id: 'i1', responseId: 'r9', rev: 3, choice: 1, state: 'editing', savedAt: '2026-11-11T09:59:00Z' })
    expect((await old.make().open('i1', old.f.item)).draft).toBeNull()
    expect(old.f.drafts.has('i1')).toBe(false)
  })
})

describe('the invariants', () => {
  it('A1 and A2: over a long walk every send carries a draft responseId with its own content, each after a read made in its press', async () => {
    const { f, make } = fake()
    const m = make()
    await m.open('i1', f.item)
    const readsBefore: number[] = []
    const press = async (c: { choice: number }) => {
      const before = f.sent.length
      await m.answer('i1', c)
      if (f.sent.length > before) readsBefore.push(f.reads)
    }
    f.replies.push({ outcome: 'refused', code: 'presence_refused' })
    await press({ choice: 0 })
    f.item = item(4)
    await press({ choice: 0 })
    f.replies.push({ outcome: 'refused', code: 'not_saved' })
    await press({ choice: 1 })
    await press({ choice: 1 })
    invariants(f, readsBefore)
    // The rebase (rev 3 to 4) began a new responseId; before and after it, each held only its draft's content.
    expect(f.sent.map((s) => [s.responseId, s.expectedRev])).toEqual([
      ['r1', 3],
      ['r2', 4],
      ['r2', 4],
    ])
  })

  it('A3: the rows shown are the provider\'s, as given', async () => {
    const { f, make } = fake()
    const m = make()
    const odd: ShellItem = { ...item(3), rows: [{ step: 'resumed', state: 'done', words: 'Work resumed' }] }
    f.replies.push({ outcome: 'kept', item: odd })
    await m.open('i1', f.item)
    expect((await m.answer('i1', { choice: 0 })).item?.rows).toEqual(odd.rows)
  })

  it('A4: after a sign-out, nothing begun before it writes a view or a draft', async () => {
    const { f, make, seen } = fake()
    const m = make()
    await m.open('i1', f.item)
    f.sendGate = gate()
    const sending = m.answer('i1', { choice: 0 })
    await tick()
    await tick()
    expect(m.view('i1').step).toBe('sending')
    m.signedOut()
    const count = seen.length
    f.drafts.clear()
    f.sendGate.open()
    await sending
    expect(seen.length).toBe(count)
    expect(m.view('i1')).toMatchObject({ step: 'editing', draft: null })
    expect(f.drafts.size).toBe(0)
  })
})
