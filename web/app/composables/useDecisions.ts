import { hasInjectionContext, inject } from 'vue'
import type { DecisionEvent } from '~/utils/protocol'
import { REF_ACCESS } from '~/utils/refAccess'
import type { Decision, DecisionRef } from '~/utils/decisions'
import { isDecisionId, replaces, runHeldOf } from '~/utils/decisions'
import type { RunInfo } from './useSessions'

/**
 * The durable decisions the operator has before them (Program item DR1, docs/plans/decisions.md, "web/app"): the open and answered
 * ones, from `GET /api/decisions`, read again on a `decision` event of the one events stream useAttention.ts holds (no second stream),
 * on that stream's `snapshot` (its reconnect: the stream replays no event it missed while away, so a gate opened or a decision answered
 * meanwhile is read then), and, while the stream is down, on the cadence useAttention.ts reads the sessions at then. One store per app
 * (decisionsLive); the card (useDecisionRecord), the ticket page (useTicketDecisions) and the store's list subscribe to the same
 * stream through it.
 */

/** The `decision` event's data as the stream sends it, or null for anything else. */
export function asDecisionEvent(v: unknown): DecisionEvent | null {
  if (v === null || typeof v !== 'object') return null
  const e = v as Record<string, unknown>
  return isDecisionId(e.id as string) && typeof e.run === 'string' && typeof e.state === 'string' && typeof e.rev === 'number' ? (e as unknown as DecisionEvent) : null
}

/** What the store reads: one state's page of the operator's list, newest first, after `after` (a page's `next`) when given. */
export interface DecisionReads {
  list: (state: 'open' | 'answered', after?: string) => Promise<{ decisions: Decision[]; next?: string }>
}

/**
 * How many pages of each state the store reads (the route's pages hold at most 100): past them the list is cut, and the store says
 * so (`cut`), so Needs you and the palette never claim to be whole. Ten pages are a thousand decisions open at once.
 */
export const DECISION_PAGES = 10

/** Every page of one state, following `next` up to DECISION_PAGES pages; `cut` when more followed. */
export async function readAll(reads: DecisionReads, state: 'open' | 'answered', pages = DECISION_PAGES): Promise<{ decisions: Decision[]; cut: boolean }> {
  const out: Decision[] = []
  let after: string | undefined
  for (let i = 0; i < pages; i++) {
    const r = await reads.list(state, after)
    out.push(...r.decisions)
    if (!r.next || r.next === after) return { decisions: out, cut: false }
    after = r.next
  }
  return { decisions: out, cut: true }
}

/**
 * What a page that shows decisions is told: a decision changed (`e`), or the stream came back or is down (null: read it all again);
 * `restarted` when the stream came back (its `snapshot`), which may follow a store restored from a backup.
 */
export type DecisionWatcher = (e: DecisionEvent | null, restarted?: boolean) => void

/**
 * The one rule every reader of decisions keeps (the store's list, the ticket page's, the card's record; Codex's round 2 on #719): one
 * read in flight and at most one queued. A read asked while one is under way is made once after it, so a burst of events reads twice at
 * most and the last read always starts after the last ask; the replies therefore come back in the order the reads were made, and a
 * reply is never older than one applied before it, whatever revision it carries. A read that fails ends only itself (`failed` is told):
 * one asked meanwhile is still made, since the event that asked for it may be the only one that comes.
 */
export class SerialReads {
  private inflight: Promise<void> | null = null
  private again = false
  private stopped = false
  constructor(
    private readonly once: () => Promise<void>,
    private readonly failed: (e: Error) => void = () => {},
  ) {}

  /** Asks for a read: made now, or once after the one under way. Resolves when the reads it waits for are done; never rejects. */
  run(): Promise<void> {
    if (this.stopped) return this.inflight ?? Promise.resolve()
    if (this.inflight) {
      this.again = true
      return this.inflight
    }
    this.inflight = (async () => {
      try {
        do {
          this.again = false
          try {
            await this.once()
          } catch (e) {
            try {
              this.failed(e as Error)
            } catch {
              /* a failure's report never stops the read queued after it */
            }
          }
        } while (this.again && !this.stopped)
      } finally {
        this.inflight = null
      }
    })()
    return this.inflight
  }

  /**
   * Ends the reads, as the page that asked for them goes: one queued is never made and none is asked again. The one under way runs to
   * its end (its caller aborts it), and what it settles starts nothing after it: stopped before that abort, so the abort's settling
   * cannot start the queued read.
   */
  stop(): void {
    this.stopped = true
    this.again = false
  }
}

/** A read of the list that failed is made again after this, doubled for each one more that fails, up to DECISION_RETRY_CAP_MS. */
export const DECISION_RETRY_FIRST_MS = 1000
export const DECISION_RETRY_CAP_MS = 30_000

/**
 * The app's store of the open and answered decisions. A read that fails keeps what was read before and is said (`error`, `staleSince`:
 * Needs you and the palette say the list is out of date since then), and is made again by itself after a capped backoff until one
 * succeeds (Codex's round 3 on #719): with the stream connected no other read may come, and the event that asked for the failed one (a
 * gate's opening) is not sent again, so the list never stays silently short.
 */
export class DecisionsLive {
  /** The open and answered decisions, oldest first. */
  decisions: Decision[] = []
  loaded = false
  /** The open or the answered decisions were more than DECISION_PAGES pages: the oldest past them are not here. */
  cut = false
  error = ''
  /** When the first read that failed since the last that succeeded was made (ms since the epoch); null while the last read succeeded. */
  staleSince: number | null = null
  /** The pages' counter: bumped after every change of the store. */
  changed: () => void = () => {}
  /** The clock, and the timer a failed read's retry is set on (it answers the timer's cancel): seams a test drives. */
  clock: () => number = () => Date.now()
  schedule: (f: () => void, ms: number) => () => void = (f, ms) => {
    const t = setTimeout(f, ms)
    return () => clearTimeout(t)
  }
  private reads: DecisionReads | null = null
  /** The reads in a row that failed, and the cancel of the retry set after the last of them. */
  private misses = 0
  private retry: (() => void) | null = null
  private readonly serial = new SerialReads(
    () => this.once(),
    (e) => {
      this.error = e.message
      this.staleSince ??= this.clock()
      this.misses++
      this.retry?.()
      this.retry = this.schedule(
        () => {
          this.retry = null
          void this.read()
        },
        Math.min(DECISION_RETRY_CAP_MS, DECISION_RETRY_FIRST_MS * 2 ** (this.misses - 1)),
      )
      this.changed()
    },
  )
  private watchers = new Set<DecisionWatcher>()

  attach(r: DecisionReads) {
    this.reads = r
  }

  /** A page's watcher of the stream; the function it answers ends it. */
  subscribe(w: DecisionWatcher): () => void {
    this.watchers.add(w)
    return () => this.watchers.delete(w)
  }

  /**
   * One event of the events stream, whatever it is: a `decision` event reads the list again and tells the watchers which decision
   * changed; a `snapshot` (the stream connected again) reads it all again; any other event is none of this store's.
   */
  stream(event: string, payload: unknown) {
    if (event === 'decision') {
      const e = asDecisionEvent(payload)
      if (!e) return
      void this.read()
      this.tell(e)
    } else if (event === 'snapshot') {
      void this.read()
      this.tell(null, true)
    }
  }

  /** The stream is down: read on the sessions' cadence (useAttention.ts), as the sessions are. */
  down() {
    void this.read()
    this.tell(null)
  }

  private tell(e: DecisionEvent | null, restarted = false) {
    for (const w of this.watchers) {
      try {
        w(e, restarted)
      } catch {
        /* a page's watcher never stops the others */
      }
    }
  }

  /** Reads the open and the answered decisions, one read at a time (SerialReads): a read that fails leaves what was read before. */
  read(): Promise<void> {
    if (!this.reads) return Promise.resolve()
    return this.serial.run()
  }

  private async once() {
    const reads = this.reads
    if (!reads) return
    const [open, answered] = await Promise.all([readAll(reads, 'open'), readAll(reads, 'answered')])
    this.decisions = [...open.decisions, ...answered.decisions].sort((a, b) => a.createdAt.localeCompare(b.createdAt) || a.id.localeCompare(b.id))
    this.cut = open.cut || answered.cut
    this.loaded = true
    this.error = ''
    this.staleSince = null
    this.misses = 0
    this.retry?.()
    this.retry = null
    this.changed()
  }

  /** The open ones. */
  open(): Decision[] {
    return this.decisions.filter((d) => d.state === 'open')
  }
}

let live: DecisionsLive | undefined

/** The app's one store of decisions. */
export function decisionsLive(): DecisionsLive {
  return (live ??= new DecisionsLive())
}

/** The decisions the operator has before them: the open and answered ones, live. `start` reads them once the layout is up. */
export function useDecisions() {
  const { request } = useApi()
  const admin = useWorkbenchToken()
  const version = useState('decisionsVersion', () => 0)
  const store = decisionsLive()
  store.attach({
    list: async (state, after) => {
      // Only the operator reads them: without the workbench token there is nothing to read (and no prompt for one).
      if (!admin.token.value) return { decisions: [] }
      const r = await request<{ decisions?: Decision[]; next?: string }>('/api/decisions', { query: after ? { state, after } : { state } })
      return { decisions: r.decisions ?? [], next: r.next }
    },
  })
  store.changed = () => version.value++

  const decisions = computed(() => {
    void version.value
    return store.decisions
  })
  const open = computed(() => decisions.value.filter((d) => d.state === 'open'))
  /** Whether the list is cut (more than DECISION_PAGES pages of a state): the rest are on their run and ticket pages. */
  const cut = computed(() => {
    void version.value
    return store.cut
  })
  /** The last read failed: since when the list is out of date, and why; it is read again by itself until a read succeeds. */
  const stale = computed(() => {
    void version.value
    return { since: store.staleSince, error: store.error }
  })
  function start() {
    if (import.meta.client) void store.read()
  }
  return { decisions, open, cut, stale, start, read: () => store.read() }
}

/** The card the app shows in its modal: the decision's id, or '' for none. One for the app (DecisionModal.vue in the layout). */
export function useDecisionModal() {
  const id = useState<string>('decisionModal', () => '')
  return {
    id,
    open: (d: string) => {
      if (isDecisionId(d)) id.value = d
    },
    close: () => (id.value = ''),
  }
}

/**
 * The record a card shows, ordered by the record's revision (Codex's round 1 on #719): a read's or a write's reply replaces it only
 * when it is of the decision shown and no older than it (`replaces`), so a reply delayed past a newer one never brings an older
 * record back, whichever of a read and a write answers last. A read of another decision than the one now asked for is dropped. The
 * reads come one at a time (SerialReads), so a read's reply of the same revision (a continuation names another run, an evidence's
 * resolution changed) is always the later one and is taken (Codex's round 2).
 *
 * The stream come back (Codex's round 2): the store may have been restored from a backup whose revisions are below the one shown, so
 * `reset` starts a new generation, as DispatchOrder.reset does on the same `snapshot`. Every reply carries the generation its read or
 * its write was started in (a write's from `begin`), and one of an older generation is dropped whenever it comes, before or after the
 * restored store's first read (Codex's round 3: a write sent to the old store answered after that read, at a revision above the
 * restored one, and was taken). The first reply of the new generation, a read's or a write's, is taken whatever its revision; the
 * revisions order the rest.
 */
export class DecisionRecordView {
  decision: Decision | null = null
  refs: DecisionRef[] = []
  /** Bumped by `reset` and `clear`: a read asked in an earlier generation is dropped. */
  generation = 0
  /** Since `reset`, no reply was taken: the record shown is not ordered against the store's revisions until one is. */
  private restored = false

  /** A read's reply, the record whole, asked in generation `gen`; false when it was dropped. */
  read(gen: number, asked: string, now: string, r: { decision: Decision; refs?: DecisionRef[] }): boolean {
    if (gen !== this.generation || asked !== now || r.decision.id !== now) return false
    if (!this.restored && !replaces(this.decision, r.decision, true)) return false
    this.restored = false
    this.decision = r.decision
    this.refs = r.refs ?? []
    return true
  }

  /**
   * A write's reply (the record without its history and responses, which the shown one's fill), the write started in generation `gen`;
   * false when it was dropped.
   */
  write(gen: number, now: string, d: Decision): boolean {
    if (gen !== this.generation || d.id !== now) return false
    if (!this.restored && !replaces(this.decision, d, false)) return false
    this.restored = false
    const same = this.decision?.id === d.id
    this.decision = { ...d, history: d.history ?? (same ? this.decision?.history : undefined), responses: d.responses ?? (same ? this.decision?.responses : undefined) }
    return true
  }

  /** A new generation for a store that may have been restored: what is shown stays until the next read replaces it, whatever its revision. */
  reset() {
    this.generation++
    this.restored = this.decision !== null
  }

  /** Another decision is asked for: nothing is shown, and a read asked before is dropped. */
  clear() {
    this.generation++
    this.restored = false
    this.decision = null
    this.refs = []
  }
}

/**
 * One decision whole (`GET /api/decisions/{id}`, with its evidence's resolutions), read when the id is set, again on its own `decision`
 * event, when the stream comes back (a new generation: DecisionRecordView.reset) and on its fallback's cadence while it is down, and
 * after each of the card's writes
 * (`read`); a write's reply is taken through `begin`, called as the write starts. The reads come one at a time (SerialReads); what is shown is what DecisionRecordView takes.
 */
export function useDecisionRecord(id: () => string) {
  const { request } = useApi()
  const view = new DecisionRecordView()
  const decision = ref<Decision | null>(null)
  const refs = ref<DecisionRef[]>([])
  const error = ref('')
  const loading = ref(false)
  function show() {
    decision.value = view.decision
    refs.value = view.refs
  }
  const reads = new SerialReads(async () => {
    const d = id()
    if (!d) return
    const gen = view.generation
    loading.value = true
    try {
      const r = await request<{ decision: Decision; refs?: DecisionRef[] }>(`/api/decisions/${encodeURIComponent(d)}`)
      if (view.read(gen, d, id(), r)) show()
      if (d === id()) error.value = ''
    } catch (e) {
      if (d === id()) error.value = (e as Error).message
    } finally {
      loading.value = false
    }
  })
  function read(): Promise<void> {
    return reads.run()
  }
  /**
   * A write starts: what takes its reply, the decision it answered with, unless a newer revision is shown already or the stream came
   * back since it started (its reply is then the old store's: DecisionRecordView.write).
   */
  function begin(): (d: Decision) => void {
    const gen = view.generation
    return (d) => {
      if (view.write(gen, id(), d)) show()
    }
  }
  const stop = decisionsLive().subscribe((e, restarted) => {
    if (restarted) view.reset()
    if (!e || e.id === id()) void read()
  })
  watch(
    id,
    (d) => {
      if (view.decision?.id !== d) {
        view.clear()
        show()
        error.value = ''
      }
      if (d) void read()
    },
    { immediate: true },
  )
  onBeforeUnmount(stop)
  return { decision, refs, error, loading, read, begin }
}

/**
 * Whether the run that holds a decision runs here now, for its card (runHeldOf): the live store's run while the store holds it, else the
 * run read through its own route (`refreshRun`: a record interrupted at a restart, which the live store leaves out, or null for one the
 * server does not have; Codex's round 1 on #719); undefined while neither is known. What was read stands only while the store does not
 * hold the run (Codex's round 2): the store taking it forgets the read, the store letting it go (a restart that left the run a record)
 * reads it again, and the reply of a read asked before either is dropped, so a run read as running before a restart is never what the
 * card judges by after it.
 *
 * A page that provides its own access (REF_ACCESS with `runHeld`: a decision link's join page, which reads nothing through the API)
 * answers it instead, and nothing is read here.
 */
export function useDecisionRun(run: () => string) {
  const given = hasInjectionContext() ? inject(REF_ACCESS, null) : null
  const held = given?.runHeld
  if (held) return computed(() => held(run()))
  const live = useAttention()
  const read = ref<{ id: string; run: RunInfo | null } | null>(null)
  let asked = 0
  watch(
    () => [run(), !!live.runOf(run())] as const,
    async ([id, held]) => {
      const mine = ++asked
      read.value = null
      if (held || !id) return
      try {
        const r = await live.refreshRun(id)
        if (mine === asked) read.value = { id, run: r }
      } catch {
        /* not known: the card says what it can without it */
      }
    },
    { immediate: true },
  )
  return computed(() => runHeldOf(live.runOf(run()) ?? (read.value?.id === run() ? read.value.run : undefined)))
}
