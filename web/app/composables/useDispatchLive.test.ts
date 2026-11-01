import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { DispatchOrder, through } from '../utils/dispatchOrder'
import type { DispatchEvent } from './useDispatch'
import { DISPATCH_RETRY_MAX_MS, DispatchLive, asDispatchEvent, listWatch, projectsWatch, ticketWatch } from './useDispatchLive'
import { noDispatchStore, runLinkWatch, runTicketRead, viewsWatch } from './useDispatchLive'
import { ApiError } from './useApi'
import type { RelatedTicket } from './useDispatch'

// The live events' rules (docs/plans/dispatch.md, "The SSE event"), on fake timers: what each page reads again, when, and how often.

const WINDOW = 250
let seq = 100

/** An event of a commit of its own (a seq after every one before), or of the commit `at` names. */
function ev(p: Partial<DispatchEvent> & { at?: number }): DispatchEvent {
  const { at, ...rest } = p
  return { seq: at ?? ++seq, entity: 'ticket', id: '', ticket: '', project: '', ...rest }
}

/** A page's re-read that counts itself, and fails its first `fails` times. */
function reads(fails = 0) {
  const r = { count: 0, fails, at: [] as number[] }
  const read = () => {
    r.count++
    r.at.push(Date.now())
    return r.fails-- > 0 ? Promise.reject(new Error('Cannot reach the server')) : Promise.resolve()
  }
  return { r, read }
}

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(0)
})
afterEach(() => vi.useRealTimers())

describe('a ticket page', () => {
  it("is read again at once for an event naming its ticket, and not again after the window", async () => {
    const live = new DispatchLive()
    const { r, read } = reads()
    live.subscribe(ticketWatch(() => 't1', () => 'CND', read))
    live.event(ev({ entity: 'ticket', id: 't1', ticket: 't1', project: 'CND' }))
    expect(r.count).toBe(1)
    await vi.advanceTimersByTimeAsync(10 * WINDOW)
    expect(r.count).toBe(1)
  })

  it("is read again once after the window for an event naming another ticket of its project, three such events once", async () => {
    const live = new DispatchLive()
    const { r, read } = reads()
    live.subscribe(ticketWatch(() => 't1', () => 'CND', read))
    live.event(ev({ entity: 'ticket', id: 't2', ticket: 't2', project: 'CND' }))
    await vi.advanceTimersByTimeAsync(WINDOW - 1)
    expect(r.count).toBe(0)
    await vi.advanceTimersByTimeAsync(1)
    expect(r.count).toBe(1)
    for (const t of ['t2', 't3', 't4']) live.event(ev({ entity: 'ticket', id: t, ticket: t, project: 'CND' }))
    await vi.advanceTimersByTimeAsync(WINDOW)
    expect(r.count).toBe(2)
    await vi.advanceTimersByTimeAsync(10 * WINDOW)
    expect(r.count).toBe(2)
  })

  it("is not read again for an event of another project, nor for a person's, which belongs to none", async () => {
    const live = new DispatchLive()
    const { r, read } = reads()
    live.subscribe(ticketWatch(() => 't1', () => 'CND', read))
    live.event(ev({ entity: 'ticket', id: 't9', ticket: 't9', project: 'API' }))
    live.event(ev({ entity: 'person', id: '', ticket: '', project: '' }))
    await vi.advanceTimersByTimeAsync(10 * WINDOW)
    expect(r.count).toBe(0)
  })

  it("is read again at once for a comment's event, which names its ticket", () => {
    const live = new DispatchLive()
    const { r, read } = reads()
    live.subscribe(ticketWatch(() => 't1', () => 'CND', read))
    live.event(ev({ entity: 'comment', id: 'c1', ticket: 't1', project: 'CND' }))
    expect(r.count).toBe(1)
  })

  it('names no ticket before it is read: its project is what it re-reads on', async () => {
    const live = new DispatchLive()
    const { r, read } = reads()
    live.subscribe(ticketWatch(() => '', () => 'CND', read))
    live.event(ev({ entity: 'person', id: '', ticket: '', project: 'CND' }))
    expect(r.count).toBe(0)
    await vi.advanceTimersByTimeAsync(WINDOW)
    expect(r.count).toBe(1)
  })
})

describe('a project event', () => {
  it('reads a list of that project and a ticket page of it again after the window, and nothing of another project', async () => {
    const live = new DispatchLive()
    const list = reads()
    const page = reads()
    const other = reads()
    live.subscribe(listWatch(() => 'CND', list.read))
    live.subscribe(ticketWatch(() => 't1', () => 'CND', page.read))
    live.subscribe(listWatch(() => 'API', other.read))
    live.event(ev({ entity: 'project', id: 'p1', ticket: '', project: 'CND' }))
    expect([list.r.count, page.r.count]).toEqual([0, 0])
    await vi.advanceTimersByTimeAsync(WINDOW)
    expect([list.r.count, page.r.count, other.r.count]).toEqual([1, 1, 0])
  })
})

describe('the projects page', () => {
  it('is read again once after the window for any event at all', async () => {
    const live = new DispatchLive()
    const { r, read } = reads()
    live.subscribe(projectsWatch(read))
    live.event(ev({ entity: 'ticket', id: 't1', ticket: 't1', project: 'CND' }))
    live.event(ev({ entity: 'ticket', id: 't9', ticket: 't9', project: 'API' }))
    live.event(ev({ entity: 'person', id: '', ticket: '', project: '' }))
    expect(r.count).toBe(0)
    await vi.advanceTimersByTimeAsync(WINDOW)
    expect(r.count).toBe(1)
    await vi.advanceTimersByTimeAsync(10 * WINDOW)
    expect(r.count).toBe(1)
  })
})

describe('a snapshot', () => {
  it('reads every page subscribed again at once', () => {
    const live = new DispatchLive()
    const pages = [reads(), reads(), reads()]
    live.subscribe(ticketWatch(() => 't1', () => 'CND', pages[0]!.read))
    live.subscribe(listWatch(() => 'CND', pages[1]!.read))
    live.subscribe(projectsWatch(pages[2]!.read))
    live.snapshot()
    expect(pages.map((p) => p.r.count)).toEqual([1, 1, 1])
  })

  it('starts the seqs over: an event after it is read whatever seq it has (a store restored from a backup)', () => {
    const live = new DispatchLive()
    const { r, read } = reads()
    live.subscribe(ticketWatch(() => 't1', () => 'CND', read))
    live.event(ev({ at: 900, entity: 'ticket', id: 't1', ticket: 't1', project: 'CND' }))
    live.snapshot()
    expect(r.count).toBe(2)
    live.event(ev({ at: 5, entity: 'ticket', id: 't1', ticket: 't1', project: 'CND' }))
    expect(r.count).toBe(3)
  })
})

describe('a re-read that fails', () => {
  it('is asked again after the window, the wait doubled each time, and stops once one answers', async () => {
    const live = new DispatchLive()
    const { r, read } = reads(3)
    live.subscribe(listWatch(() => 'CND', read))
    live.event(ev({ project: 'CND', ticket: 't1', id: 't1' }))
    await vi.advanceTimersByTimeAsync(WINDOW)
    expect(r.count).toBe(1)
    await vi.advanceTimersByTimeAsync(20_000)
    // Read at 250 (failed), then after 250, 500 and 1,000 more: the fourth answered, and nothing is asked after it.
    expect(r.at).toEqual([250, 500, 1000, 2000])
  })

  it('waits at most 8 s between two asks', async () => {
    const live = new DispatchLive()
    const { r, read } = reads(Number.POSITIVE_INFINITY)
    live.subscribe(ticketWatch(() => 't1', () => 'CND', read))
    live.event(ev({ entity: 'ticket', id: 't1', ticket: 't1', project: 'CND' }))
    await vi.advanceTimersByTimeAsync(40_000)
    const gaps = r.at.slice(1).map((t, i) => t - r.at[i]!)
    expect(gaps.slice(0, 7)).toEqual([250, 500, 1000, 2000, 4000, 8000, 8000])
    expect(Math.max(...gaps)).toBe(DISPATCH_RETRY_MAX_MS)
  })

  it('stops being asked once a snapshot reads it again, and the wait starts over', async () => {
    const live = new DispatchLive()
    const { r, read } = reads(2)
    live.subscribe(listWatch(() => 'CND', read))
    live.event(ev({ project: 'CND' }))
    await vi.advanceTimersByTimeAsync(WINDOW)
    await vi.advanceTimersByTimeAsync(WINDOW)
    // Two failed, the next ask is due in 500 ms: a snapshot reads it now, and it answers.
    expect(r.count).toBe(2)
    live.snapshot()
    expect(r.count).toBe(3)
    await vi.advanceTimersByTimeAsync(20_000)
    expect(r.count).toBe(3)
    // A later failure waits the window again, not the doubled wait it had come to.
    r.fails = 1
    live.event(ev({ project: 'CND' }))
    await vi.advanceTimersByTimeAsync(WINDOW)
    expect(r.count).toBe(4)
    await vi.advanceTimersByTimeAsync(WINDOW)
    expect(r.count).toBe(5)
  })
})

describe("a page's own read of part of it, asked through the store", () => {
  it("is asked again after the window when it fails, doubled, by the watch's read, until one answers", async () => {
    const live = new DispatchLive()
    const { r, read } = reads(1)
    const watch = listWatch(() => 'CND', read)
    live.subscribe(watch)
    // A filter changed: the page reads its new list, which fails.
    let own = 0
    live.ask(watch, () => {
      own++
      return Promise.reject(new Error('Cannot reach the server'))
    })
    expect([own, r.count]).toEqual([1, 0])
    await vi.advanceTimersByTimeAsync(20_000)
    // The watch's read at 250 (failed again) and 750 (answered), and nothing after.
    expect(r.at).toEqual([250, 750])
    expect(own).toBe(1)
  })

  it('stands for no event: a window gathered before it still reads the whole page, once, and reads for its failure too', async () => {
    const live = new DispatchLive()
    const { r, read } = reads()
    const watch = listWatch(() => 'CND', read)
    live.subscribe(watch)
    // A label renamed: the project's event gathers a read of the whole page; the filter changes 100 ms into the window.
    live.event(ev({ project: 'CND' }))
    await vi.advanceTimersByTimeAsync(100)
    let own = 0
    live.ask(watch, () => {
      own++
      return Promise.reject(new Error('Cannot reach the server'))
    })
    await vi.advanceTimersByTimeAsync(10 * WINDOW)
    // The window's read, at 250 as gathered: not dropped for the list's read, and no retry of its own besides.
    expect(own).toBe(1)
    expect(r.at).toEqual([250])
  })

  it("is the page's first read too: made through the store once the page is subscribed, it is asked again with no event and no snapshot", async () => {
    const live = new DispatchLive()
    const { r, read } = reads(2)
    const watch = ticketWatch(() => '', () => 'CND', read)
    // useDispatchLive as the page is mounted: subscribed, then its `first`, the page's load, which reads the page through `ask`. The
    // stream was connected already (the page opened from another), so no snapshot comes to read it.
    live.subscribe(watch)
    live.ask(watch, read)
    expect(r.count).toBe(1)
    await vi.advanceTimersByTimeAsync(20_000)
    // Failed at 0, asked again at 250 (failed) and at 750 (answered), and nothing after.
    expect(r.at).toEqual([0, 250, 750])
  })

  it('reads nothing for a page not subscribed, or gone, and asks nothing again for one gone while its read was on its way', async () => {
    const live = new DispatchLive()
    const { r, read } = reads()
    const watch = listWatch(() => 'CND', read)
    let own = 0
    let fail!: (e: Error) => void
    const send = () => {
      own++
      return new Promise<void>((_, reject) => (fail = reject))
    }
    live.ask(watch, send)
    expect(own).toBe(0)
    const stop = live.subscribe(watch)
    live.ask(watch, send)
    expect(own).toBe(1)
    stop()
    fail(new Error('Cannot reach the server'))
    live.ask(watch, send)
    await vi.advanceTimersByTimeAsync(20_000)
    expect([own, r.count]).toEqual([1, 0])
  })
})

describe('a snapshot after a store restored from a backup', () => {
  it("shows the store as restored: the page's gate forgets the rev it showed, a reply asked before is dropped, the next edit expects the store's rev", async () => {
    const live = new DispatchLive()
    const gate = new DispatchOrder()
    type Shown = { rev: number; title: string }
    let store: Shown = { rev: 10, title: 'before the restore' }
    let shown: Shown | null = null
    const read = () => through(gate, 'ticket:CND-12', () => Promise.resolve({ ...store }), (t) => (shown = t), (t) => t.rev)
    live.subscribe({ ...ticketWatch(() => 't1', () => 'CND', read), reset: () => gate.reset() })
    live.event(ev({ entity: 'ticket', id: 't1', ticket: 't1', project: 'CND' }))
    await vi.advanceTimersByTimeAsync(0)
    expect(shown).toEqual({ rev: 10, title: 'before the restore' })
    // A read asked before the stream dropped, answered after it started again.
    let answer!: (t: Shown) => void
    const asked = through(gate, 'ticket:CND-12', () => new Promise<Shown>((resolve) => (answer = resolve)), (t) => (shown = t), (t) => t.rev)
    // The server restarts on a backup: the ticket is at rev 3 there.
    store = { rev: 3, title: 'restored' }
    live.snapshot()
    await vi.advanceTimersByTimeAsync(0)
    expect(shown).toEqual({ rev: 3, title: 'restored' })
    answer({ rev: 11, title: 'asked before the restore' })
    expect(await asked).toBe(false)
    expect(shown).toEqual({ rev: 3, title: 'restored' })
    expect(gate.rev('ticket:CND-12')).toBe(3)
    // Its events are read again as the store has them.
    store = { rev: 4, title: 'edited after the restore' }
    live.event(ev({ at: 1, entity: 'ticket', id: 't1', ticket: 't1', project: 'CND' }))
    await vi.advanceTimersByTimeAsync(0)
    expect(shown).toEqual({ rev: 4, title: 'edited after the restore' })
  })
})

// The owner's decision of 2026-10-30 (Owner decisions, 22): a commit's events arrive together, and a commit that changed more than 64
// entities of one kind in one project arrives as one event for them, its id and ticket empty, its project set. The pages key on the
// project: an empty ticket falls to the project's gathered re-read, and nothing reads the event's id.
describe("a commit's collapsed event", () => {
  it("falls to its project's gathered re-read on a ticket page and reads its lists and the projects page, never another project's", async () => {
    const live = new DispatchLive()
    const page = reads()
    const list = reads()
    const projects = reads()
    const other = reads()
    live.subscribe(ticketWatch(() => 't1', () => 'CND', page.read))
    live.subscribe(listWatch(() => 'CND', list.read))
    live.subscribe(projectsWatch(projects.read))
    live.subscribe(ticketWatch(() => 't9', () => 'API', other.read))
    live.event(ev({ entity: 'ticket', id: '', ticket: '', project: 'CND' }))
    // Not at once: an empty ticket names no ticket, not even one whose id the page has not read yet.
    expect(page.r.count).toBe(0)
    await vi.advanceTimersByTimeAsync(WINDOW)
    expect([page.r.count, list.r.count, projects.r.count, other.r.count]).toEqual([1, 1, 1, 0])
  })

  it('is read by its data whatever its id holds, and an empty ticket never matches a page that has not read its own', () => {
    const live = new DispatchLive()
    const { r, read } = reads()
    live.subscribe(ticketWatch(() => '', () => 'CND', read))
    live.event(ev({ entity: 'comment', id: '', ticket: '', project: 'CND' }))
    expect(r.count).toBe(0)
  })
})

describe("a commit's events", () => {
  it('arrive together: the read one of them caused stands for the rest', async () => {
    const live = new DispatchLive()
    const { r, read } = reads()
    live.subscribe(ticketWatch(() => 't1', () => 'CND', read))
    // A relation added between t1 and t2: one event for each ticket, of one commit.
    live.event(ev({ at: 500, entity: 'ticket', id: 't1', ticket: 't1', project: 'CND' }))
    live.event(ev({ at: 500, entity: 'ticket', id: 't2', ticket: 't2', project: 'CND' }))
    live.event(ev({ at: 500, entity: 'comment', id: 'c1', ticket: 't1', project: 'CND' }))
    expect(r.count).toBe(1)
    await vi.advanceTimersByTimeAsync(10 * WINDOW)
    expect(r.count).toBe(1)
    // The next commit is read again.
    live.event(ev({ at: 501, entity: 'ticket', id: 't1', ticket: 't1', project: 'CND' }))
    expect(r.count).toBe(2)
  })
})

describe('a page gone', () => {
  it('is read no more, its window and its retry dropped', async () => {
    const live = new DispatchLive()
    const { r, read } = reads(1)
    const stop = live.subscribe(listWatch(() => 'CND', read))
    live.event(ev({ project: 'CND' }))
    await vi.advanceTimersByTimeAsync(WINDOW)
    expect(r.count).toBe(1)
    stop()
    await vi.advanceTimersByTimeAsync(20_000)
    live.event(ev({ project: 'CND' }))
    live.snapshot()
    await vi.advanceTimersByTimeAsync(20_000)
    expect(r.count).toBe(1)
    expect(live.size).toBe(0)
  })
})

describe('the stream data', () => {
  it('reads a dispatch event, and nothing that is not one', () => {
    expect(asDispatchEvent({ seq: 7, entity: 'ticket', id: 'a', ticket: 'a', project: 'CND' })).toEqual({ seq: 7, entity: 'ticket', id: 'a', ticket: 'a', project: 'CND' })
    expect(asDispatchEvent({ seq: 7, entity: 'person', id: '', ticket: '', project: '' })).toEqual({ seq: 7, entity: 'person', id: '', ticket: '', project: '' })
    expect(asDispatchEvent({ seq: 8, entity: 'ticket', project: 'CND' })).toEqual({ seq: 8, entity: 'ticket', id: '', ticket: '', project: 'CND' })
    expect(asDispatchEvent(null)).toBeNull()
    expect(asDispatchEvent({ entity: 'ticket' })).toBeNull()
    expect(asDispatchEvent({ seq: '7', entity: 'ticket' })).toBeNull()
  })
})

describe("a list's saved views (PR-9)", () => {
  it('are read again once after the window on a view event of every project (project ""), for a list of any project', async () => {
    const live = new DispatchLive()
    const cnd = reads()
    const api = reads()
    live.subscribe(viewsWatch(() => 'CND', cnd.read))
    live.subscribe(viewsWatch(() => 'API', api.read))
    live.event(ev({ entity: 'view', id: 'v1', ticket: '', project: '' }))
    expect([cnd.r.count, api.r.count]).toEqual([0, 0])
    await vi.advanceTimersByTimeAsync(WINDOW - 1)
    expect([cnd.r.count, api.r.count]).toEqual([0, 0])
    await vi.advanceTimersByTimeAsync(1)
    expect([cnd.r.count, api.r.count]).toEqual([1, 1])
    await vi.advanceTimersByTimeAsync(10 * WINDOW)
    expect([cnd.r.count, api.r.count]).toEqual([1, 1])
  })

  it("are read again on a view event of their own project, and not on another project's", async () => {
    const live = new DispatchLive()
    const cnd = reads()
    const api = reads()
    live.subscribe(viewsWatch(() => 'CND', cnd.read))
    live.subscribe(viewsWatch(() => 'API', api.read))
    live.event(ev({ entity: 'view', id: 'v2', ticket: '', project: 'CND' }))
    await vi.advanceTimersByTimeAsync(WINDOW)
    expect([cnd.r.count, api.r.count]).toEqual([1, 0])
  })

  it('are not read again for any other event, and three view events in one window are one read', async () => {
    const live = new DispatchLive()
    const { r, read } = reads()
    live.subscribe(viewsWatch(() => 'CND', read))
    live.event(ev({ entity: 'ticket', id: 't1', ticket: 't1', project: 'CND' }))
    live.event(ev({ entity: 'label', id: 'l1', ticket: '', project: 'CND' }))
    live.event(ev({ entity: 'person', id: '', ticket: '', project: '' }))
    await vi.advanceTimersByTimeAsync(10 * WINDOW)
    expect(r.count).toBe(0)
    for (const id of ['v1', 'v2', 'v3']) live.event(ev({ entity: 'view', id, ticket: '', project: id === 'v2' ? 'CND' : '' }))
    await vi.advanceTimersByTimeAsync(WINDOW)
    expect(r.count).toBe(1)
  })

  it('beside the list, which is read again on its project\'s events and not on a view of every project', async () => {
    const live = new DispatchLive()
    const list = reads()
    const views = reads()
    live.subscribe(listWatch(() => 'CND', list.read))
    live.subscribe(viewsWatch(() => 'CND', views.read))
    live.event(ev({ entity: 'view', id: 'v1', ticket: '', project: '' }))
    await vi.advanceTimersByTimeAsync(WINDOW)
    expect([list.r.count, views.r.count]).toEqual([0, 1])
    live.event(ev({ entity: 'ticket', id: 't1', ticket: 't1', project: 'CND' }))
    await vi.advanceTimersByTimeAsync(WINDOW)
    expect([list.r.count, views.r.count]).toEqual([1, 1])
  })

  it('are read again by a snapshot, and a read that fails is asked again after the window, doubled', async () => {
    const live = new DispatchLive()
    const { r, read } = reads(2)
    live.subscribe(viewsWatch(() => 'CND', read))
    live.snapshot()
    expect(r.count).toBe(1)
    await vi.advanceTimersByTimeAsync(WINDOW)
    expect(r.count).toBe(2)
    await vi.advanceTimersByTimeAsync(2 * WINDOW)
    expect(r.count).toBe(3)
    await vi.advanceTimersByTimeAsync(10 * WINDOW)
    expect(r.count).toBe(3)
  })
})

describe("the run page's chip (PR-11)", () => {
  it('reads its lookup again once after the window on a ticket event of any project, three of them once', async () => {
    const live = new DispatchLive()
    const { r, read } = reads()
    live.subscribe(runLinkWatch(read))
    live.event(ev({ entity: 'ticket', id: 't1', ticket: 't1', project: 'CND' }))
    live.event(ev({ entity: 'ticket', id: 't9', ticket: 't9', project: 'API' }))
    live.event(ev({ entity: 'ticket', id: 't2', ticket: 't2', project: 'CND' }))
    expect(r.count).toBe(0)
    await vi.advanceTimersByTimeAsync(WINDOW - 1)
    expect(r.count).toBe(0)
    await vi.advanceTimersByTimeAsync(1)
    expect(r.count).toBe(1)
    await vi.advanceTimersByTimeAsync(10 * WINDOW)
    expect(r.count).toBe(1)
  })

  it("reads it on a commit's collapsed ticket event, which names no ticket", async () => {
    const live = new DispatchLive()
    const { r, read } = reads()
    live.subscribe(runLinkWatch(read))
    live.event(ev({ entity: 'ticket', id: '', ticket: '', project: 'CND' }))
    await vi.advanceTimersByTimeAsync(WINDOW)
    expect(r.count).toBe(1)
  })

  it("is not read for a label's, a comment's, a project's, a view's or a person's event, where the projects page is", async () => {
    const live = new DispatchLive()
    const lookup = reads()
    const projects = reads()
    live.subscribe(runLinkWatch(lookup.read))
    live.subscribe(projectsWatch(projects.read))
    live.event(ev({ entity: 'label', id: 'l1', ticket: '', project: 'CND' }))
    await vi.advanceTimersByTimeAsync(WINDOW)
    expect([lookup.r.count, projects.r.count]).toEqual([0, 1])
    for (const entity of ['comment', 'project', 'milestone', 'view', 'person'] as const) live.event(ev({ entity, id: '', ticket: entity === 'comment' ? 't1' : '', project: entity === 'person' ? '' : 'CND' }))
    await vi.advanceTimersByTimeAsync(WINDOW)
    expect([lookup.r.count, projects.r.count]).toEqual([0, 2])
  })

  it('reads it again at once on a snapshot, and a read that fails is asked again after the window, doubled', async () => {
    const live = new DispatchLive()
    const { r, read } = reads(2)
    live.subscribe(runLinkWatch(read))
    live.snapshot()
    expect(r.count).toBe(1)
    await vi.advanceTimersByTimeAsync(WINDOW)
    expect(r.count).toBe(2)
    await vi.advanceTimersByTimeAsync(2 * WINDOW - 1)
    expect(r.count).toBe(2)
    await vi.advanceTimersByTimeAsync(1)
    expect(r.count).toBe(3)
    await vi.advanceTimersByTimeAsync(20 * WINDOW)
    expect(r.count).toBe(3)
  })

  it('takes the latest lookup through the ordering gate: a slow answer of an earlier read never replaces a later one', async () => {
    const gate = new DispatchOrder()
    let shown: string | null = 'unread'
    let slow!: (v: string | null) => void
    const first = through(gate, 'run-ticket:r1', () => new Promise<string | null>((resolve) => (slow = resolve)), (v) => (shown = v))
    await through(gate, 'run-ticket:r1', () => Promise.resolve(null), (v) => (shown = v))
    slow('CND-12')
    expect(await first).toBe(false)
    expect(shown).toBeNull()
  })

  it("orders a server's no-Dispatch answer through the gate: a slow 503 never clears a chip a later lookup filled, after a reset too", async () => {
    const ticket = { key: 'CND-12' } as RelatedTicket
    const unavailable = () => new ApiError(503, 'dispatch_unavailable', 'Dispatch is not available on this server')
    for (const reset of [false, true]) {
      const gate = new DispatchOrder()
      let shown: RelatedTicket | null | 'unread' = 'unread'
      let refuse!: (e: unknown) => void
      const first = runTicketRead(gate, 'r1', () => new Promise<RelatedTicket | null>((_, reject) => (refuse = reject)), (t) => (shown = t))
      // A reconnect's snapshot resets the gate before the next lookup.
      if (reset) gate.reset()
      expect(await runTicketRead(gate, 'r1', () => Promise.resolve(ticket), (t) => (shown = t))).toBe(true)
      refuse(unavailable())
      expect(await first).toBe(false)
      expect(shown).toBe(ticket)
    }
    // Alone, it answers no ticket; a switchyard the same; any other failure rejects and applies nothing.
    const gate = new DispatchOrder()
    let shown: RelatedTicket | null | 'unread' = 'unread'
    expect(await runTicketRead(gate, 'r2', () => Promise.reject(unavailable()), (t) => (shown = t))).toBe(true)
    expect(shown).toBeNull()
    shown = 'unread'
    expect(await runTicketRead(gate, 'r3', () => Promise.reject(new ApiError(403, 'switchyard', 'A switchyard')), (t) => (shown = t))).toBe(true)
    expect(shown).toBeNull()
    shown = 'unread'
    await expect(runTicketRead(gate, 'r4', () => Promise.reject(new ApiError(500, 'internal', 'Boom')), (t) => (shown = t))).rejects.toThrow('Boom')
    expect(shown).toBe('unread')
    expect(noDispatchStore(new ApiError(403, 'forbidden', 'No'))).toBe(false)
  })
})
