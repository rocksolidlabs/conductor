import { describe, expect, it } from 'vitest'
import type { CapacityQuery, CapacityView } from '../utils/capacity'
import { CapacityLive, type CapacityReads } from './useCapacity'

// The capacity store (docs/plans/capacity.md, PR-5): one fetch per question at a time, a fetch again on its key's `capacity` event,
// a read never sent while the view's nextReadAfter is ahead of the clock, and each answer the question's that asked it.

function view(agent: string, account: string, over: Partial<CapacityView> = {}): CapacityView {
  return { agent: { id: agent }, model: { chosen: '' }, account: { id: account }, host: { kind: 'native' }, state: 'never_read', windows: [], credits: [], fresh: 'none', ...over }
}

/** A server whose answers the test hands out one by one: every ask waits until the test answers it. */
function fakeServer() {
  const gets: Array<{ q: CapacityQuery; answer: (v: CapacityView) => void; fail: (e: Error) => void }> = []
  const posts: Array<{ q: CapacityQuery; check: boolean; answer: (v: CapacityView) => void; fail: (e: Error) => void }> = []
  const reads: CapacityReads = {
    view: (q) => new Promise((answer, fail) => gets.push({ q, answer, fail })),
    read: (q, check) => new Promise((answer, fail) => posts.push({ q, check, answer, fail })),
  }
  return { gets, posts, reads }
}

const settle = async () => {
  for (let i = 0; i < 5; i++) await new Promise((r) => setTimeout(r, 0))
}

describe('CapacityLive', () => {
  it('fetches a question once at a time, and once more for an event that came while it was out', async () => {
    const srv = fakeServer()
    const live = new CapacityLive()
    live.attach(srv.reads)
    const q = { agent: 'claude' }
    void live.watch(q)
    void live.watch(q)
    expect(srv.gets).toHaveLength(1)
    // The event names the key while the fetch is out: one more fetch, after it, never two at once.
    live.event({ agent: 'claude', account: 'default', state: 'ok', readAt: '2026-11-12T10:00:00Z' })
    live.event({ agent: 'claude', account: 'default', state: 'ok', readAt: '2026-11-12T10:00:01Z' })
    expect(srv.gets).toHaveLength(1)
    srv.gets[0]!.answer(view('claude', 'default'))
    await settle()
    expect(srv.gets).toHaveLength(2)
    srv.gets[1]!.answer(view('claude', 'default', { state: 'ok' }))
    await settle()
    expect(srv.gets).toHaveLength(2)
    expect(live.entry(q).view?.state).toBe('ok')
  })

  it("fetches again on its key's event only: another agent's or another account's moves nothing", async () => {
    const srv = fakeServer()
    const live = new CapacityLive()
    live.attach(srv.reads)
    void live.watch({ agent: 'claude', account: 'a-work' })
    srv.gets[0]!.answer(view('claude', 'a-work'))
    await settle()
    live.event({ agent: 'codex', account: 'a-work', state: 'ok', readAt: 'x' })
    live.event({ agent: 'claude', account: 'default', state: 'ok', readAt: 'x' })
    live.event({ nonsense: true })
    expect(srv.gets).toHaveLength(1)
    live.event({ agent: 'claude', account: 'a-work', state: 'ok', readAt: 'x' })
    expect(srv.gets).toHaveLength(2)
    // A question with no account follows the account its view resolved.
    void live.watch({ agent: 'codex' })
    srv.gets[2]!.answer(view('codex', 'a-codex'))
    await settle()
    live.event({ agent: 'codex', account: 'default', state: 'ok', readAt: 'x' })
    expect(srv.gets).toHaveLength(3)
    live.event({ agent: 'codex', account: 'a-codex', state: 'ok', readAt: 'x' })
    expect(srv.gets).toHaveLength(4)
  })

  it('fetches nothing for a question no page shows, and every shown one when the stream comes back', async () => {
    const srv = fakeServer()
    const live = new CapacityLive()
    live.attach(srv.reads)
    const shown = { agent: 'claude' }
    const gone = { agent: 'codex' }
    void live.watch(shown)
    void live.watch(gone)
    srv.gets[0]!.answer(view('claude', 'default'))
    srv.gets[1]!.answer(view('codex', 'default'))
    await settle()
    live.unwatch(gone)
    expect(live.entry(gone).view).toBeNull()
    live.event({ agent: 'codex', account: 'default', state: 'ok', readAt: 'x' })
    expect(srv.gets).toHaveLength(2)
    live.snapshot()
    expect(srv.gets.map((g) => g.q.agent)).toEqual(['claude', 'codex', 'claude'])
  })

  it("sends no read while nextReadAfter is ahead of the clock, and one once it has passed", async () => {
    const srv = fakeServer()
    let now = Date.parse('2026-11-12T10:00:00Z')
    const live = new CapacityLive(() => now)
    live.attach(srv.reads)
    const q = { agent: 'claude', account: 'a-work' }
    void live.watch(q)
    srv.gets[0]!.answer(view('claude', 'a-work', { state: 'ok', nextReadAfter: '2026-11-12T10:05:00Z' }))
    await settle()
    expect(await live.read(q)).toBe(false)
    expect(srv.posts).toHaveLength(0)
    now = Date.parse('2026-11-12T10:05:00Z')
    const sent = live.read(q, true)
    expect(srv.posts).toHaveLength(1)
    expect(srv.posts[0]).toMatchObject({ q, check: true })
    // One read of a question at a time.
    expect(await live.read(q)).toBe(false)
    expect(live.entry(q).reading).toBe(true)
    srv.posts[0]!.answer(view('claude', 'a-work', { state: 'reading' }))
    await settle()
    // The read's own answer is a plain session's: the question's view is fetched after it, reading nothing.
    expect(srv.gets).toHaveLength(2)
    srv.gets[1]!.answer(view('claude', 'a-work', { state: 'reading' }))
    expect(await sent).toBe(true)
    expect(live.entry(q).reading).toBe(false)
    expect(live.entry(q).view?.state).toBe('reading')
  })

  it("says a read's refusal in the server's words, and shows the view fetched after it", async () => {
    const srv = fakeServer()
    const live = new CapacityLive()
    live.attach(srv.reads)
    const q = { agent: 'claude' }
    void live.watch(q)
    const first = srv.gets[0]!
    void live.read(q, true)
    srv.posts[0]!.fail(new Error('another server over this data directory reads this agent usage'))
    await settle()
    expect(live.entry(q).error).toBe('another server over this data directory reads this agent usage')
    // The fetch after the read was owed behind the first one; the first, answered last and older, never replaces it.
    first.answer(view('claude', 'default', { state: 'never_read' }))
    await settle()
    srv.gets[1]!.answer(view('claude', 'default', { state: 'error' }))
    await settle()
    expect(live.entry(q).view?.state).toBe('error')
  })

  it('forgets what it knew for another token, and drops an answer on its way for the old one', async () => {
    const srv = fakeServer()
    const live = new CapacityLive()
    live.attach(srv.reads)
    const q = { agent: 'claude' }
    void live.watch(q)
    live.forget()
    srv.gets[0]!.answer(view('claude', 'a-secret', { state: 'ok' }))
    await settle()
    expect(live.entry(q).view).toBeNull()
  })
})
