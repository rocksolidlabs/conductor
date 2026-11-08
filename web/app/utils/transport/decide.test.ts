import { describe, expect, it } from 'vitest'
import type { DecideOkMsg, DecisionReply } from '../protocol'
import type { InboxAction } from '../inbox'
import { DecisionLane, LaneError, decideOf, decideUnconfirmed, pageOf, refusedWords, type LaneAsk, type LaneDecide, type LaneWire } from './decide'

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
