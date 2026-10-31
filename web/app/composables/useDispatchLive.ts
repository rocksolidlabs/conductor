import { RUN_EVENT_WINDOW_MS } from '../utils/runs'
import { type DispatchOrder, through } from '../utils/dispatchOrder'
import { ApiError } from './useApi'
import type { DispatchEvent, RelatedTicket } from './useDispatch'

/** The longest a re-read that failed waits before it is asked again: the window, doubled after each failure up to this. */
export const DISPATCH_RETRY_MAX_MS = 8000

/**
 * What a page re-reads on Dispatch's events, and when (docs/plans/dispatch.md, "The SSE event"). What a page shows derives from more
 * than its own entity (a ticket's blocked from its blockers, its children's rows from other tickets, its labels and policy from its
 * project, the projects' counts from every ticket), so the scope a page re-reads on is its project, not its entity.
 */
export interface DispatchWatch {
  /** Whether an event names what the page shows itself (a ticket page's ticket): read again at once. */
  now?: (e: DispatchEvent) => boolean
  /** Whether an event touches what the page shows: read again once, at the end of the window that gathers it. */
  later: (e: DispatchEvent) => boolean
  /** The page's re-read, through its ordering gate: resolves once it answered, rejects when it failed. */
  read: () => Promise<unknown>
  /**
   * Called on a `snapshot` before the page is read again: the stream started over, maybe on a store restored from a backup whose `rev`s
   * are below those shown, so the page forgets what its ordering gate knows (DispatchOrder.reset) and shows what the read finds.
   */
  reset?: () => void
}

/**
 * A ticket page's watch: its ticket at once when an event's `ticket` is it (a comment, link, attachment or relation names the ticket it
 * belongs to), and after the window on any other event of its project (a blocker closed, a sibling reparented, a label renamed, the policy
 * edited). An event whose `ticket` is empty never names it: a person's, or the one event a commit that changed more than 64 entities of a
 * kind in the project sends for them, which falls to the project's gathered re-read.
 */
export function ticketWatch(ticketId: () => string, project: () => string, read: () => Promise<unknown>): DispatchWatch {
  return {
    now: (e) => e.ticket !== '' && e.ticket === ticketId(),
    later: (e) => e.project !== '' && e.project === project(),
    read,
  }
}

/** A list's, a board's or a roadmap's watch: itself, after the window, on any event of its project. */
export function listWatch(project: () => string, read: () => Promise<unknown>): DispatchWatch {
  return { later: (e) => e.project !== '' && e.project === project(), read }
}

/**
 * A list's, a board's or a roadmap's saved views' watch: read again, after the window, on a `view` event of its project or of every
 * project (`project` ""): a view of every project is offered on every project's pages beside the project's own, and its save, edit and
 * delete name no project.
 */
export function viewsWatch(project: () => string, read: () => Promise<unknown>): DispatchWatch {
  return { later: (e) => e.entity === 'view' && (e.project === '' || e.project === project()), read }
}

/** The projects page's watch: itself, after the window, on any event at all, since a ticket's status moves its project's counts. */
export function projectsWatch(read: () => Promise<unknown>): DispatchWatch {
  return { later: () => true, read }
}

/**
 * The run page's chip's watch (PR-11): the ticket the run is linked to (GET /api/dispatch/links?run=) read again, after the window, on
 * every event whose entity is a ticket. A link's add or remove and a ticket's delete or undelete are events of that ticket, which name no
 * run and no field, and a run linked to nothing has no project to narrow by, so every ticket event is one the chip may follow; the read
 * is one indexed row. The collapsed event a commit sends for more than 64 tickets of a project is a ticket event too.
 */
export function runLinkWatch(read: () => Promise<unknown>): DispatchWatch {
  return { later: (e) => e.entity === 'ticket', read }
}

/** Whether a failure says the server has no Dispatch store (503: no data directory, a store that could not open) or is a switchyard (403). */
export function noDispatchStore(e: unknown): boolean {
  return e instanceof ApiError && (e.status === 503 || (e.status === 403 && e.code === 'switchyard'))
}

/**
 * The run page's chip's read (GET /api/dispatch/links?run=), through the page's ordering gate. A server with no Dispatch store answers
 * no ticket inside the gated read, ordered as any reply: a slow refusal never clears a chip a later read filled, nor one read after a
 * reset (a snapshot, a reconnect). Any other failure rejects and applies nothing, and is asked again as a re-read is.
 */
export function runTicketRead(gate: DispatchOrder, run: string, send: () => Promise<RelatedTicket | null>, apply: (t: RelatedTicket | null) => void): Promise<boolean> {
  return through(
    gate,
    `run-ticket:${run}`,
    () =>
      send().catch((e: unknown) => {
        if (noDispatchStore(e)) return null
        throw e
      }),
    apply,
  )
}

/** A dispatch event's data as the stream sent it, or null for what is not one. */
export function asDispatchEvent(v: unknown): DispatchEvent | null {
  if (v === null || typeof v !== 'object') return null
  const e = v as Record<string, unknown>
  if (typeof e.seq !== 'number' || typeof e.entity !== 'string') return null
  const text = (x: unknown) => (typeof x === 'string' ? x : '')
  return { seq: e.seq, entity: e.entity as DispatchEvent['entity'], id: text(e.id), ticket: text(e.ticket), project: text(e.project) }
}

/** One page's subscription. */
interface Watching {
  watch: DispatchWatch
  /** The window's timer, or a retry's. */
  timer: ReturnType<typeof setTimeout> | undefined
  /** How long the next retry waits. */
  delay: number
  /**
   * The highest seq of the events received when the page's last re-read was asked: an event at or below it is of a commit made before
   * that read was sent (the store's commits are ordered by their seq), so the read stands for it. A commit's events arrive together.
   */
  covered: number
  gone: boolean
}

/**
 * The live store of Dispatch's events (useAttention hands it every `dispatch` event and every `snapshot` of GET /api/events): each page
 * subscribes with its watch, and is read again at once for an event that names what it shows, after the window (RUN_EVENT_WINDOW_MS,
 * the run events' rule) for one that touches it, three such events in one window one read, and at once on a `snapshot`, which is how a
 * client dropped for falling behind repairs what it missed. A re-read that fails is asked again after the window, doubled each time up
 * to DISPATCH_RETRY_MAX_MS, until one answers or a `snapshot` reads everything again, so a page behind on a passing failure catches up
 * with no further event. Nothing polls. No event's `id` is read: what a page re-reads is keyed on `ticket` and `project`, and an empty
 * one names nothing.
 */
export class DispatchLive {
  private readonly watching = new Set<Watching>()
  /** The highest seq received since the last snapshot. */
  private seen = -1

  constructor(
    private readonly windowMs = RUN_EVENT_WINDOW_MS,
    private readonly maxDelayMs = DISPATCH_RETRY_MAX_MS,
  ) {}

  /** Starts handing events to a page's watch; the function returned stops it. */
  subscribe(watch: DispatchWatch): () => void {
    const w: Watching = { watch, timer: undefined, delay: this.windowMs, covered: -1, gone: false }
    this.watching.add(w)
    return () => {
      w.gone = true
      clearTimeout(w.timer)
      w.timer = undefined
      this.watching.delete(w)
    }
  }

  /** One dispatch event, for every page it names or touches. */
  event(e: DispatchEvent): void {
    this.seen = Math.max(this.seen, e.seq)
    for (const w of [...this.watching]) {
      if (e.seq <= w.covered) continue
      if (w.watch.now?.(e)) this.read(w)
      else if (w.watch.later(e)) this.gather(w)
    }
  }

  /**
   * The stream started again: what was missed is unknown, so every page reads again at once. Seqs start over, and so do the `rev`s each
   * page's gate knows (`reset`), for a store restored from a backup.
   */
  snapshot(): void {
    this.seen = -1
    for (const w of [...this.watching]) {
      w.delay = this.windowMs
      w.covered = -1
      w.watch.reset?.()
      this.read(w)
    }
  }

  /**
   * Reads part of a subscribed page now by `send` (the list a changed filter names), so that a failure is asked again as a re-read's is:
   * after the delay, doubled each time up to the most, by the watch's read, until one answers or a snapshot reads it. It stands for no
   * event, so a window gathered or a retry pending still reads the whole page, and reads for its failure too. A watch not subscribed (a
   * page gone) reads nothing.
   */
  ask(watch: DispatchWatch, send: () => Promise<unknown>): void {
    const w = [...this.watching].find((x) => x.watch === watch)
    if (!w) return
    let read: Promise<unknown>
    try {
      read = Promise.resolve(send())
    } catch (e) {
      read = Promise.reject(e)
    }
    read.then(
      () => {
        if (!w.gone) w.delay = this.windowMs
      },
      () => this.retry(w),
    )
  }

  /** How many pages are watching (for a test). */
  get size(): number {
    return this.watching.size
  }

  /** Gathers a re-read at the end of the window; a window or a retry already pending reads for it. */
  private gather(w: Watching): void {
    if (w.timer !== undefined) return
    w.timer = setTimeout(() => this.fire(w), this.windowMs)
  }

  private fire(w: Watching): void {
    w.timer = undefined
    this.read(w)
  }

  /** Reads the page again now: the read stands for a window or a retry pending, which goes. A failure is asked again after the delay. */
  private read(w: Watching): void {
    clearTimeout(w.timer)
    w.timer = undefined
    w.covered = this.seen
    let read: Promise<unknown>
    try {
      read = Promise.resolve(w.watch.read())
    } catch (e) {
      read = Promise.reject(e)
    }
    read.then(
      () => {
        if (!w.gone) w.delay = this.windowMs
      },
      () => this.retry(w),
    )
  }

  /** A read failed: the page is read again after the delay, doubled for the next, unless it is gone. */
  private retry(w: Watching): void {
    // A window gathered while it was on its way, or a retry pending, reads again anyway.
    if (w.gone || w.timer !== undefined) return
    const wait = w.delay
    w.delay = Math.min(w.delay * 2, this.maxDelayMs)
    w.timer = setTimeout(() => this.fire(w), wait)
  }
}

/** The app's one live store of Dispatch's events, made by the first page or stream that asks. */
let live: DispatchLive | undefined
export function dispatchLive(): DispatchLive {
  return (live ??= new DispatchLive())
}

/**
 * Subscribes a page's watch to the live events while it is mounted, and starts the stream (useAttention) when nothing has. `ask` reads
 * part of the page now through the live store (DispatchLive.ask), so that a read the page asks of itself is asked again after a failure.
 * `first`, the page's first read, is called once the watch is subscribed, as the page is mounted: a read it makes through `ask` is asked
 * again when it fails, as a re-read is. The stream may be connected already (a page opened from another), and then no snapshot comes to
 * read the page for it.
 */
export function useDispatchLive(watch: DispatchWatch, first?: () => void): { ask: (read: () => Promise<unknown>) => void } {
  const attention = useAttention()
  let stop: (() => void) | undefined
  onMounted(() => {
    attention.start()
    stop = dispatchLive().subscribe(watch)
    first?.()
  })
  onBeforeUnmount(() => stop?.())
  return { ask: (read) => dispatchLive().ask(watch, read) }
}
