import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { computed, ref, watch, type Ref } from 'vue'
import type { BrakemanConfig, BrakemanRequest, BrakemanState } from '../utils/brakeman'
import { makeAgain } from '../utils/brakemanCards'
import { ApiError } from './useApi'
import { BrakemanLive, asBrakemanEvent, brakemanLive, keepOpened, sendTwice, useBrakeman, type BrakemanReads } from './useBrakeman'
import { DispatchLive } from './useDispatchLive'
import { requestNumber } from '../utils/brakeman'

// Brakeman's live store and its writes (docs/plans/brakeman.md, PR-6): an id kept across a lost reply, and the `brakeman` events
// folded into the list.

function req(id: string, state: string, over: Partial<BrakemanRequest> = {}): BrakemanRequest {
  return { id, opId: `op-${id}`, kind: 'briefing', createdAt: '2026-11-02T09:00:00Z', state, agent: {}, config: { visibility: {} as never, scope: {}, permissions: { observe: true, draft: true, propose: true }, budget: { turnMinutes: 15, readsPerTurn: 300, turnsPerDay: 40, minutesPerDay: 240 }, rev: 1 }, reads: 0, ...over }
}

/** Reads that answer from `server`, whose requests a test moves; each read resolves when the test lets it (`release`). */
function fakeServer() {
  const server = new Map<string, BrakemanRequest>()
  const held: Array<() => void> = []
  let hold = false
  const later = <T>(v: () => T): Promise<T> => (hold ? new Promise((done) => held.push(() => done(v()))) : Promise.resolve(v()))
  const reads: BrakemanReads = {
    state: () => later((): BrakemanState => ({ config: {} as never, current: [...server.values()].find((r) => r.state === 'running' || r.state === 'starting') ?? null, queue: [...server.values()].filter((r) => r.state === 'queued'), turnsToday: 1 })),
    page: () => later(() => ({ requests: [...server.values()].reverse(), next: '' })),
    request: (id) => later(() => ({ request: server.get(id)!, brief: null })),
  }
  return {
    server,
    reads,
    holdReads: (on: boolean) => (hold = on),
    release: async () => {
      while (held.length) held.shift()!()
      await new Promise((r) => setTimeout(r, 0))
    },
  }
}

const settle = () => new Promise((r) => setTimeout(r, 0))

describe('a write whose reply is lost', () => {
  it('is sent once more with the same body, so the same id', async () => {
    const seen: string[] = []
    const out = await sendTwice(async (b: { opId: string }) => {
      seen.push(b.opId)
      if (seen.length === 1) throw new ApiError(0, 'network', 'Cannot reach the server')
      return { request: req('br-1', 'queued', { opId: b.opId }) }
    }, { opId: 'abc123' })
    expect(seen).toEqual(['abc123', 'abc123'])
    expect(out.request.opId).toBe('abc123')
  })

  it('is not sent a third time, and a refusal is not sent again', async () => {
    let n = 0
    await expect(sendTwice(async () => {
      n++
      throw new ApiError(0, 'network', 'Cannot reach the server')
    }, {})).rejects.toThrow('Cannot reach the server')
    expect(n).toBe(2)
    n = 0
    await expect(sendTwice(async () => {
      n++
      throw new ApiError(409, 'brakeman_off', 'Brakeman is off')
    }, {})).rejects.toThrow('Brakeman is off')
    expect(n).toBe(1)
  })
})

describe('the brakeman events', () => {
  it('move a request on at once, and read the rest of it', async () => {
    const f = fakeServer()
    const live = new BrakemanLive()
    live.attach(f.reads)
    f.server.set('br-1', req('br-1', 'queued'))
    await live.readFirst()
    expect(live.list().map((r) => [r.id, r.state])).toEqual([['br-1', 'queued']])
    f.server.set('br-1', req('br-1', 'running', { sessionId: '0123456789abcdef', runId: 'brakeman-0a1b2c3d' }))
    f.holdReads(true)
    live.event({ id: 'br-1', state: 'running' })
    // The state at once; the session and the run once the read answers.
    expect(live.requests.get('br-1')!.state).toBe('running')
    expect(live.requests.get('br-1')!.sessionId).toBeUndefined()
    await f.release()
    expect(live.requests.get('br-1')!.sessionId).toBe('0123456789abcdef')
    expect(live.state?.current?.id).toBe('br-1')
  })

  it('add a request the page had not read, newest first', async () => {
    const f = fakeServer()
    const live = new BrakemanLive()
    live.attach(f.reads)
    f.server.set('br-1', req('br-1', 'briefed', { hasBrief: true }))
    await live.readFirst()
    f.server.set('br-2', req('br-2', 'queued'))
    live.event({ id: 'br-2', state: 'queued' })
    await settle()
    expect(live.list().map((r) => r.id)).toEqual(['br-2', 'br-1'])
  })

  it('never take a request back: a read sent before the event that moved it is dropped', async () => {
    const f = fakeServer()
    const live = new BrakemanLive()
    live.attach(f.reads)
    f.server.set('br-1', req('br-1', 'running'))
    await live.readFirst()
    // A read goes out while the request runs; the brief is kept and its event arrives before that read answers.
    f.holdReads(true)
    const stale = live.readRequest('br-1')
    f.holdReads(false)
    f.server.set('br-1', req('br-1', 'briefed', { hasBrief: true }))
    live.event({ id: 'br-1', state: 'briefed' })
    await settle()
    expect(live.requests.get('br-1')!.state).toBe('briefed')
    // The old read answers now, with what the server held when it was sent.
    f.server.set('br-1', req('br-1', 'running'))
    await f.release()
    await stale
    expect(live.requests.get('br-1')!.state).toBe('briefed')
  })

  it("never take a draft back: two cards' makes answered out of order leave both made, with their relation", async () => {
    const live = new BrakemanLive()
    const at = '2026-11-02T09:00:01.000Z'
    const a = { item: 6, opId: 'a', state: 'made', key: 'BKC-2', at }
    // The two replies of one briefed request, the later make's first: B made with the edge, then A's, sent while B was pending.
    live.apply(req('br-1', 'briefed', { hasBrief: true, made: [a, { item: 7, opId: 'b', state: 'made', key: 'BKC-3', at }], edges: [{ from: 6, to: 7, op: 'e', state: 'made' }] }))
    live.apply(req('br-1', 'briefed', { hasBrief: true, made: [a, { item: 7, opId: 'b', state: 'pending', at }], edges: [{ from: 6, to: 7, op: 'e', state: 'pending' }] }))
    const r = live.requests.get('br-1')!
    expect(r.made!.find((m) => m.item === 7)).toMatchObject({ state: 'made', key: 'BKC-3' })
    expect(r.edges).toEqual([{ from: 6, to: 7, op: 'e', state: 'made' }])
  })

  it('never take an act back: two reads of a briefed request answered out of order leave its act done, with its receipt', async () => {
    const live = new BrakemanLive()
    const act = { opId: 'a1', operation: 'launch_approved_plan', target: 'ticket:CND-4', op: 'k', at: '2026-11-05T10:00:01.000Z' }
    // The brief was kept while the act was pending; the read that saw it done is answered first, the one sent before it second.
    live.apply(req('br-1', 'briefed', { hasBrief: true, acts: [{ ...act, state: 'done', receipt: '0123456789abcdef', at: '2026-11-05T10:00:02.000Z' }] }))
    live.apply(req('br-1', 'briefed', { hasBrief: true, acts: [{ ...act, state: 'pending' }] }))
    expect(live.requests.get('br-1')!.acts).toEqual([{ ...act, state: 'done', receipt: '0123456789abcdef', at: '2026-11-05T10:00:02.000Z' }])
  })

  it('tell the pages each change, and a snapshot reads what was missed', async () => {
    const f = fakeServer()
    let changes = 0
    const live = new BrakemanLive(() => changes++)
    live.attach(f.reads)
    await live.readFirst()
    const before = changes
    f.server.set('br-1', req('br-1', 'briefed', { hasBrief: true }))
    const roleSeq = live.roleSeq
    live.snapshot()
    await settle()
    await settle()
    expect(live.list().map((r) => r.id)).toEqual(['br-1'])
    expect(changes).toBeGreaterThan(before)
    // The role's events missed meanwhile too: a page showing its log reads it again.
    expect(live.roleSeq).toBe(roleSeq + 1)
  })

  it('take the request in progress off the page once its slot is given up, though its state is the same', async () => {
    // The brief's event is read inside the grace, while the briefed request still holds the slot; the server sends the request's
    // event once more when the slot is given up, and the state read then holds no request in progress.
    const f = fakeServer()
    const live = new BrakemanLive()
    live.attach(f.reads)
    let slot: BrakemanRequest | null = req('br-1', 'running')
    f.reads.state = async () => ({ config: {} as never, current: slot, queue: [], turnsToday: 1 })
    f.server.set('br-1', req('br-1', 'running'))
    await live.readFirst()
    expect(live.state?.current?.id).toBe('br-1')
    slot = req('br-1', 'briefed', { hasBrief: true })
    f.server.set('br-1', slot)
    live.event({ id: 'br-1', state: 'briefed' })
    await settle()
    expect([live.state?.current?.id, live.state?.current?.state]).toEqual(['br-1', 'briefed'])
    slot = null
    live.event({ id: 'br-1', state: 'briefed' })
    await settle()
    expect(live.state?.current).toBeNull()
    expect(live.requests.get('br-1')!.state).toBe('briefed')
  })

  it('are read off the stream only as they are', () => {
    expect(asBrakemanEvent({ id: 'br-3', state: 'running' })).toEqual({ id: 'br-3', state: 'running' })
    expect(asBrakemanEvent({ id: 'br-0', state: 'running' })).toBeNull()
    expect(asBrakemanEvent({ id: 'x', state: 'running' })).toBeNull()
    expect(asBrakemanEvent({ id: 'br-3' })).toBeNull()
    expect(asBrakemanEvent(null)).toBeNull()
    expect(asBrakemanEvent({ id: 'role', state: 'cooling' })).toEqual({ id: 'role', state: 'cooling' })
  })

  it("of the background role move its state at once, read the state again and count the role's changes", async () => {
    let reads = 0
    const live = new BrakemanLive()
    const state = { config: {} as never, current: null, queue: [], turnsToday: 0, role: { state: 'briefing' } }
    live.attach({ state: () => (reads++, Promise.resolve({ ...state, role: { state: 'cooling', coolUntil: '2026-11-05T22:00:00Z' } })), page: () => Promise.resolve({ requests: [], next: '' }), request: () => Promise.reject(new Error('none')) })
    live.state = state
    live.event({ id: 'role', state: 'cooling' })
    expect([live.state?.role?.state, live.roleSeq, live.requests.size]).toEqual(['cooling', 1, 0])
    await Promise.resolve()
    await Promise.resolve()
    expect(reads).toBe(1)
    expect(live.state?.role?.coolUntil).toBe('2026-11-05T22:00:00Z')
  })

  it('say Brakeman is not here on a server without it', async () => {
    const live = new BrakemanLive()
    const no = () => Promise.reject(new ApiError(503, 'store_unavailable', 'Brakeman needs a data directory'))
    live.attach({ state: no, page: no, request: no })
    await live.readFirst()
    expect([live.available, live.problem]).toEqual([false, 'Brakeman needs a data directory'])
  })
})

describe('the pages read', () => {
  /** A server holding br-1 to br-<n> (`add` asks more), paged newest first 100 at a time after an id, as the route answers. */
  function pagedServer(n: number) {
    const server = new Map<string, BrakemanRequest>()
    const add = (to: number) => {
      for (let i = server.size + 1; i <= to; i++) server.set(`br-${i}`, req(`br-${i}`, 'briefed', { hasBrief: true }))
    }
    add(n)
    let down = false
    const reads: BrakemanReads = {
      state: async () => ({ config: {} as never, current: null, queue: [], turnsToday: 0 }),
      page: async (after) => {
        if (down && after) throw new ApiError(0, 'network', 'Cannot reach the server')
        const below = after ? Number(after.slice(3)) : server.size + 1
        const ids = [...server.keys()].map((id) => Number(id.slice(3))).filter((i) => i < below).sort((a, b) => b - a)
        const page = ids.slice(0, 100)
        return { requests: page.map((i) => server.get(`br-${i}`)!), next: ids.length > 100 ? `br-${page.at(-1)}` : '' }
      },
      request: async (id) => ({ request: server.get(id)!, brief: null }),
    }
    return { reads, add, failPagesAfter: (on: boolean) => (down = on) }
  }
  const ids = (live: BrakemanLive) => live.list().map((r) => Number(r.id.slice(3)))
  const range = (from: number, to: number) => Array.from({ length: to - from + 1 }, (_, i) => to - i)

  it('leave no request asked while the stream was down between the first page and those read before', async () => {
    const f = pagedServer(101)
    const live = new BrakemanLive()
    live.attach(f.reads)
    await live.readFirst()
    await live.readMore()
    expect(ids(live)).toEqual(range(1, 101))
    expect(live.next).toBe('')
    // br-102 to br-202 are asked while the stream is down: the first page read again holds br-103 to br-202.
    f.add(202)
    live.snapshot()
    await settle()
    await settle()
    expect(ids(live)).toEqual(range(1, 202))
    expect(live.next).toBe('')
  })

  it('keep the older requests to read once the gap is filled', async () => {
    const f = pagedServer(250)
    const live = new BrakemanLive()
    live.attach(f.reads)
    await live.readFirst()
    await live.readMore()
    expect(live.next).toBe('br-51')
    f.add(400)
    await live.readFirst()
    expect(ids(live)).toEqual(range(51, 400))
    expect(live.next).toBe('br-51')
    await live.readMore()
    expect(ids(live)).toEqual(range(1, 400))
  })

  it('read on from the gap when a page of it fails', async () => {
    const f = pagedServer(101)
    const live = new BrakemanLive()
    live.attach(f.reads)
    await live.readFirst()
    await live.readMore()
    f.add(202)
    f.failPagesAfter(true)
    await live.readFirst()
    expect(live.requests.has('br-102')).toBe(false)
    expect(live.next).toBe('br-103')
    f.failPagesAfter(false)
    await live.readMore()
    expect(ids(live)).toEqual(range(1, 202))
    expect(live.next).toBe('br-3')
    await live.readMore()
    expect(live.next).toBe('')
  })
})

describe('a request a link names', () => {
  /** A server holding br-1 to br-<n>, whose first page is the newest 100 (MaxPage), as the route answers it. */
  function manyServer(n: number, pruned: string[] = []) {
    const server = new Map<string, BrakemanRequest>()
    for (let i = 1; i <= n; i++) server.set(`br-${i}`, req(`br-${i}`, 'briefed', { hasBrief: true }))
    const reads: string[] = []
    const r: BrakemanReads = {
      state: async () => ({ config: {} as never, current: null, queue: [], turnsToday: 0 }),
      page: async () => ({ requests: [...server.values()].reverse().slice(0, 100), next: n > 100 ? `br-${n - 99}` : '' }),
      request: async (id) => {
        reads.push(id)
        if (pruned.includes(id)) throw new ApiError(410, 'pruned', 'pruned')
        const got = server.get(id)
        if (!got) throw new ApiError(404, 'not_found', 'no such request')
        return { request: got, brief: null }
      },
    }
    return { reads: r, asked: reads }
  }

  it('is read by itself when it is older than the first page, and listed with the rest', async () => {
    const f = manyServer(101)
    const live = new BrakemanLive()
    live.attach(f.reads)
    await live.readFirst()
    expect(live.requests.has('br-1')).toBe(false)
    expect(await live.readNamed('br-1')).toBe('')
    expect(live.requests.get('br-1')?.hasBrief).toBe(true)
    expect(live.list().at(-1)!.id).toBe('br-1')
    // One the page holds is not read again.
    expect(await live.readNamed('br-50')).toBe('')
    expect(f.asked).toEqual(['br-1'])
    // The pages after the first still follow on from the first.
    expect(live.next).toBe('br-2')
  })

  it('says why one the server no longer keeps cannot be shown', async () => {
    const f = manyServer(3, ['br-2'])
    const live = new BrakemanLive()
    live.attach(f.reads)
    await live.readFirst()
    live.requests.delete('br-2')
    expect(await live.readNamed('br-2')).toBe('br-2 was pruned: the server keeps the newest 500 requests that are done.')
    expect(await live.readNamed('br-9')).toBe('br-9 is not kept here: the server keeps the newest 500 requests that are done.')
  })
})

describe('a new token', () => {
  /** Reads that answer with what `server` holds as each is sent (another server's, once the test swaps it), held while `hold`. */
  function swappable() {
    let server = new Map<string, BrakemanRequest>()
    const held: Array<() => void> = []
    let hold = false
    const answer = <T>(v: T): Promise<T> => (hold ? new Promise((done) => held.push(() => done(v))) : Promise.resolve(v))
    const reads: BrakemanReads = {
      state: () => answer<BrakemanState>({ config: {} as never, current: [...server.values()].find((r) => r.state === 'running') ?? null, queue: [], turnsToday: 1 }),
      page: () => answer({ requests: [...server.values()].reverse(), next: '' }),
      request: (id) => answer({ request: server.get(id)!, brief: null }),
    }
    return {
      reads,
      swap: (next: Map<string, BrakemanRequest>) => (server = next),
      holdReads: (on: boolean) => (hold = on),
      release: async () => {
        while (held.length) held.shift()!()
        await settle()
      },
    }
  }

  it("drops what the store held, so another server's br-1 running is not taken for a regression of the briefed one", async () => {
    const f = swappable()
    f.swap(new Map([['br-1', req('br-1', 'briefed', { hasBrief: true, runId: 'run-before' })]]))
    const live = new BrakemanLive()
    live.attach(f.reads)
    await live.readFirst()
    expect(live.requests.get('br-1')!.state).toBe('briefed')
    // The server is started again on fresh data with a fresh token: its br-1 is a new request, running.
    f.swap(new Map([['br-1', req('br-1', 'running', { runId: 'run-after', sessionId: 'abcdef0123456789' })]]))
    live.reset()
    expect(live.list()).toEqual([])
    expect(live.state).toBeNull()
    expect(live.available).toBeUndefined()
    await live.readFirst()
    expect(live.requests.get('br-1')).toMatchObject({ state: 'running', runId: 'run-after' })
    expect(live.state!.current!.runId).toBe('run-after')
  })

  it('drops the replies to reads sent with the token before, however they arrive', async () => {
    const f = swappable()
    f.swap(new Map([['br-1', req('br-1', 'briefed', { hasBrief: true, runId: 'run-before' })]]))
    const live = new BrakemanLive()
    live.attach(f.reads)
    // Every kind of read goes out to the server before, and answers only after the reset.
    f.holdReads(true)
    const first = live.readFirst()
    const one = live.readRequest('br-1')
    const named = live.readNamed('br-1')
    const state = live.readState()
    const at = live.generation
    f.swap(new Map([['br-1', req('br-1', 'running', { runId: 'run-after' })]]))
    live.reset()
    f.holdReads(false)
    // The new token's reads answer first.
    await live.readFirst()
    expect(live.requests.get('br-1')).toMatchObject({ state: 'running', runId: 'run-after' })
    // The old server's answers arrive now, and none of them is taken.
    f.holdReads(true)
    await f.release()
    await Promise.all([first, one, named, state])
    expect(live.requests.get('br-1')).toMatchObject({ state: 'running', runId: 'run-after' })
    expect(live.state!.current!.runId).toBe('run-after')
    // A write answered for the token before is not taken either.
    live.apply(req('br-1', 'briefed', { runId: 'run-before' }), at)
    expect(live.requests.get('br-1')!.runId).toBe('run-after')
    // The state's reads after the reset are the new token's: a read asked now is sent, not folded into the old one.
    f.holdReads(false)
    f.swap(new Map([['br-1', req('br-1', 'briefed', { hasBrief: true, runId: 'run-after' })]]))
    await live.readState()
    expect(live.state!.current).toBeNull()
  })
})

/**
 * One browser's storage, which every tab reads and writes; `touched` is called before each call to it (a test runs another tab's
 * write there). Returns the marks of the briefs it keeps opened, newest first, as a page read again finds them.
 */
function browserStorage(touched: () => void = () => {}) {
  const markNumber = (m: string) => requestNumber(m.split('@')[0]!)
  const kept = new Map<string, string>()
  const prefix = 'conductor.brakeman.opened/'
  vi.stubGlobal('localStorage', {
    get length() {
      touched()
      return kept.size
    },
    key: (i: number) => (touched(), [...kept.keys()][i] ?? null),
    getItem: (k: string) => (touched(), kept.get(k) ?? null),
    setItem: (k: string, v: string) => (touched(), void kept.set(k, v)),
    removeItem: (k: string) => (touched(), void kept.delete(k)),
  })
  return {
    kept,
    opened: () => [...kept.keys()].filter((k) => k.startsWith(prefix)).map((k) => k.slice(prefix.length)).sort((a, b) => markNumber(b) - markNumber(a) || (b < a ? -1 : b > a ? 1 : 0)),
  }
}

describe('the briefs a browser opened', () => {
  afterEach(() => void vi.unstubAllGlobals())
  const at = '2026-11-02T09:00:00Z'

  it("are kept with those another tab opened, wherever the other tab's write falls among this one's", () => {
    // How many calls to the storage one tab's keep makes, with nothing else written.
    let calls = 0
    browserStorage(() => calls++)
    keepOpened([`br-1@${at}`])
    expect(calls).toBeGreaterThan(0)
    // Tab A opens br-1 and tab B br-2, B's whole write falling before each of A's calls in turn (and after the last).
    for (let k = 0; k <= calls; k++) {
      let n = 0
      let tabB = false
      const other = () => {
        if (tabB || n++ !== k) return
        tabB = true
        keepOpened([`br-2@${at}`])
      }
      const s = browserStorage(other)
      keepOpened([`br-1@${at}`])
      other()
      // Read again (the page reloaded), neither is new.
      expect(s.opened(), `tab B's write before A's call ${k}`).toEqual([`br-2@${at}`, `br-1@${at}`])
    }
  })

  it('are the newest 500, newest first', () => {
    const s = browserStorage()
    keepOpened(Array.from({ length: 499 }, (_, i) => `br-${i + 1}@${at}`))
    const kept = keepOpened([`br-600@${at}`, `br-700@${at}`])
    expect(kept).toHaveLength(500)
    expect(kept.slice(0, 3)).toEqual([`br-700@${at}`, `br-600@${at}`, `br-499@${at}`])
    expect(kept).not.toContain(`br-1@${at}`)
    expect(s.opened()).toEqual(kept)
  })

  it('drop the one list kept before marks named their request, whose br-3 was any server\'s', () => {
    const s = browserStorage()
    s.kept.set('conductor.brakeman.opened', JSON.stringify(['br-3']))
    expect(keepOpened([])).toEqual([])
    expect(s.kept.has('conductor.brakeman.opened')).toBe(false)
  })
})

describe('the makes kept, across a new token', () => {
  // useBrakeman reads Nuxt's auto-imports (useApi, useState, computed, watch) as globals: each is stubbed, and the make route is the
  // test's, answering as each case says.
  type Answer = () => Promise<unknown>
  let states: Map<string, Ref<unknown>>
  let answers: Answer[]
  let sent: Array<{ path: string; body: unknown }>
  beforeEach(() => {
    states = new Map()
    answers = []
    sent = []
    vi.stubGlobal('useState', <T>(key: string, init: () => T) => {
      if (!states.has(key)) states.set(key, ref(init()))
      return states.get(key) as Ref<T>
    })
    vi.stubGlobal('computed', computed)
    vi.stubGlobal('watch', watch)
    vi.stubGlobal('useApi', () => ({
      request: (path: string, opts: { body?: unknown } = {}) => {
        sent.push({ path, body: opts.body })
        return answers.shift()!()
      },
    }))
    brakemanLive().reset()
  })
  afterEach(() => void vi.unstubAllGlobals())

  /** One server's br-3 and another's: the same id, written at other times. */
  const first = req('br-3', 'briefed', { hasBrief: true, createdAt: '2026-11-02T09:00:00.123456789Z' })
  const other = req('br-3', 'briefed', { hasBrief: true, createdAt: '2026-11-03T10:30:00.987654321Z' })
  const failed = () => Promise.reject(new ApiError(500, 'make_failed', 'the backend failed'))
  /** An answer the test gives when it says. */
  function held() {
    let answer!: (v: Answer) => void
    const out = new Promise<unknown>((done, fail) => (answer = (v) => void v().then(done, fail)))
    answers.push(() => out)
    return answer
  }

  it("are the request's they were asked of: another server's br-3 finds none to finish, and the first server's still does", async () => {
    const bk = useBrakeman()
    answers.push(failed)
    await expect(bk.make(first, 1, { edit: { title: 'For the first server' } })).rejects.toThrow('the backend failed')
    expect(bk.keptMake(first, 1)).toMatchObject({ body: { edit: { title: 'For the first server' } } })
    // A new token, another server: its br-3 has a draft at item 1 and no make.
    bk.reset()
    expect(bk.keptMake(other, 1)).toBeUndefined()
    expect(makeAgain(undefined, bk.keptMake(other, 1)).kind).toBe('none')
    expect(() => bk.finishMake(other, 1)).toThrow('This browser holds no make of it to finish.')
    expect(sent).toHaveLength(1)
    // The first server's request, under any token, still finishes its make, as it was sent.
    const kept = bk.keptMake(first, 1)!
    answers.push(() => Promise.resolve({ request: { ...first, made: [{ item: 1, opId: kept.opId, state: 'made', key: 'BKC-1' }] }, made: {} }))
    await bk.finishMake(first, 1)
    expect(sent[1]).toEqual({ path: '/api/brakeman/requests/br-3/items/1/make', body: { opId: kept.opId, edit: { title: 'For the first server' } } })
    expect(bk.keptMake(first, 1)).toBeUndefined()
  })

  it("answered after a reset, are not taken into the new token's store, nor kept for its requests", async () => {
    const bk = useBrakeman()
    const live = brakemanLive()
    // Made: the reply arrives after the reset, once the new server's br-3 is read.
    const made = held()
    const making = bk.make(first, 1, { edit: { title: 'Made late' } })
    bk.reset()
    live.apply(other)
    made(() => Promise.resolve({ request: { ...first, made: [{ item: 1, opId: 'x', state: 'made', key: 'BKC-1' }] }, made: {} }))
    await expect(making).rejects.toThrow('The token changed before the make was answered')
    expect(live.requests.get('br-3')).toEqual(other)
    // Made on the first server, so nothing is left to finish.
    expect(bk.keptMake(first, 1)).toBeUndefined()
    // Failed: the failure arrives after a reset, and the make is the first server's to finish, not the new one's.
    const fail = held()
    const failing = bk.make(first, 2, { edit: { title: 'Failed late' } })
    bk.reset()
    live.apply(other)
    fail(failed)
    await expect(failing).rejects.toThrow('the backend failed')
    expect(bk.keptMake(other, 2)).toBeUndefined()
    expect(bk.keptMake(first, 2)).toMatchObject({ body: { edit: { title: 'Failed late' } } })
    expect(live.requests.get('br-3')).toEqual(other)
  })

  it("answered after a reset, give their card nothing to open: a crew card would open the server before's crew on the new one", async () => {
    const bk = useBrakeman()
    const live = brakemanLive()
    // BrakemanDraftCrew's save: it opens /crews/<id> of the crew the make answers with.
    const opened: string[] = []
    const save = async () => {
      const out = await bk.make(first, 1, { edit: { crew: { name: 'Late crew' } } })
      if (out.crew?.id) opened.push(`/crews/${out.crew.id}`)
    }
    const made = held()
    const saving = save()
    // The token changes while the make is out, the card unmounted with the list, and the new server's br-3 read.
    bk.reset()
    live.apply(other)
    made(() => Promise.resolve({ request: { ...first, made: [{ item: 1, opId: 'x', state: 'made', crew: 'old-crew' }] }, made: {}, crew: { id: 'old-crew', url: '/crews/old-crew' } }))
    await expect(saving).rejects.toThrow('The token changed before the make was answered')
    expect(opened).toEqual([])
    expect(live.requests.get('br-3')).toEqual(other)
    // Under one token, the crew made is the card's to open.
    answers.push(() => Promise.resolve({ request: { ...other, made: [{ item: 1, opId: 'y', state: 'made', crew: 'new-crew' }] }, made: {}, crew: { id: 'new-crew', url: '/crews/new-crew' } }))
    const out = await bk.make(other, 1, { edit: { crew: { name: 'Crew' } } })
    expect(out.crew?.id).toBe('new-crew')
  })

  it('made, name their ticket by its title as Dispatch holds it: read once a token, again after a failed read, never from a make\'s edit', async () => {
    const bk = useBrakeman()
    // A make that answers 200 with the item refused is no ticket: nothing of its edit is kept as a title.
    answers.push(() => Promise.resolve({ request: { ...first, made: [{ item: 1, opId: 'x', state: 'refused', why: 'no project' }] }, made: { state: 'refused' } }))
    await bk.make(first, 1, { edit: { title: 'Never made' } })
    expect(bk.ticketTitle('BKC-1')).toBeUndefined()
    // The ticket's title is Dispatch's, read once however many cards ask.
    let answer: (v: unknown) => void = () => {}
    answers.push(() => new Promise((done) => (answer = done)))
    bk.readTicketTitle('BKC-1')
    bk.readTicketTitle('BKC-1')
    expect(sent.filter((s) => s.path === '/api/dispatch/tickets/BKC-1')).toHaveLength(1)
    expect(bk.ticketTitle('BKC-1')).toBeUndefined()
    answer({ ticket: { key: 'BKC-1', title: 'Made in another tab' } })
    await settle()
    expect(bk.ticketTitle('BKC-1')).toBe('Made in another tab')
    // A read that fails leaves nothing, and the next ask reads again.
    answers.push(() => Promise.reject(new ApiError(0, 'network', 'Cannot reach the server')))
    bk.readTicketTitle('BKC-2')
    await settle()
    expect(bk.ticketTitle('BKC-2')).toBeUndefined()
    answers.push(() => Promise.resolve({ ticket: { key: 'BKC-2', title: 'Read again' } }))
    bk.readTicketTitle('BKC-2')
    await settle()
    expect(bk.ticketTitle('BKC-2')).toBe('Read again')
    // A new token's server has its own BKC-1.
    bk.reset()
    expect(bk.ticketTitle('BKC-1')).toBeUndefined()
  })

  it('made, name their ticket by its title as Dispatch holds it now: read again on its events and on a snapshot, the latest read taken', async () => {
    vi.useFakeTimers()
    try {
      const bk = useBrakeman()
      const live = new DispatchLive(250)
      live.subscribe(bk.ticketTitlesWatch(() => ['BKC-1']))
      const reads = () => sent.filter((s) => s.path === '/api/dispatch/tickets/BKC-1').length
      answers.push(() => Promise.resolve({ ticket: { id: 't1', key: 'BKC-1', title: 'First title' } }))
      await bk.readTicketTitle('BKC-1')
      expect(bk.ticketTitle('BKC-1')).toBe('First title')
      // Renamed in Dispatch: its event, by the ticket's id, reads it again at once; the old title shows until the answer.
      let answer: (v: unknown) => void = () => {}
      answers.push(() => new Promise((done) => (answer = done)))
      live.event({ seq: 1, entity: 'ticket', id: 't1', ticket: 't1', project: 'BKC' })
      expect(reads()).toBe(2)
      expect(bk.ticketTitle('BKC-1')).toBe('First title')
      answer({ ticket: { id: 't1', key: 'BKC-1', title: 'Renamed' } })
      await vi.advanceTimersByTimeAsync(0)
      expect(bk.ticketTitle('BKC-1')).toBe('Renamed')
      // Another ticket's event, or another project's, reads nothing; the collapsed event of its project does, after the window.
      live.event({ seq: 2, entity: 'ticket', id: 't2', ticket: 't2', project: 'BKC' })
      live.event({ seq: 3, entity: 'ticket', id: '', ticket: '', project: 'API' })
      await vi.advanceTimersByTimeAsync(1000)
      expect(reads()).toBe(2)
      answers.push(() => Promise.resolve({ ticket: { id: 't1', key: 'BKC-1', title: 'Renamed in bulk' } }))
      live.event({ seq: 4, entity: 'ticket', id: '', ticket: '', project: 'BKC' })
      await vi.advanceTimersByTimeAsync(250)
      expect([reads(), bk.ticketTitle('BKC-1')]).toEqual([3, 'Renamed in bulk'])
      // A reconnect's snapshot reads it again; a read that fails keeps the title shown and is asked again by the live store.
      answers.push(() => Promise.reject(new ApiError(0, 'network', 'Cannot reach the server')))
      answers.push(() => Promise.resolve({ ticket: { id: 't1', key: 'BKC-1', title: 'Renamed while away' } }))
      live.snapshot()
      await vi.advanceTimersByTimeAsync(0)
      expect([reads(), bk.ticketTitle('BKC-1')]).toEqual([4, 'Renamed in bulk'])
      await vi.advanceTimersByTimeAsync(250)
      expect([reads(), bk.ticketTitle('BKC-1')]).toEqual([5, 'Renamed while away'])
      // A slow read sent before a rename never puts the title back over the read after it.
      let slow: (v: unknown) => void = () => {}
      answers.push(() => new Promise((done) => (slow = done)))
      answers.push(() => Promise.resolve({ ticket: { id: 't1', key: 'BKC-1', title: 'Latest' } }))
      const first = bk.readTicketTitle('BKC-1', true)
      await bk.readTicketTitle('BKC-1', true)
      slow({ ticket: { id: 't1', key: 'BKC-1', title: 'Stale' } })
      await first
      expect(bk.ticketTitle('BKC-1')).toBe('Latest')
    } finally {
      vi.useRealTimers()
    }
  })

  it('made, name their ticket by the title Dispatch holds as a card is drawn again: a rename no watch saw is read by the first read', async () => {
    // The card's first read (BrakemanDraftTicket: useDispatchLive's first, through the live store's ask) reads every title it shows
    // anew: a brief closed, its blocked ticket renamed, the event seen by no card, and opened again shows the rename, not the title kept.
    const bk = useBrakeman()
    const live = new DispatchLive(250)
    const watch = bk.ticketTitlesWatch(() => ['BKC-1'])
    const reads = () => sent.filter((s) => s.path === '/api/dispatch/tickets/BKC-1').length
    let stop = live.subscribe(watch)
    answers.push(() => Promise.resolve({ ticket: { id: 't1', key: 'BKC-1', title: 'First title' } }))
    live.ask(watch, watch.read)
    await settle()
    expect([reads(), bk.ticketTitle('BKC-1')]).toEqual([1, 'First title'])
    // Closed: the rename's event reaches no watch.
    stop()
    live.event({ seq: 1, entity: 'ticket', id: 't1', ticket: 't1', project: 'BKC' })
    expect(reads()).toBe(1)
    // Opened again: the first read reads it anew, the kept title shown until the answer.
    stop = live.subscribe(watch)
    let answer: (v: unknown) => void = () => {}
    answers.push(() => new Promise((done) => (answer = done)))
    live.ask(watch, watch.read)
    expect([reads(), bk.ticketTitle('BKC-1')]).toEqual([2, 'First title'])
    answer({ ticket: { id: 't1', key: 'BKC-1', title: 'Renamed while closed' } })
    await settle()
    expect(bk.ticketTitle('BKC-1')).toBe('Renamed while closed')
    stop()
  })

  it('whose reply was lost, are not asked again after a reset: the retry would go with the new token', async () => {
    const bk = useBrakeman()
    const lost = held()
    const making = bk.make(first, 1, { edit: { title: 'Lost once' } })
    bk.reset()
    lost(() => Promise.reject(new ApiError(0, 'network', 'Cannot reach the server')))
    await expect(making).rejects.toThrow('The token changed before the make was asked again')
    expect(sent).toHaveLength(1)
    expect(bk.keptMake(first, 1)).toMatchObject({ body: { edit: { title: 'Lost once' } } })
    expect(bk.keptMake(other, 1)).toBeUndefined()
  })

  describe('and the briefs opened', () => {
    afterEach(() => void vi.unstubAllGlobals())

    it("are the request's they were opened of: another server's br-3, briefed after a reset, is new", async () => {
      const s = browserStorage()
      const bk = useBrakeman()
      const live = brakemanLive()
      // The first read of this browser: a request with no brief yet, so none is taken as opened.
      live.available = true
      live.apply(req('br-1', 'queued'))
      await settle()
      expect(bk.unopened.value).toEqual([])
      // The first server's br-3 is briefed, new, and opened.
      live.apply(first)
      await settle()
      expect(bk.unopened.value).toEqual(['br-3'])
      bk.markOpened(first)
      expect(bk.unopened.value).toEqual([])
      // A new token, another server: its br-3 runs, then is briefed. Its brief is new, in the badge and the sidebar's count.
      bk.reset()
      live.available = true
      live.apply({ ...other, state: 'running', hasBrief: false })
      await settle()
      expect(bk.unopened.value).toEqual([])
      live.apply(other)
      await settle()
      expect(bk.unopened.value).toEqual(['br-3'])
      // Read again, the browser keeps the first server's br-3 opened, and the other's is new still.
      expect(s.opened()).toEqual([`br-3@${first.createdAt}`])
      bk.markOpened(other)
      expect(bk.unopened.value).toEqual([])
      expect(s.opened()).toEqual([`br-3@${other.createdAt}`, `br-3@${first.createdAt}`])
    })
  })

  describe('and the other writes', () => {
    const lost = () => Promise.reject(new ApiError(0, 'network', 'Cannot reach the server'))

    it("whose reply was lost, are asked again with the token they were sent with, and never with the next one's", async () => {
      const bk = useBrakeman()
      // Under one token a lost reply is asked once more, with the same id.
      answers.push(lost, () => Promise.resolve({ request: { ...first, state: 'cancelled' } }), () => Promise.resolve({ config: {}, current: null, queue: [], turnsToday: 0 }))
      await bk.cancel('br-3')
      await settle()
      expect(sent.map((s) => s.path)).toEqual(['/api/brakeman/requests/br-3/cancel', '/api/brakeman/requests/br-3/cancel', '/api/brakeman'])
      expect(sent[1]).toEqual(sent[0])
      sent.length = 0
      // A cancel of the first server's br-3, its reply lost after a reset: sent again it would cancel the new server's br-3.
      const cancelling = held()
      const cancel = bk.cancel('br-3')
      bk.reset()
      cancelling(lost)
      await expect(cancel).rejects.toThrow('The token changed before the cancel of br-3 was sent again')
      // An ask, likewise: sent again it would start a turn on the new server.
      const asking = held()
      const ask = bk.ask({ kind: 'briefing' })
      bk.reset()
      asking(lost)
      await expect(ask).rejects.toThrow('The token changed before the ask was sent again')
      expect(sent.map((s) => s.path)).toEqual(['/api/brakeman/requests/br-3/cancel', '/api/brakeman/requests'])
    })

    it("a configuration saved, answered after a reset, is not shown over the new server's, though its revision is the same", async () => {
      const bk = useBrakeman()
      const live = brakemanLive()
      const conf = (over: Partial<BrakemanConfig>) => ({ enabled: true, agentId: 'claude', cwd: '/srv', allowConductorTools: false, visibility: {} as never, scope: {}, permissions: { observe: true, draft: true, propose: true }, wake: { mode: 'invoked' }, budget: { turnMinutes: 15, readsPerTurn: 300, turnsPerDay: 40 }, rev: 2, ...over }) as BrakemanConfig
      const saving = held()
      const save = bk.saveConfig(conf({ rev: 1, cwd: '/first' }))
      // A reset, and the new server's configuration read: revision 2 as well.
      bk.reset()
      const second: BrakemanState = { config: conf({ cwd: '/second' }), current: null, queue: [], turnsToday: 0 }
      live.state = second
      saving(() => Promise.resolve({ config: conf({ cwd: '/first' }) }))
      await expect(save).rejects.toThrow('The token changed before the save was answered')
      expect(live.state).toEqual(second)
      expect(bk.state.value?.config.cwd).toBe('/second')
      expect(bk.generation.value).toBe(live.generation)
      // Nothing more was sent for the token before: no read of its state after the save.
      expect(sent).toHaveLength(1)
    })
  })

  describe("and the configuration read for a stale refusal's choice (#707's review)", () => {
    const conf = (over: Partial<BrakemanConfig>) => ({ enabled: true, agentId: 'claude', cwd: '/srv', allowConductorTools: false, visibility: {} as never, scope: {}, permissions: { observe: true, draft: true, propose: true }, wake: { mode: 'invoked' }, budget: { turnMinutes: 15, readsPerTurn: 300, turnsPerDay: 40 }, rev: 2, ...over }) as BrakemanConfig
    const stateOf = (c: BrakemanConfig): BrakemanState => ({ config: c, current: null, queue: [], turnsToday: 0 })

    it("is this server's only: a reset while it is out, or before it is asked, refuses it, and the store keeps the new server's", async () => {
      const bk = useBrakeman()
      const live = brakemanLive()
      const at = bk.generation.value
      const reading = held()
      const read = bk.readConfig(at)
      // A new token while the read is out: the new server's form is its own, whatever this reply says.
      bk.reset()
      const second = stateOf(conf({ rev: 7, cwd: '/second' }))
      live.state = second
      reading(() => Promise.resolve(stateOf(conf({ rev: 5, cwd: '/first' }))))
      await expect(read).rejects.toThrow("The token changed before Brakeman's settings were read")
      expect(live.state).toEqual(second)
      // Asked under the generation before: refused unsent.
      await expect(bk.readConfig(at)).rejects.toThrow("The token changed before Brakeman's settings were read")
      expect(sent).toHaveLength(1)
    })

    it('is a read of its own, answered fresh while another read of the state is out, and a failure is the caller\'s, never swallowed', async () => {
      const bk = useBrakeman()
      const live = brakemanLive()
      // A read of the state out (sent before the save elsewhere): the choice's read is not folded into it.
      const older = held()
      const out = live.readState()
      answers.push(() => Promise.resolve(stateOf(conf({ rev: 9 }))))
      expect((await bk.readConfig(bk.generation.value)).rev).toBe(9)
      expect(bk.state.value?.config.rev).toBe(9)
      // The older read answers after it: the store reads once more, so it ends on what is saved now, never on the older answer.
      answers.push(() => Promise.resolve(stateOf(conf({ rev: 9 }))))
      older(() => Promise.resolve(stateOf(conf({ rev: 8 }))))
      await out
      expect(bk.state.value?.config.rev).toBe(9)
      expect(sent.map((x) => x.path)).toEqual(['/api/brakeman', '/api/brakeman', '/api/brakeman'])
      // A read that fails says so: the choice keeps the form and the refusal.
      answers.push(() => Promise.reject(new ApiError(0, 'network', 'Cannot reach the server')))
      await expect(bk.readConfig(bk.generation.value)).rejects.toThrow('Cannot reach the server')
    })
  })
})
