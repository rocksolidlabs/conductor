import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { computed, ref, watch, type Ref } from 'vue'
import type { BrakemanConfig, BrakemanDigest, BrakemanRequest, BrakemanSince, BrakemanState } from '../utils/brakeman'
import { makeAgain } from '../utils/brakemanCards'
import { ApiError } from './useApi'
import { BrakemanLive, MAX_MARKS, MAX_OPENED_EVENT, MAX_UNREAD, asOpenedReply, MAX_PROGRESS_EVENT, SINCE_EVERY_MS, asBrakemanEvent, asBrakemanOpened, asBrakemanProgress, brakemanLive, brakemanStream, keepOpened, listParams, sendTwice, useBrakeman, type BrakemanReads } from './useBrakeman'
import { DispatchLive } from './useDispatchLive'
import { requestNumber, sameConfig } from '../utils/brakeman'
import { StaleRefusal, configForm } from '../utils/settingsSave'

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
      // The older read answers after it (Codex's second round): it is dropped as it comes, asked before the read the store holds, so
      // the store ends on what is saved now with no read after it to repair it, and with one that would fail.
      older(() => Promise.resolve(stateOf(conf({ rev: 8 }))))
      await out
      expect(bk.state.value?.config.rev).toBe(9)
      expect(sent.map((x) => x.path)).toEqual(['/api/brakeman', '/api/brakeman'])
      // A read that fails says so: the choice keeps the form and the refusal.
      answers.push(() => Promise.reject(new ApiError(0, 'network', 'Cannot reach the server')))
      await expect(bk.readConfig(bk.generation.value)).rejects.toThrow('Cannot reach the server')
      expect(bk.state.value?.config.rev).toBe(9)
    })

    it('answers with the newer configuration when a read asked after it was taken first, and a save is never undone by a read asked before it', async () => {
      const bk = useBrakeman()
      const live = brakemanLive()
      answers.push(() => Promise.resolve(stateOf(conf({ rev: 3 }))))
      await live.readState()
      // The choice's read out, then a read of the state asked after it and answered first: the choice is answered with the newer.
      const mine = held()
      const read = bk.readConfig(bk.generation.value)
      answers.push(() => Promise.resolve(stateOf(conf({ rev: 5 }))))
      await live.readState()
      mine(() => Promise.resolve(stateOf(conf({ rev: 4 }))))
      expect((await read).rev).toBe(5)
      expect(bk.state.value?.config.rev).toBe(5)
      // A read of the state out, then a save: the read answers after the save, with what was saved before it, and is dropped.
      const before = held()
      const out = live.readState()
      const saving = held()
      const save = bk.saveConfig(conf({ rev: 5, cwd: '/saved' }))
      // The read the save asks after it fails: nothing repairs the store but the order its replies are taken in.
      answers.push(() => Promise.reject(new ApiError(0, 'network', 'Cannot reach the server')))
      saving(() => Promise.resolve({ config: conf({ rev: 6, cwd: '/saved' }) }))
      await save
      expect(bk.state.value?.config).toMatchObject({ rev: 6, cwd: '/saved' })
      before(() => Promise.resolve(stateOf(conf({ rev: 5 }))))
      await out
      expect(bk.state.value?.config).toMatchObject({ rev: 6, cwd: '/saved' })
    })
  })

  // Settings' Brakeman form (utils/settingsSave.ts, the one rule) on this store: a reply is applied only when its revision is newer
  // than the form's, and only over the fields not edited since its operation began; the watcher is off while one is out. The two
  // sequences of Codex's third round, each of which ended with the form wrong and Save not offered.
  describe("and Settings' form, answered by revision (Codex's third round)", () => {
    const conf = (reads: number, rev: number, over: Partial<BrakemanConfig> = {}) => ({ enabled: true, agentId: 'claude', cwd: '/srv', allowConductorTools: false, visibility: {} as never, scope: {}, permissions: { observe: true, draft: true, propose: true }, wake: { mode: 'invoked' }, budget: { turnMinutes: 15, readsPerTurn: reads, turnsPerDay: 40 }, rev, ...over }) as BrakemanConfig
    const stateOf = (c: BrakemanConfig): BrakemanState => ({ config: c, current: null, queue: [], turnsToday: 0 })
    const copy = <T>(v: T) => JSON.parse(JSON.stringify(v)) as T
    const unreachable = () => Promise.reject(new ApiError(0, 'network', 'Cannot reach the server'))
    let stops: Array<() => void> = []
    afterEach(() => {
      for (const stop of stops) stop()
      stops = []
    })
    /** The card's form on the store, its watcher run at once on every change (a sync flush: the harshest order). */
    function formOn(bk: ReturnType<typeof useBrakeman>) {
      const ctl = configForm<BrakemanConfig>({
        generation: () => bk.generation.value,
        saved: () => bk.state.value?.config ?? null,
        save: (c) => bk.saveConfig(c),
        read: (gen) => bk.readConfig(gen),
        isStale: (e) => e instanceof ApiError && e.code === 'stale_revision',
        current: (f) => (f ? copy(f) : null),
        same: sameConfig,
        shown: () => {},
      })
      stops.push(watch(() => [bk.generation.value, bk.state.value?.config?.rev, ctl.busy.value], () => void ctl.follow(), { immediate: true, flush: 'sync' }))
      return ctl
    }

    it('save 400, 300 typed back while it is out, an old read answers 300, the PUT completes: the store holds 400 and Save is offered', async () => {
      const bk = useBrakeman()
      const live = brakemanLive()
      answers.push(() => Promise.resolve(stateOf(conf(300, 2))))
      await live.readState()
      const ctl = formOn(bk)
      expect(ctl.form.value).toMatchObject({ rev: 2, budget: { readsPerTurn: 300 } })
      ctl.form.value!.budget.readsPerTurn = 400
      const put = held()
      const saving = ctl.save(copy(ctl.form.value!), bk.generation.value)
      // A read of the state that left after the PUT, before the server wrote it, and answers first with what was saved before.
      const get = held()
      const reading = live.readState()
      ctl.form.value!.budget.readsPerTurn = 300
      get(() => Promise.resolve(stateOf(conf(300, 2))))
      await reading
      // The PUT completes; the read the save asks after it fails, so nothing repairs the store but the revisions compared.
      answers.push(unreachable)
      put(() => Promise.resolve({ config: conf(400, 3) }))
      await saving
      await settle()
      expect(bk.state.value?.config).toMatchObject({ rev: 3, budget: { readsPerTurn: 400 } })
      // The 300 typed while the save was out is the person's, against revision 3: the form differs from what is saved, Save offered.
      expect(ctl.form.value).toMatchObject({ rev: 3, budget: { readsPerTurn: 300 } })
      expect(ctl.dirty.value).toBe(true)
      expect(ctl.edited.value).toBe(true)

      // The same order with nothing typed meanwhile: the form shows the 400 saved, and nothing is left to save.
      ctl.form.value!.budget.readsPerTurn = 400
      expect(ctl.dirty.value).toBe(false)
      ctl.form.value!.budget.readsPerTurn = 500
      const put2 = held()
      const saving2 = ctl.save(copy(ctl.form.value!), bk.generation.value)
      const get2 = held()
      const reading2 = live.readState()
      get2(() => Promise.resolve(stateOf(conf(400, 3))))
      await reading2
      answers.push(unreachable)
      put2(() => Promise.resolve({ config: conf(500, 4) }))
      await saving2
      await settle()
      expect(bk.state.value?.config).toMatchObject({ rev: 4, budget: { readsPerTurn: 500 } })
      expect(ctl.form.value).toMatchObject({ rev: 4, budget: { readsPerTurn: 500 } })
      expect(ctl.dirty.value).toBe(false)
      expect(sent.filter((x) => x.path === '/api/brakeman/config').map((x) => (x.body as { rev: number }).rev)).toEqual([2, 3])
    })

    it("Keep mine: read 300, 400 typed, 500 saved elsewhere, refused stale, 300 typed back, Keep mine: the form keeps 300 and the next Save replaces the other's", async () => {
      const bk = useBrakeman()
      const live = brakemanLive()
      answers.push(() => Promise.resolve(stateOf(conf(300, 2))))
      await live.readState()
      const ctl = formOn(bk)
      ctl.form.value!.budget.readsPerTurn = 400
      answers.push(() => Promise.reject(new ApiError(409, 'stale_revision', 'Saved elsewhere meanwhile.')))
      await expect(ctl.save(copy(ctl.form.value!), bk.generation.value)).rejects.toBeInstanceOf(StaleRefusal)
      expect(ctl.stale.value).toBe(true)
      ctl.form.value!.budget.readsPerTurn = 300
      expect(ctl.edited.value).toBe(false)
      // Keep mine: its read publishes the other writer's 500 in the store before it resolves; the watcher, off, keeps the form.
      answers.push(() => Promise.resolve(stateOf(conf(500, 3))))
      expect(await ctl.keepMine()).toBe(true)
      await settle()
      expect(bk.state.value?.config).toMatchObject({ rev: 3, budget: { readsPerTurn: 500 } })
      expect(ctl.form.value).toMatchObject({ rev: 3, budget: { readsPerTurn: 300 } })
      expect(ctl.stale.value).toBe(false)
      expect(ctl.dirty.value).toBe(true)
      // The next Save sends the 300 kept, at the revision read, and replaces the other change.
      answers.push(() => Promise.resolve({ config: conf(300, 4) }))
      answers.push(() => Promise.resolve(stateOf(conf(300, 4))))
      await ctl.save(copy(ctl.form.value!), bk.generation.value)
      await settle()
      expect(sent.at(-2)).toMatchObject({ path: '/api/brakeman/config', body: { rev: 3, config: { budget: { readsPerTurn: 300 } } } })
      expect(ctl.form.value).toMatchObject({ rev: 4, budget: { readsPerTurn: 300 } })
      expect(ctl.dirty.value).toBe(false)
    })

    it('Read again: what is saved now replaces the edits made before the choice, but not a field typed while its read is out', async () => {
      const bk = useBrakeman()
      const live = brakemanLive()
      answers.push(() => Promise.resolve(stateOf(conf(300, 2))))
      await live.readState()
      const ctl = formOn(bk)
      ctl.form.value!.budget.readsPerTurn = 400
      answers.push(() => Promise.reject(new ApiError(409, 'stale_revision', 'Saved elsewhere meanwhile.')))
      await expect(ctl.save(copy(ctl.form.value!), bk.generation.value)).rejects.toBeInstanceOf(StaleRefusal)
      const read = held()
      const choice = ctl.readAgain()
      ctl.form.value!.cwd = '/typed'
      read(() => Promise.resolve(stateOf(conf(500, 3, { cwd: '/theirs' }))))
      expect(await choice).toBe(true)
      expect(ctl.form.value).toMatchObject({ rev: 3, cwd: '/typed', budget: { readsPerTurn: 500 } })
      expect(ctl.stale.value).toBe(false)
      expect(ctl.dirty.value).toBe(true)
      // A read that fails says so, and leaves the form and the refusal as they are; the watcher, back on, does not set the form.
      ctl.form.value!.cwd = '/theirs'
      ctl.form.value!.budget.readsPerTurn = 600
      answers.push(() => Promise.reject(new ApiError(409, 'stale_revision', 'Saved elsewhere meanwhile.')))
      await expect(ctl.save(copy(ctl.form.value!), bk.generation.value)).rejects.toBeInstanceOf(StaleRefusal)
      ctl.form.value!.budget.readsPerTurn = 500
      answers.push(unreachable)
      expect(await ctl.keepMine()).toBe(false)
      answers.push(() => Promise.resolve(stateOf(conf(700, 4, { cwd: '/theirs' }))))
      await live.readState()
      expect(ctl.staleError.value).toBe('Not read: Cannot reach the server')
      expect(ctl.stale.value).toBe(true)
      expect(ctl.form.value).toMatchObject({ rev: 3, budget: { readsPerTurn: 500 } })
    })
  })
})

describe("a turn's progress", () => {
  const p = (reads: number, over: Record<string, unknown> = {}) => ({ id: 'br-15', reads, readsPerTurn: 300, startedAt: '2026-11-17T05:08:00Z', deadline: '2026-11-17T05:23:00Z', reading: { what: 'task', ref: 'task:fix-login-1a2b3c4d/deploy' }, ...over })

  it('is read off the stream only as it is, and refused whole past its bound', () => {
    expect(asBrakemanProgress(p(41))).toEqual(p(41))
    expect(asBrakemanProgress({ id: 'br-15', reads: 0, readsPerTurn: 300 })).toEqual({ id: 'br-15', reads: 0, readsPerTurn: 300 })
    expect(asBrakemanProgress(p(41, { reading: { what: 'changes' } }))).toEqual(p(41, { reading: { what: 'changes' } }))
    for (const bad of [null, [], 'br-15', p(41, { id: 'br-0' }), p(-1), p(1.5), p(41, { readsPerTurn: '300' }), p(41, { deadline: 7 }), p(41, { reading: { what: 'w'.repeat(17) } }), p(41, { reading: { ref: 'run:x' } }), p(41, { reading: null })]) {
      expect(asBrakemanProgress(bad)).toBeNull()
    }
    // At the bound it is taken; one byte past it, refused (the server leaves the ref out before it would pass it).
    const base = JSON.stringify(p(41, { reading: { what: 'log', ref: '' } })).length
    const at = p(41, { reading: { what: 'log', ref: 'r'.repeat(MAX_PROGRESS_EVENT - base) } })
    expect(JSON.stringify(at).length).toBe(MAX_PROGRESS_EVENT)
    expect(asBrakemanProgress(at)).not.toBeNull()
    expect(asBrakemanProgress(p(41, { reading: { what: 'log', ref: 'r'.repeat(MAX_PROGRESS_EVENT - base + 1) } }))).toBeNull()
  })

  it('keeps the read with the most reads, from the state or the events, whichever comes first', async () => {
    const live = new BrakemanLive()
    live.apply(req('br-15', 'running'))
    live.progressEvent(asBrakemanProgress(p(41))!)
    // A state read sent before the event, answered after it: its fewer reads are older.
    live.takeState({ config: {} as never, current: req('br-15', 'running'), queue: [], turnsToday: 1, progress: p(40) })
    expect(live.progress['br-15']!.reads).toBe(41)
    live.takeState({ config: {} as never, current: req('br-15', 'running'), queue: [], turnsToday: 1, progress: p(42, { reading: undefined }) })
    expect(live.progress['br-15']).toEqual(p(42, { reading: undefined }))
    live.progressEvent(asBrakemanProgress(p(42, { reading: { what: 'runs' } }))!)
    expect(live.progress['br-15']!.reading).toEqual({ what: 'runs' })
  })

  it('is dropped once its request closes, by its event or a read, and an event after it is not taken', async () => {
    const live = new BrakemanLive()
    live.apply(req('br-15', 'running'))
    live.progressEvent(asBrakemanProgress(p(41))!)
    live.event({ id: 'br-15', state: 'cancelled' })
    expect(live.progress).toEqual({})
    live.progressEvent(asBrakemanProgress(p(42))!)
    expect(live.progress).toEqual({})
    // Closed by a read (the brief's request read again).
    live.apply(req('br-16', 'running'))
    live.progressEvent(asBrakemanProgress(p(3, { id: 'br-16' }))!)
    live.apply(req('br-16', 'briefed', { hasBrief: true }))
    expect(live.progress).toEqual({})
    // An event of a request closed before the page read it drops what it held.
    live.progressEvent(asBrakemanProgress(p(5, { id: 'br-17' }))!)
    live.event({ id: 'br-17', state: 'failed' })
    expect(live.progress).toEqual({})
  })

  it('is dropped with the rest at a new token', () => {
    const live = new BrakemanLive()
    live.progressEvent(asBrakemanProgress(p(41))!)
    live.reset()
    expect(live.progress).toEqual({})
  })
})

describe('the opened event', () => {
  it('is `{}` within its bound, and counted', () => {
    expect(asBrakemanOpened({})).toEqual({})
    expect(asBrakemanOpened(null)).toBeNull()
    expect(asBrakemanOpened([])).toBeNull()
    expect(asBrakemanOpened({ who: 'x'.repeat(MAX_OPENED_EVENT) })).toBeNull()
    const live = new BrakemanLive()
    live.openedEvent()
    expect(live.openedSeq).toBe(1)
  })
})

describe("a brief's digest in the live store", () => {
  const digest: BrakemanDigest = { schema: 1, title: 'Build is out of attempts', reading: 'Three failures on one test.', counts: { decisions: 1, facts: 3 }, about: { runs: ['fix-login-1a2b3c4d'] } }

  it("is kept when the request is read again with none, as each brakeman event reads it, and a list's match is not kept", async () => {
    const f = fakeServer()
    const live = new BrakemanLive()
    live.attach(f.reads)
    f.server.set('br-14', req('br-14', 'briefed', { hasBrief: true }))
    live.apply({ ...req('br-14', 'briefed', { hasBrief: true }), digest, match: 'cited' })
    expect(live.requests.get('br-14')).not.toHaveProperty('match')
    live.event({ id: 'br-14', state: 'briefed' })
    await settle()
    expect(live.requests.get('br-14')!.digest).toEqual(digest)
    // A read that gives a digest gives the store its own.
    const newer = { ...digest, title: 'Another title' }
    f.reads.request = async (id) => ({ request: f.server.get(id)!, brief: null, digest: newer })
    await live.readRequest('br-14')
    expect(live.requests.get('br-14')!.digest).toEqual(newer)
    // A request a link names, read by itself, comes with its digest too.
    f.server.set('br-9', req('br-9', 'briefed', { hasBrief: true }))
    expect(await live.readNamed('br-9')).toBe('')
    expect(live.requests.get('br-9')!.digest).toEqual(newer)
  })
})

describe('the brakeman events seen', () => {
  it("move their sequence on each, the role's and a stream's start again too", () => {
    const live = new BrakemanLive()
    live.event({ id: 'br-1', state: 'queued' })
    expect(live.lastEvent).toEqual({ id: 'br-1', state: 'queued', seq: 1 })
    live.event({ id: 'br-1', state: 'queued' })
    expect(live.lastEvent.seq).toBe(2)
    live.event({ id: 'role', state: 'cooling' })
    expect(live.lastEvent).toEqual({ id: 'role', state: 'cooling', seq: 3 })
    live.snapshot()
    expect(live.lastEvent).toEqual({ id: '', state: 'snapshot', seq: 4 })
  })
})

describe('what changed since the last briefing', () => {
  const answer: BrakemanSince = { since: { from: 'briefing', briefing: 'br-12', at: '2026-11-17T02:55:00Z' }, runsEnded: { count: 1, refs: ['run:a'] }, tasksFailed: { count: 0, refs: [] }, ticketsReady: { count: 0, refs: [] }, questionsOpen: { count: 0, refs: [] }, truncated: false }
  afterEach(() => void vi.useRealTimers())

  function sinceServer() {
    let clock = 1_000_000
    const sent: Array<(v: BrakemanSince | null) => void> = []
    const live = new BrakemanLive()
    live.now = () => clock
    live.attach({ state: () => Promise.reject(new Error('none')), page: () => Promise.reject(new Error('none')), request: () => Promise.reject(new Error('none')), since: () => new Promise((done) => sent.push(done)) })
    return { live, sent, tick: (ms: number) => ((clock += ms), vi.advanceTimersByTime(ms)) }
  }

  it('is read when a page follows it, and on events only while one does', async () => {
    vi.useFakeTimers()
    const { live, sent, tick } = sinceServer()
    live.nudgeSince()
    expect(sent).toHaveLength(0)
    const unfollow = live.followSince()
    expect(sent).toHaveLength(1)
    sent[0]!(answer)
    await Promise.resolve()
    expect(live.sinceNow).toEqual(answer)
    unfollow()
    tick(SINCE_EVERY_MS)
    live.event({ id: 'br-13', state: 'briefed' })
    expect(sent).toHaveLength(1)
  })

  it('is read at most once every five seconds, one read at a time, and what was asked meanwhile is one read after', async () => {
    vi.useFakeTimers()
    const { live, sent, tick } = sinceServer()
    live.followSince()
    // Events while the first read is out: one read after it, not one each.
    live.nudgeSince()
    live.nudgeSince()
    expect(sent).toHaveLength(1)
    sent[0]!(answer)
    await Promise.resolve()
    await Promise.resolve()
    // The read after waits out the five seconds from the first one's start.
    expect(sent).toHaveLength(1)
    tick(SINCE_EVERY_MS - 1)
    expect(sent).toHaveLength(1)
    tick(1)
    expect(sent).toHaveLength(2)
    sent[1]!(null)
    await Promise.resolve()
    expect(live.sinceNow).toBeNull()
    // More events inside the five seconds: one read when they end.
    live.nudgeSince()
    live.nudgeSince()
    tick(SINCE_EVERY_MS)
    expect(sent).toHaveLength(3)
  })

  it("is dropped at a new token, and a read sent before it is not taken", async () => {
    vi.useFakeTimers()
    const { live, sent } = sinceServer()
    live.followSince()
    live.reset()
    sent[0]!(answer)
    await Promise.resolve()
    expect(live.sinceNow).toBeNull()
  })
})

describe('the list route', () => {
  it('is asked each filter once, the kinds as one list', () => {
    expect(listParams({})).toEqual({})
    expect(listParams({ kind: ['briefing', 'wake', 'briefing'], has: 'decision', about: 'run:a', q: 'cache', after: 'br-9', limit: 20 })).toEqual({ after: 'br-9', limit: '20', kind: 'briefing,wake', has: 'decision', about: 'run:a', q: 'cache' })
  })

  describe('through the composable', () => {
    let states: Map<string, Ref<unknown>>
    let answers: Array<(path: string, query?: Record<string, string>) => Promise<unknown>>
    let sent: Array<{ path: string; query?: Record<string, string> }>
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
        request: (path: string, opts: { query?: Record<string, string> } = {}) => {
          sent.push({ path, query: opts.query })
          return answers.shift()!(path, opts.query)
        },
      }))
      brakemanLive().reset()
    })
    afterEach(() => void vi.unstubAllGlobals())
    const older = () => Promise.reject(new ApiError(400, 'invalid_request', 'this route takes no parameter "kind"'))
    const digest: BrakemanDigest = { schema: 1, title: 'A title', reading: '', counts: {}, about: {} }

    it("answers the server's filtered rows, each applied into the live store with its digest", async () => {
      const bk = useBrakeman()
      answers.push(() => Promise.resolve({ requests: [{ ...req('br-14', 'briefed', { hasBrief: true }), digest, match: 'subject' }], next: '' }))
      const out = await bk.list({ about: 'run:a', has: 'brief' })
      expect(out).toMatchObject({ filtered: true, next: '', requests: [{ id: 'br-14', match: 'subject' }] })
      expect(sent).toEqual([{ path: '/api/brakeman/requests', query: { about: 'run:a', has: 'brief' } }])
      expect(brakemanLive().requests.get('br-14')!.digest).toEqual(digest)
    })

    it('on an older server, answers no rows for a subject, and the plain page for another filter', async () => {
      const bk = useBrakeman()
      answers.push(older)
      expect(await bk.list({ about: 'run:a' })).toEqual({ requests: [], next: '', filtered: false })
      expect(sent).toHaveLength(1)
      sent.length = 0
      answers.push(older, () => Promise.resolve({ requests: [req('br-3', 'briefed', { hasBrief: true })], next: 'br-3' }))
      const out = await bk.list({ kind: ['briefing'], q: 'cache', after: 'br-9', limit: 1 })
      expect(out).toMatchObject({ filtered: false, next: 'br-3', requests: [{ id: 'br-3' }] })
      expect(sent.map((x) => x.query)).toEqual([{ after: 'br-9', limit: '1', kind: 'briefing', q: 'cache' }, { after: 'br-9', limit: '1' }])
    })

    it("leaves a newer server's refusal, and any other, to the caller", async () => {
      const bk = useBrakeman()
      answers.push(() => Promise.reject(new ApiError(400, 'invalid_request', 'q: a line break')))
      await expect(bk.list({ q: 'a' })).rejects.toThrow('q: a line break')
      answers.push(older)
      await expect(bk.list({})).rejects.toThrow('this route takes no parameter')
    })

    it("is not taken into the store when a new token came before its answer", async () => {
      const bk = useBrakeman()
      let answer!: (v: unknown) => void
      answers.push(() => new Promise((done) => (answer = done)))
      const out = bk.list({ has: 'brief' })
      bk.reset()
      answer({ requests: [req('br-14', 'briefed', { hasBrief: true })], next: '' })
      await expect(out).rejects.toThrow('The token changed before the list was answered')
      expect(brakemanLive().requests.size).toBe(0)
    })

    it('reads the changes since a briefing, or none where the server has no such route', async () => {
      const bk = useBrakeman()
      answers.push(() => Promise.resolve({ since: { from: 'day', at: '2026-11-16T09:00:00Z' } }))
      expect(await bk.since()).toMatchObject({ since: { from: 'day' } })
      answers.push(() => Promise.reject(new ApiError(404, 'not_found', 'no such route')))
      expect(await bk.since('2026-11-17T00:00:00Z')).toBeNull()
      expect(sent.map((x) => x.query)).toEqual([undefined, { since: '2026-11-17T00:00:00Z' }])
      answers.push(() => Promise.reject(new ApiError(403, 'forbidden', 'not yours')))
      await expect(bk.since()).rejects.toThrow('not yours')
    })
  })
})

describe('the unread count, kept by the server', () => {
  /** A store whose server keeps opened marks: each read and mark answered when the test says, or at once. */
  function openedServer() {
    const sent: Array<string[] | undefined> = []
    let unread = ['br-3', 'br-2']
    const held: Array<() => void> = []
    let hold = false
    let answer: (ids?: string[]) => Promise<{ unread: string[]; begun: string } | null> = async (ids) => {
      if (ids) unread = unread.filter((id) => !ids.includes(id))
      return { unread, begun: '2026-11-17T08:00:00Z' }
    }
    const live = new BrakemanLive()
    live.attach({
      state: async () => ({ config: {} as never, current: null, queue: [], turnsToday: 0 }),
      page: async () => ({ requests: [req('br-3', 'briefed', { hasBrief: true }), req('br-2', 'briefed', { hasBrief: true }), req('br-1', 'briefed', { hasBrief: true })], next: '' }),
      request: async (id) => ({ request: req(id, 'briefed', { hasBrief: true }), brief: null }),
      opened: (ids) => {
        sent.push(ids)
        return hold ? new Promise((done) => held.push(() => void answer(ids).then(done))) : answer(ids)
      },
    })
    return {
      live,
      sent,
      setUnread: (u: string[]) => (unread = u),
      answerWith: (f: typeof answer) => (answer = f),
      holdReads: (on: boolean) => (hold = on),
      release: async () => {
        while (held.length) held.shift()!()
        await settle()
        await settle()
      },
    }
  }

  it("is the server's once it answers, and a brief marked opened leaves it at once and is sent", async () => {
    const f = openedServer()
    await f.live.readFirst()
    await settle()
    expect([f.live.openedMode, f.live.unread]).toEqual(['server', ['br-3', 'br-2']])
    f.live.markOpened(['br-3'])
    expect(f.live.unread).toEqual(['br-2'])
    await settle()
    expect(f.sent).toEqual([undefined, ['br-3']])
    // One already opened is not sent again.
    f.live.markOpened(['br-1'])
    expect(f.sent).toHaveLength(2)
  })

  it('is read again when a brief is kept, on the opened event and on a snapshot, one read at a time', async () => {
    const f = openedServer()
    await f.live.readFirst()
    await settle()
    f.sent.length = 0
    f.setUnread(['br-4', 'br-3', 'br-2'])
    f.holdReads(true)
    f.live.event({ id: 'br-4', state: 'briefed' })
    expect(f.sent).toHaveLength(1)
    f.live.openedEvent()
    f.live.openedEvent()
    expect(f.sent).toHaveLength(1)
    await f.release()
    // What was asked while the first read was out is one read after it.
    expect(f.sent).toHaveLength(2)
    await f.release()
    expect(f.live.unread).toEqual(['br-4', 'br-3', 'br-2'])
    expect(f.live.openedSeq).toBe(2)
    f.holdReads(false)
    f.live.event({ id: 'br-4', state: 'running' })
    await settle()
    expect(f.sent).toHaveLength(2)
    f.live.snapshot()
    await settle()
    expect(f.sent).toHaveLength(3)
  })

  it('sends the marks made before it first answered once it does, and never counts them meanwhile', async () => {
    const f = openedServer()
    f.holdReads(true)
    const first = f.live.readFirst()
    await settle()
    expect(f.live.openedMode).toBe('unknown')
    f.live.markOpened(['br-3'])
    expect(f.sent).toEqual([undefined])
    await f.release()
    await first
    expect(f.live.unread).toEqual(['br-2'])
    expect(f.sent).toEqual([undefined, ['br-3']])
  })

  it("is the browser's where the server keeps none: no mark is sent", async () => {
    const f = openedServer()
    f.answerWith(async () => null)
    await f.live.readFirst()
    await settle()
    expect(f.live.openedMode).toBe('local')
    f.live.markOpened(['br-3'])
    f.live.openedEvent()
    await settle()
    expect(f.sent).toEqual([undefined])
  })

  it('reads again when a mark is not taken, and drops what it held at a new token', async () => {
    const f = openedServer()
    await f.live.readFirst()
    await settle()
    f.answerWith(async (ids) => {
      if (ids) throw new ApiError(0, 'network', 'Cannot reach the server')
      return { unread: ['br-3', 'br-2'], begun: '' }
    })
    f.live.markOpened(['br-3'])
    await settle()
    await settle()
    expect(f.sent.slice(1)).toEqual([['br-3'], undefined])
    expect(f.live.unread).toEqual(['br-3', 'br-2'])
    f.live.reset()
    expect([f.live.openedMode, f.live.unread]).toEqual(['unknown', []])
  })

  it('sends many marks at most a hundred at a time, and reads a reply only as it is', () => {
    expect(MAX_MARKS).toBe(100)
    expect(asOpenedReply({ unread: ['br-2', 'br-1'], begun: '2026-11-17T08:00:00Z' })).toEqual({ unread: ['br-2', 'br-1'], begun: '2026-11-17T08:00:00Z' })
    expect(asOpenedReply({ unread: ['x'] })).toBeNull()
    expect(asOpenedReply({ unread: Array.from({ length: MAX_UNREAD + 1 }, (_, i) => `br-${i + 1}`) })).toBeNull()
    expect(asOpenedReply(null)).toBeNull()
  })

  describe('through the composable', () => {
    let states: Map<string, Ref<unknown>>
    let answers: Array<(path: string, opts: { method?: string; body?: unknown }) => Promise<unknown>>
    let sent: Array<{ path: string; method?: string; body?: unknown }>
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
        request: (path: string, opts: { method?: string; body?: unknown } = {}) => {
          sent.push({ path, method: opts.method, body: opts.body })
          return (answers.shift() ?? (() => Promise.reject(new Error(`unasked: ${path}`))))(path, opts)
        },
      }))
      browserStorage()
      // The app's one store keeps the reads of the first composable that attached them (an earlier block's): these tests' own are given it.
      ;(brakemanLive() as unknown as { reads?: unknown }).reads = undefined
      brakemanLive().reset()
    })
    afterEach(() => void vi.unstubAllGlobals())
    const briefed = req('br-3', 'briefed', { hasBrief: true })

    it("counts what the server says is unread, the same on every browser of the credential, and marks through it", async () => {
      const bk = useBrakeman()
      const live = brakemanLive()
      live.available = true
      live.apply(briefed)
      answers.push(() => Promise.resolve({ unread: ['br-3'], begun: '' }))
      await live.readOpened()
      expect(bk.unopened.value).toEqual(['br-3'])
      answers.push(() => Promise.resolve({ unread: [], begun: '' }))
      bk.markOpened(briefed)
      expect(bk.unopened.value).toEqual([])
      await settle()
      expect(sent.map((x) => [x.path, x.method, x.body])).toEqual([
        ['/api/brakeman/opened', undefined, undefined],
        ['/api/brakeman/opened', 'POST', { ids: ['br-3'] }],
      ])
    })

    for (const [what, refusal] of [
      ['an older server, which has no such route', new ApiError(404, 'not_found', 'no such route')],
      ['a read-only store', new ApiError(503, 'brakeman_not_owner', "Another server owns Brakeman's records")],
    ] as const) {
      it(`keeps the browser's own marks on ${what}, and sends none`, async () => {
        const bk = useBrakeman()
        const live = brakemanLive()
        live.available = true
        live.apply(req('br-1', 'queued'))
        await settle()
        live.apply(briefed)
        answers.push(() => Promise.reject(refusal))
        await live.readOpened()
        expect(live.openedMode).toBe('local')
        expect(bk.unopened.value).toEqual(['br-3'])
        bk.markOpened(briefed)
        expect(bk.unopened.value).toEqual([])
        expect(sent).toHaveLength(1)
      })
    }

    it('leaves the count shown when a read fails another way, and reads again on the next event', async () => {
      const bk = useBrakeman()
      const live = brakemanLive()
      live.available = true
      live.apply(req('br-1', 'queued'))
      await settle()
      live.apply(briefed)
      answers.push(() => Promise.reject(new ApiError(500, 'internal', 'broken')))
      await live.readOpened()
      expect(live.openedMode).toBe('unknown')
      expect(bk.unopened.value).toEqual(['br-3'])
      answers.push(() => Promise.resolve({ unread: [], begun: '' }))
      live.openedEvent()
      await settle()
      expect(bk.unopened.value).toEqual([])
    })
  })
})

describe("the stream's events that change what changed since the last briefing", () => {
  afterEach(() => void vi.useRealTimers())

  /** A store followed by a page, its since reads counted, its clock the test's. */
  function followed() {
    let clock = 1_000_000
    let reads = 0
    const live = new BrakemanLive()
    live.now = () => clock
    const since: BrakemanSince = { since: { from: 'day', at: '2026-11-16T09:00:00Z' }, runsEnded: { count: 0, refs: [] }, tasksFailed: { count: 0, refs: [] }, ticketsReady: { count: 0, refs: [] }, questionsOpen: { count: 1, refs: [] }, truncated: false }
    live.attach({ state: () => Promise.reject(new Error('none')), page: () => Promise.reject(new Error('none')), request: () => Promise.reject(new Error('none')), since: async () => (reads++, since) })
    live.followSince()
    return { live, reads: () => reads, tick: (ms: number) => ((clock += ms), vi.advanceTimersByTime(ms)) }
  }

  // A question asked or answered in a plain session is only a session's event (or its removal): the open questions are counted again.
  for (const [event, payload] of [
    ['session', { id: 'a'.repeat(16), status: 'running', attention: { state: 'working' } }],
    ['removed', { id: 'a'.repeat(16) }],
    ['run', { id: 'fix-login-1a2b3c4d' }],
    ['dispatch', { seq: 1, entity: 'ticket', project: 'BK', ticket: 't1', op: 'edit' }],
  ] as const) {
    it(`reads it again on a ${event} event`, async () => {
      vi.useFakeTimers()
      const f = followed()
      await Promise.resolve()
      expect(f.reads()).toBe(1)
      f.tick(SINCE_EVERY_MS)
      brakemanStream(f.live, event, payload)
      await Promise.resolve()
      expect(f.reads()).toBe(2)
    })
  }

  it("reads it on none of the events that change nothing of it, and hands progress and the opened marks to the store", async () => {
    vi.useFakeTimers()
    const f = followed()
    await Promise.resolve()
    f.tick(SINCE_EVERY_MS)
    brakemanStream(f.live, 'chat', { sessionId: 'a'.repeat(16) })
    brakemanStream(f.live, 'capacity', {})
    await Promise.resolve()
    expect(f.reads()).toBe(1)
    brakemanStream(f.live, 'brakeman_progress', { id: 'br-3', reads: 4, readsPerTurn: 300 })
    expect(f.live.progress['br-3']?.reads).toBe(4)
    brakemanStream(f.live, 'brakeman_opened', {})
    expect(f.live.openedSeq).toBe(1)
  })
})

describe('whether the server has Brakeman', () => {
  it('is said by the state read alone: the requests page, the opened marks and a since answer arriving while it is out leave it unsaid', async () => {
    let answerState!: (s: BrakemanState) => void
    let changes = 0
    const live = new BrakemanLive(() => changes++)
    live.attach({
      state: () => new Promise((done) => (answerState = done)),
      page: async () => ({ requests: [], next: '' }),
      request: () => Promise.reject(new Error('none')),
      opened: async () => ({ unread: [], begun: '' }),
      since: async () => ({ since: { from: 'day', at: '2026-11-16T09:00:00Z' }, runsEnded: { count: 0, refs: [] }, tasksFailed: { count: 0, refs: [] }, ticketsReady: { count: 0, refs: [] }, questionsOpen: { count: 0, refs: [] }, truncated: false }),
    })
    const first = live.readFirst()
    live.followSince()
    await settle()
    await settle()
    // The page, the marks and the since answer are in, the store has told the pages it changed, and the state read is still out.
    expect([live.openedMode, live.sinceNow?.since.from, changes > 0]).toEqual(['server', 'day', true])
    expect(live.available).toBeUndefined()
    answerState({ config: {} as never, current: null, queue: [], turnsToday: 0 })
    await first
    expect(live.available).toBe(true)
  })
})
