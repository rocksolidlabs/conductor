import { describe, expect, it } from 'vitest'
import type { DecideOkMsg, DecisionReply } from '../protocol'
import type { InboxAction } from '../inbox'
import { responseBody, type Decision } from '../decisions'
import {
  DecisionLane,
  LaneError,
  RECORD_RESTARTS,
  assembleRecord,
  decideOf,
  decideOfComment,
  decideOfResponse,
  decideOutcome,
  decideUnconfirmed,
  pageOf,
  refusedWords,
  type LaneAsk,
  type LaneDecide,
  type LaneRecordPart,
  type LaneWire,
} from './decide'

/** A wire whose replies the test gives, one by one, in any order. */
function fakeWire() {
  const asked: Array<{ m: LaneAsk; settle: (r: DecisionReply) => void; fail: (e: unknown) => void }> = []
  const decided: Array<{ m: LaneDecide; settle: (r: DecideOkMsg) => void; fail: (e: unknown) => void }> = []
  const wire: LaneWire = {
    laneRequest: (m) => new Promise((settle, fail) => asked.push({ m, settle, fail })),
    decide: (m) => new Promise((settle, fail) => decided.push({ m, settle, fail })),
  }
  return { wire, asked, decided }
}

const flush = () => new Promise((r) => setTimeout(r, 0))

describe('decideOf', () => {
  const retry: InboxAction = { name: 'retry', label: 'Retry', expect: '12', scope: 'Starts attempt 4', route: 'POST /api/runs/r/tasks/build/retry' }
  it("maps an action onto the frame's fields: its name, its expect as the revision, and the action's own opId", () => {
    expect(decideOf({ ref: 'task:r/build' }, retry, 'op-1')).toEqual({ ref: 'task:r/build', action: 'retry', revision: '12', opId: 'op-1' })
  })
  it("carries a choice's keys exactly, a line only where the action takes one, and a plan's ticket revision", () => {
    const choice: InboxAction = { name: 'answer', label: 'SQLite', input: '2', expect: 'q-1', scope: 'Types 2', route: '/ws/sessions/s' }
    expect(decideOf({ ref: 'question:s/q-1' }, choice, 'op-2', 'ignored')).toEqual({ ref: 'question:s/q-1', action: 'answer', revision: 'q-1', input: '2', opId: 'op-2' })
    const line: InboxAction = { name: 'answer', label: 'Type a reply', text: true, expect: 'q-1', scope: 'Types a line', route: '/ws/sessions/s' }
    expect(decideOf({ ref: 'question:s/q-1' }, line, 'op-3', ' a line ')).toMatchObject({ text: ' a line ' })
    const approve: InboxAction = { name: 'approve', label: 'Approve', expect: '3', ticketRev: 9, scope: 'Approves', route: 'POST /api/dispatch/plans/p/approve' }
    expect(decideOf({ ref: 'plan:CND-1/2' }, approve, 'op-4')).toMatchObject({ revision: '3', ticketRev: 9 })
  })
})

describe('the lane client', () => {
  it('holds a fifth read back until one of the four under way has its reply, and asks it then', async () => {
    const f = fakeWire()
    const lane = new DecisionLane(() => f.wire)
    const reads = Array.from({ length: 5 }, (_, i) => lane.read(`task:r/t${i}`))
    await flush()
    expect(f.asked.map((a) => a.m)).toHaveLength(4)
    f.asked[2]!.settle({ reqId: 'x', item: { ref: 'task:r/t2' } })
    await flush()
    expect(f.asked).toHaveLength(5)
    expect(f.asked[4]!.m).toEqual({ t: 'decision_read', ref: 'task:r/t4' })
    await expect(reads[2]).resolves.toMatchObject({ ref: 'task:r/t2' })
    for (const a of f.asked) a.settle({ reqId: 'x', item: { ref: 'ok' } })
  })

  it('holds a fifth decide back the same way, apart from the reads', async () => {
    const f = fakeWire()
    const lane = new DecisionLane(() => f.wire)
    for (let i = 0; i < 5; i++) lane.decide({ ref: 'r', action: 'retry', revision: '1', opId: `op-${i}` }).catch(() => {})
    void lane.list()
    await flush()
    expect(f.decided).toHaveLength(4)
    expect(f.asked).toHaveLength(1)
    f.decided[0]!.fail(new LaneError('stale_revision', 'moved'))
    await flush()
    expect(f.decided[4]!.m.opId).toBe('op-4')
  })

  it("reads a page with the link's Recent, a track and an evidence entry, checking the entry the reply names", async () => {
    const f = fakeWire()
    const lane = new DecisionLane(() => f.wire)
    const page = lane.list('cursor')
    const tr = lane.track('task:r/build', 'op-1', 'op-1')
    const ev = lane.evidence('task:r/build', 'check:r/build/3/never/0')
    const wrong = lane.evidence('task:r/build', 'check:r/build/3/never/1')
    await flush()
    expect(f.asked.map((a) => a.m)).toEqual([
      { t: 'decisions_list', after: 'cursor' },
      { t: 'decision_track', ref: 'task:r/build', receipt: 'op-1', attempt: 'op-1' },
      { t: 'decision_evidence', ref: 'task:r/build', evidence: 'check:r/build/3/never/0' },
      { t: 'decision_evidence', ref: 'task:r/build', evidence: 'check:r/build/3/never/1' },
    ])
    f.asked[0]!.settle({ reqId: 'a', list: { items: [{ ref: 'task:r/build', kind: 'task', since: 's', title: 't', brief: 'b', question: '', answerer: 'you', actions: null }], next: 'n2', total: 60, at: 'now', recent: [{ ref: 'task:r/x', receipt: 'op-0', attempt: 'op-0', at: 'then' }] } })
    f.asked[1]!.settle({ reqId: 'b', track: { ref: 'task:r/build', receipt: 'op-1', attempt: 'op-1', steps: [{ step: 'resumed', state: 'done', words: 'attempt 4 runs' }] } })
    f.asked[2]!.settle({ reqId: 'c', evidence: { ref: 'check:r/build/3/never/0', kind: 'log', tail: 'the check looked', exit: 1 } })
    f.asked[3]!.settle({ reqId: 'd', evidence: { ref: 'check:r/build/3/never/0', kind: 'log', tail: 'not this one', exit: 1 } })
    await expect(page).resolves.toMatchObject({ total: 60, next: 'n2', items: [{ actions: [] }], recent: [{ receipt: 'op-0' }] })
    await expect(tr).resolves.toMatchObject({ steps: [{ step: 'resumed', state: 'done' }] })
    await expect(ev).resolves.toMatchObject({ kind: 'log', tail: 'the check looked' })
    await expect(wrong).rejects.toMatchObject({ code: 'invalid' })
  })

  it('sends nothing with no connection', async () => {
    const lane = new DecisionLane(() => null)
    await expect(lane.list()).rejects.toMatchObject({ code: 'not_sent' })
    await expect(lane.decide({ ref: 'r', action: 'retry', revision: '1', opId: 'o' })).rejects.toMatchObject({ code: 'not_sent' })
  })
})

describe('what a refusal or a lost reply says', () => {
  it('tells a refusal from a decide that may have been taken', () => {
    expect(decideUnconfirmed(new LaneError('closed', 'x'))).toBe(true)
    expect(decideUnconfirmed(new LaneError('no_word', 'x'))).toBe(true)
    expect(decideUnconfirmed(new LaneError('stale_revision', 'x'))).toBe(false)
    expect(decideUnconfirmed(new Error('x'))).toBe(false)
  })

  it('settles an action only on a reply to it: confirmed, refused, or else owed', () => {
    expect(decideOutcome(null, false)).toBe('confirmed')
    for (const c of ['closed', 'no_word', 'not_sent', 'too_many_requests', 'unavailable']) {
      expect(decideOutcome(new LaneError(c, 'x'), false), c).toBe('owed')
      expect(decideOutcome(new LaneError(c, 'x'), true), c).toBe('owed')
    }
    expect(decideOutcome(new Error('x'), false), 'no word of the owner').toBe('owed')
    for (const c of ['stale_revision', 'question_answered', 'not_in_scope', 'op_conflict', 'task_state', 'invalid']) expect(decideOutcome(new LaneError(c, 'x'), true), c).toBe('refused')
    // A replay holds the link before it reads the opId: its refusal of the link says nothing of an earlier send.
    expect(decideOutcome(new LaneError('revoked', 'x'), false)).toBe('refused')
    expect(decideOutcome(new LaneError('revoked', 'x'), true)).toBe('owed')
    expect(decideOutcome(new LaneError('expired', 'x'), true)).toBe('owed')
  })
  it("words each of the lane's codes, and keeps an operation's own words", () => {
    expect(refusedWords(new LaneError('stale_revision', 'x'), 'task')).toBe('The run moved on. Nothing was done.')
    expect(refusedWords(new LaneError('stale_revision', 'x'), 'question')).toBe('The question moved on. Nothing was done.')
    expect(refusedWords(new LaneError('question_answered', 'x'), 'question')).toContain('Nothing was typed')
    expect(refusedWords(new LaneError('task_state', 'the task is running'), 'task')).toBe('the task is running')
  })
  it('reads a list the server left short as empty lists', () => {
    expect(pageOf({ total: 0, at: 'now' })).toEqual({ items: [], total: 0, at: 'now', recent: [] })
  })
})

// DR1's kinds on the lane (docs/plans/decision-inbox.md, PR-4): DecisionRespond's body (responseBody, the one the card sends to DR1's
// respond route) and a comment's, each mapped onto the DECIDE frame field for field, nothing dropped or merged.
describe("DR1's response and comment as a decide", () => {
  it('maps a choice with its rationale field for field: the response id the opId, the rev the revision', () => {
    const body = responseBody({ choice: 2, rationale: 'Smaller change.', expectedRev: 7 }, 'resp-1')
    expect(decideOfResponse({ ref: 'decision:dr-3' }, 'clarification', body)).toEqual({ ref: 'decision:dr-3', action: 'answer', revision: '7', opId: 'resp-1', choice: 2, rationale: 'Smaller change.' })
  })
  it("maps a clarification's words beside its choice, whole, and leaves out the name a person typed (a link's name is the link's)", () => {
    const words = 'Use <the> "second" & keep\nthe first'
    const body = responseBody({ choice: 0, text: words, rationale: 'Because.', expectedRev: 12, byName: 'Nate' }, 'resp-2')
    const m = decideOfResponse({ ref: 'decision:dr-9' }, 'clarification', body)
    expect(m).toEqual({ ref: 'decision:dr-9', action: 'answer', revision: '12', opId: 'resp-2', choice: 0, text: words, rationale: 'Because.' })
    expect(m).not.toHaveProperty('input')
    expect(m).not.toHaveProperty('byName')
  })
  it("names an approval's first option approve and its second reject, as the owner holds them (choice 0, choice 1)", () => {
    expect(decideOfResponse({ ref: 'decision:dr-1' }, 'approval', responseBody({ choice: 0, expectedRev: 1 }, 'a'))).toMatchObject({ action: 'approve', choice: 0 })
    expect(decideOfResponse({ ref: 'decision:dr-1' }, 'approval', responseBody({ choice: 1, rationale: 'Not this week.', expectedRev: 1 }, 'b'))).toEqual({ ref: 'decision:dr-1', action: 'reject', revision: '1', opId: 'b', choice: 1, rationale: 'Not this week.' })
    expect(decideOfResponse({ ref: 'decision:dr-1' }, 'assignment', responseBody({ choice: 1, expectedRev: 1 }, 'c'))).toMatchObject({ action: 'answer', choice: 1 })
  })
  it('maps a comment and a request for more information onto their kind, text and rev', () => {
    expect(decideOfComment({ ref: 'decision:dr-4' }, { opId: 'op-9', kind: 'more_info', text: 'Which tests?', expectedRev: 5 })).toEqual({ ref: 'decision:dr-4', action: 'more_info', revision: '5', opId: 'op-9', text: 'Which tests?' })
    expect(decideOfComment({ ref: 'decision:dr-4' }, { opId: 'op-8', kind: 'comment', text: 'Noted.', expectedRev: 6 })).toEqual({ ref: 'decision:dr-4', action: 'comment', revision: '6', opId: 'op-8', text: 'Noted.' })
  })
})

// The record's assembler (docs/plans/decision-inbox.md, PR-4): the parts of one mark handed whole, a record_moved starting again from
// the head, four restarts then the words, and an assembly a later one replaced asking nothing more and handing nothing on.
describe("a DR1 record's assembly over the lane", () => {
  const ref = 'decision:dr-1'
  const head = (rev: string, state = 'open'): LaneRecordPart => ({ ref, part: 'head', rev, decision: { id: 'dr-1', state, rev: Number(rev.slice(0, -5)) || 1, request: { question: 'Ship?' } } as unknown as Decision, refs: [{ ref: 'run:r', seen: {}, now: {} }], total: 0 })
  const receipt = (k: number) => ({ decision: 'dr-1', k, status: 'accepted' as const, words: `r${k}`, actor: 'admin', at: '', rev: k })
  const row = (seq: number) => ({ seq, at: '', kind: 'opened' })

  /** A record of `responses` receipts (3 a page) and `history` rows (2 a page) at `rev`, served as the owner cuts it: newest first. */
  function owner(rev: () => string, responses: number, history: number) {
    const asked: Array<{ part: string; rev?: string; after?: string }> = []
    const read = async (part: LaneRecordPart['part'], r?: string, after?: string): Promise<LaneRecordPart> => {
      asked.push({ part, rev: r, after })
      const now = rev()
      if (part === 'head') return head(now)
      if (r !== now) throw new LaneError('record_moved', 'moved', { rev: now })
      const from = Number(after ?? 0)
      const n = part === 'responses' ? responses : history
      const size = part === 'responses' ? 3 : 2
      const all = Array.from({ length: n }, (_, i) => n - i)
      const page = all.slice(from, from + size)
      const out: LaneRecordPart = { ref, part, rev: now, total: n, ...(from + size < n ? { next: String(from + size) } : {}) }
      if (part === 'responses') {
        out.responses = page.map(receipt)
        if (from === 0) out.answer = 'Approve'
      } else out.history = page.map(row)
      return out
    }
    return { read, asked }
  }

  it("hands every part of one mark whole, the responses and history in the order the route keeps them, with the head's refs", async () => {
    const o = owner(() => '201003', 7, 5)
    const got = await assembleRecord(ref, o.read)
    expect('whole' in got).toBe(true)
    if (!('whole' in got)) return
    expect(got.whole.rev).toBe('201003')
    expect(got.whole.decision.responses?.map((r) => r.k)).toEqual([1, 2, 3, 4, 5, 6, 7])
    expect(got.whole.decision.history?.map((h) => h.seq)).toEqual([1, 2, 3, 4, 5])
    expect(got.whole.decision.answer).toBe('Approve')
    expect(got.whole.refs.map((r) => r.ref)).toEqual(['run:r'])
    // Every part after the head named the head's mark, and each page the next its page before gave.
    expect(o.asked.filter((a) => a.part !== 'head').every((a) => a.rev === '201003')).toBe(true)
    expect(o.asked.filter((a) => a.part === 'responses').map((a) => a.after)).toEqual([undefined, '3', '6'])
  })

  it('starts again from the head when a part was cut at another mark, and hands on only the record of one mark', async () => {
    let rev = '101001'
    const o = owner(() => rev, 4, 2)
    const read = async (part: LaneRecordPart['part'], r?: string, after?: string) => {
      const p = await o.read(part, r, after)
      // The record moves once, after its first page of responses was read: the next part is refused record_moved.
      if (part === 'responses' && !after && rev === '101001') rev = '202002'
      return p
    }
    const got = await assembleRecord(ref, read)
    expect(o.asked.filter((a) => a.part === 'head')).toHaveLength(2)
    expect('whole' in got && got.whole.rev).toBe('202002')
    expect('whole' in got && got.whole.decision.responses).toHaveLength(4)
  })

  it('gives up after four restarts in a row (record_moved each time), and says so for the next change to read again', async () => {
    let n = 0
    const o = owner(() => `${++n}01001`, 1, 1)
    const got = await assembleRecord(ref, o.read)
    expect(got).toEqual({ moved: true })
    expect(o.asked.filter((a) => a.part === 'head')).toHaveLength(RECORD_RESTARTS + 1)
  })

  it('takes a part whose own mark differs from the one asked as moved, never as the record', async () => {
    let first = true
    const read = async (part: LaneRecordPart['part'], r?: string): Promise<LaneRecordPart> => {
      if (part === 'head') return head('101000')
      if (part === 'responses' && first) {
        first = false
        return { ref, part, rev: '999000', total: 0 }
      }
      return { ref, part, rev: r ?? '', total: 0 }
    }
    const got = await assembleRecord(ref, read)
    expect('whole' in got && got.whole.rev).toBe('101000')
    expect(first).toBe(false)
  })

  it('asks nothing more and hands nothing on once a later assembly has begun', async () => {
    let live = true
    const o = owner(() => '101002', 1, 2)
    const read = async (part: LaneRecordPart['part'], r?: string, after?: string) => {
      const p = await o.read(part, r, after)
      if (part === 'responses') live = false
      return p
    }
    expect(await assembleRecord(ref, read, () => live)).toEqual({ dropped: true })
    expect(o.asked.map((a) => a.part)).toEqual(['head', 'responses'])
  })

  it('asks no head again when the part in flight as it was dropped is refused record_moved, and hands no refusal on', async () => {
    let live = true
    const o = owner(() => '101002', 1, 2)
    const read = async (part: LaneRecordPart['part'], r?: string, after?: string) => {
      if (part === 'responses') {
        // The item is left while the part is under way, and the owner refuses the part: the record moved meanwhile.
        o.asked.push({ part, rev: r, after })
        live = false
        throw new LaneError('record_moved', 'moved', { rev: '202003' })
      }
      return o.read(part, r, after)
    }
    expect(await assembleRecord(ref, read, () => live)).toEqual({ dropped: true })
    expect(o.asked.map((a) => a.part)).toEqual(['head', 'responses'])
    // Any other refusal of a dropped assembly's part is not handed on either: the reply is the dropped request's.
    live = true
    const failing = async (part: LaneRecordPart['part'], r?: string, after?: string) => {
      if (part === 'history') {
        live = false
        throw new LaneError('no_word', 'no word came back')
      }
      return o.read(part, r, after)
    }
    expect(await assembleRecord(ref, failing, () => live)).toEqual({ dropped: true })
  })

  it('restarts only while it is still wanted: a restart dropped before its head asks nothing', async () => {
    let live = true
    let heads = 0
    const read = async (part: LaneRecordPart['part'], r?: string): Promise<LaneRecordPart> => {
      if (part === 'head') {
        heads++
        return head('101000')
      }
      // Each part moved; the second refusal comes as a later change begins another assembly.
      if (heads === 2) live = false
      throw new LaneError('record_moved', 'moved', { rev: r })
    }
    expect(await assembleRecord(ref, read, () => live)).toEqual({ dropped: true })
    expect(heads).toBe(2)
  })

  it('refuses a head that names another record, and a page cursor that does not move on', async () => {
    await expect(assembleRecord(ref, async () => ({ ...head('101000'), ref: 'decision:dr-2' }))).rejects.toMatchObject({ code: 'invalid' })
    const stuck = async (part: LaneRecordPart['part'], r?: string): Promise<LaneRecordPart> => (part === 'head' ? head('103000') : { ref, part, rev: r!, total: 3, next: '0', responses: [] })
    await expect(assembleRecord(ref, stuck)).rejects.toMatchObject({ code: 'invalid' })
  })

  it("reads a part through the lane's decision_record, naming the head's mark and the page's cursor", async () => {
    const f = fakeWire()
    const lane = new DecisionLane(() => f.wire)
    const p = lane.record(ref, 'history', '101003', '32')
    await flush()
    expect(f.asked[0]!.m).toEqual({ t: 'decision_record', ref, part: 'history', rev: '101003', after: '32' })
    f.asked[0]!.settle({ reqId: 'x', record: { ref, part: 'history', rev: '101003', history: [], total: 3 } })
    await expect(p).resolves.toMatchObject({ part: 'history', total: 3 })
    const h = lane.record(ref, 'head')
    await flush()
    expect(f.asked[1]!.m).toEqual({ t: 'decision_record', ref, part: 'head' })
    f.asked[1]!.settle({ reqId: 'y' })
    await expect(h).rejects.toMatchObject({ code: 'invalid' })
  })
})
