import { describe, expect, it } from 'vitest'
import type { BrakemanRequest, BrakemanState } from '../utils/brakeman'
import { ApiError } from './useApi'
import { BrakemanLive, asBrakemanEvent, sendTwice, type BrakemanReads } from './useBrakeman'

// Brakeman's live store and its writes (docs/plans/brakeman.md, PR-6): an id kept across a lost reply, and the `brakeman` events
// folded into the list.

function req(id: string, state: string, over: Partial<BrakemanRequest> = {}): BrakemanRequest {
  return { id, opId: `op-${id}`, kind: 'briefing', createdAt: '2026-11-02T09:00:00Z', state, agent: {}, config: { visibility: {} as never, scope: {}, permissions: { observe: true, draft: true, propose: true }, budget: { turnMinutes: 15, readsPerTurn: 300, turnsPerDay: 40 }, rev: 1 }, reads: 0, ...over }
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

  it('tell the pages each change, and a snapshot reads what was missed', async () => {
    const f = fakeServer()
    let changes = 0
    const live = new BrakemanLive(() => changes++)
    live.attach(f.reads)
    await live.readFirst()
    const before = changes
    f.server.set('br-1', req('br-1', 'briefed', { hasBrief: true }))
    live.snapshot()
    await settle()
    await settle()
    expect(live.list().map((r) => r.id)).toEqual(['br-1'])
    expect(changes).toBeGreaterThan(before)
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
