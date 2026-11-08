import { describe, expect, it } from 'vitest'
import { nextTick, ref } from 'vue'
import type { DecideOkMsg, DecisionReply } from '~/utils/protocol'
import type { InboxAction, InboxItem, InboxSummary } from '~/utils/inbox'
import { DecisionLane, LaneError, LaneLocal, type LaneAsk, type LaneDecide, type LaneWire } from '~/utils/transport/decide'
import { useJoinDecide } from './useJoinDecide'

// What the join page's Decide keeps (docs/plans/decision-inbox.md, PR-7), over a wire whose replies the test gives one by one, in
// the order it chooses: a More asked of a list read again since lands on no list; a blocker's Cancel, tracked as its task's, shows
// its steps on the question's card; and an action whose connection closed before its reply is owed until a reply confirms or
// refuses it: kept through a reconnect and asked again under its own opId with the welcome, kept when its ask again cannot be sent
// (no connection now), and no other action on its item replaces it meanwhile.

type Asked = { m: LaneAsk; settle: (r: DecisionReply) => void; fail: (e: unknown) => void }
type Decided = { m: LaneDecide; settle: (r: DecideOkMsg) => void; fail: (e: unknown) => void }

function fakeWire() {
  const asked: Asked[] = []
  const decided: Decided[] = []
  const wire: LaneWire = {
    laneRequest: (m) => new Promise((settle, fail) => asked.push({ m, settle, fail })),
    decide: (m) => new Promise((settle, fail) => decided.push({ m, settle, fail })),
  }
  /** The asks of type t not yet taken by the test, oldest first. */
  const taken = new Set<Asked>()
  function take(t: LaneAsk['t'], match: (m: LaneAsk) => boolean = () => true): Asked {
    const a = asked.find((x) => !taken.has(x) && x.m.t === t && match(x.m))
    if (!a) throw new Error(`no ${t} asked: ${JSON.stringify(asked.filter((x) => !taken.has(x)).map((x) => x.m))}`)
    taken.add(a)
    return a
  }
  /** Answers every ask not yet taken of type t with r. */
  function answerAll(t: LaneAsk['t'], r: (m: LaneAsk) => DecisionReply) {
    for (const a of asked) if (!taken.has(a) && a.m.t === t) {
      taken.add(a)
      a.settle(r(a.m))
    }
  }
  return { wire, asked, decided, take, answerAll }
}

const flush = async () => {
  for (let i = 0; i < 5; i++) {
    await new Promise((r) => setTimeout(r, 0))
    await nextTick()
  }
}

function summary(i: number): InboxSummary {
  return { ref: `task:r/t${i}`, kind: 'task', since: new Date(1_700_000_000_000 + i * 1000).toISOString(), title: `Task ${i}`, brief: '', question: '', answerer: '', actions: ['retry'] }
}
const page = (from: number, to: number, total: number, next?: string, recent: unknown[] = []) => ({
  reqId: 'x',
  list: { items: Array.from({ length: to - from + 1 }, (_, k) => summary(from + k)), total, at: '', recent, ...(next ? { next } : {}) },
})

function question(ref: string, actions: InboxAction[]): InboxItem {
  return { ref, subject: ref, kind: 'question', since: '', title: 'Which way?', brief: '', briefBy: 'conductor', question: 'Which way?', answerer: '', revision: 'q-1', blocking: { words: '', refs: [] }, actions, recommended: -1 } as InboxItem
}

describe('useJoinDecide', () => {
  it('drops a More whose reply comes after a list read again from the top, so no page lands on another list', async () => {
    const f = fakeWire()
    const seq = ref(0)
    const d = useJoinDecide(new DecisionLane(() => f.wire), seq)
    seq.value++
    await flush()
    f.take('decisions_list').settle(page(1, 25, 60, 'c25'))
    await flush()
    void d.more()
    await flush()
    f.take('decisions_list', (m) => m.t === 'decisions_list' && m.after === 'c25').settle(page(26, 50, 60, 'c50'))
    await flush()
    expect(d.items.value).toHaveLength(50)
    expect(d.next.value).toBe('c50')

    // The last page asked, then decisions_changed reads the list again from the top, whose reply lands first.
    void d.more()
    await flush()
    const last = f.take('decisions_list', (m) => m.t === 'decisions_list' && m.after === 'c50')
    seq.value++
    await flush()
    f.take('decisions_list', (m) => m.t === 'decisions_list' && !m.after).settle(page(1, 25, 60, 'c25'))
    await flush()
    last.settle(page(51, 60, 60))
    await flush()
    expect(d.items.value.map((s) => s.ref)).toEqual(Array.from({ length: 25 }, (_, k) => `task:r/t${k + 1}`))
    expect(d.next.value, 'More is still there, after the first page').toBe('c25')
    expect(d.total.value).toBe(60)
  })

  it("shows a blocker's Cancel on the question's card: its steps are read under its task's ref, kept with the question", async () => {
    const f = fakeWire()
    const seq = ref(0)
    const d = useJoinDecide(new DecisionLane(() => f.wire), seq)
    const q = 'question:s1/q-1'
    const cancel: InboxAction = { name: 'cancel', label: 'Cancel', expect: '7', scope: 'Cancels the task', route: 'POST /api/runs/r/tasks/build/cancel', track: 'task:r/build' }
    d.open(q)
    await flush()
    f.take('decision_read').settle({ reqId: 'x', item: question(q, [cancel]) })
    await flush()
    expect(d.item.value?.ref).toBe(q)

    void d.act(q, cancel)
    await flush()
    expect(f.decided[0]!.m).toMatchObject({ ref: q, action: 'cancel', revision: '7' })
    f.decided[0]!.settle({ t: 'decide_ok', reqId: 'x', ref: 'task:r/build', receipt: 'cmd-1' })
    await flush()
    const track = f.take('decision_track')
    expect(track.m).toMatchObject({ ref: 'task:r/build', receipt: 'cmd-1', attempt: f.decided[0]!.m.opId })
    track.settle({ reqId: 'x', track: { ref: 'task:r/build', receipt: 'cmd-1', attempt: f.decided[0]!.m.opId, steps: [{ step: 'available', state: 'done', words: '' }, { step: 'recorded', state: 'done', words: '' }] } })
    f.answerAll('decision_read', () => ({ reqId: 'x', item: question(q, []) }))
    await flush()
    expect(d.openRef.value).toBe(q)
    expect(d.delivery.value?.filter((s) => s.state === 'done').map((s) => s.step), "the Cancel's steps on the question's card").toEqual(['available', 'recorded'])
    expect(d.actions.value[0]).toMatchObject({ ref: 'task:r/build', from: q })
  })

  it('keeps an action whose connection closed before its reply through a reconnect, and asks it again under its own opId with the welcome', async () => {
    const f = fakeWire()
    let wire: LaneWire | null = f.wire
    const seq = ref(0)
    const d = useJoinDecide(new DecisionLane(() => wire), seq)
    const q = 'question:s1/q-1'
    const right: InboxAction = { name: 'answer', label: 'Right', input: 'Right\r', expect: 'q-1', scope: 'Types Right', route: '/ws/sessions/s1' }
    seq.value++
    await flush()
    f.take('decisions_list').settle(page(1, 1, 1))
    await flush()
    d.open(q)
    await flush()
    f.take('decision_read').settle({ reqId: 'x', item: question(q, [right]) })
    await flush()

    // The decide goes; the connection closes before its reply: no word of what came of it.
    void d.act(q, right)
    await flush()
    const first = f.decided[0]!
    wire = null
    first.fail(new LaneError(LaneLocal.Closed, 'closed'))
    await flush()
    expect(d.shownFeedback.value?.unknown).toContain('no word came back')
    expect(d.shownFeedback.value?.again?.opId).toBe(first.m.opId)

    // The lane joined again (its welcome moves seq): the list read again, the item still open, and the action, still owed, asked
    // again at once: the same decide, under the same opId.
    wire = f.wire
    seq.value++
    await flush()
    f.answerAll('decisions_list', () => page(1, 1, 1))
    await flush()
    f.answerAll('decision_read', () => ({ reqId: 'x', item: question(q, [right]) }))
    await flush()
    expect(d.openRef.value).toBe(q)
    expect(d.item.value?.ref).toBe(q)
    expect(d.shownFeedback.value?.again?.opId).toBe(first.m.opId)
    expect(f.decided).toHaveLength(2)
    expect(f.decided[1]!.m).toEqual(first.m)

    // The owner's repeat confirms it.
    f.decided[1]!.settle({ t: 'decide_ok', reqId: 'x', ref: q, receipt: 'q-1', repeat: true })
    await flush()
    expect(d.shownFeedback.value?.unknown).toBeUndefined()
    expect(d.actions.value.map((e) => [e.ref, e.attempt])).toEqual([[q, first.m.opId]])
  })

  it('keeps an action owed when its ask again cannot be sent, takes no other action on its item meanwhile, and asks it with the next connection', async () => {
    const f = fakeWire()
    let wire: LaneWire | null = f.wire
    const seq = ref(0)
    const d = useJoinDecide(new DecisionLane(() => wire), seq)
    const q = 'question:s1/q-1'
    const right: InboxAction = { name: 'answer', label: 'Right', input: 'Right\r', expect: 'q-1', scope: 'Types Right', route: '/ws/sessions/s1' }
    const left: InboxAction = { name: 'answer', label: 'Left', input: 'Left\r', expect: 'q-1', scope: 'Types Left', route: '/ws/sessions/s1' }
    d.open(q)
    await flush()
    f.take('decision_read').settle({ reqId: 'x', item: question(q, [right, left]) })
    await flush()

    // The decide goes, and its connection closes before its reply: owed.
    void d.act(q, right)
    await flush()
    const first = f.decided[0]!
    wire = null
    first.fail(new LaneError(LaneLocal.Closed, 'closed'))
    await flush()
    expect(d.shownFeedback.value?.again?.opId).toBe(first.m.opId)

    // Read again with no connection: nothing is sent, and the action is still owed, with words that say so.
    await d.reread()
    await flush()
    expect(f.decided).toHaveLength(1)
    expect(d.shownFeedback.value?.again).toEqual(first.m)
    expect(d.shownFeedback.value?.unknown).toContain('Not connected: nothing was sent.')
    expect(d.shownFeedback.value?.unknown).toContain('still owed')
    expect(d.shownFeedback.value?.words).toBeUndefined()
    expect(d.item.value?.ref, 'the item drawn as last read, its owed action with it').toBe(q)

    // Another action on the item, the item drawn: not taken while the first is owed, so it cannot replace it.
    await d.act(q, left)
    await flush()
    expect(f.decided).toHaveLength(1)
    expect(d.shownFeedback.value?.again).toEqual(first.m)

    // The next connection's welcome: the owed action asked again under its own opId; the owner's repeat confirms it.
    wire = f.wire
    seq.value++
    await flush()
    expect(f.decided).toHaveLength(2)
    expect(f.decided[1]!.m).toEqual(first.m)
    f.decided[1]!.settle({ t: 'decide_ok', reqId: 'x', ref: q, receipt: 'q-1', repeat: true })
    await flush()
    expect(d.shownFeedback.value?.again).toBeUndefined()
    expect(d.shownFeedback.value?.unknown).toBeUndefined()
    expect(d.actions.value.map((e) => [e.ref, e.attempt])).toEqual([[q, first.m.opId]])
  })
})
