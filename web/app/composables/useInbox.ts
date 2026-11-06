import { effectScope, watch } from 'vue'
import { ApiError } from './useApi'
import {
  addRecent,
  barCount,
  inboxOrder,
  mergeRecent,
  readRecentActions,
  reconcileDelivery,
  stepsFinal,
  trackKey,
  withOutcome,
  withoutRecent,
  writeRecentActions,
  type DeliveryStep,
  type InboxPage,
  type InboxSummary,
  type InboxTrack,
  type RecentEntry,
} from '~/utils/inbox'

/** What the live inbox reads: a page of the list from a cursor, and one recent action's steps. */
export interface InboxReads {
  list: (after?: string) => Promise<InboxPage>
  track: (e: RecentEntry) => Promise<InboxTrack>
}

/** The inbox as the page and the bar show it. */
export interface InboxState {
  /** The open items read, oldest first: as many pages as the page has asked for (More). */
  items: InboxSummary[]
  /** Every open item, not the pages' (the bar's number, "Showing 150 of 212"). */
  total: number
  /** The cursor of the next page when more follow, else ''. */
  next: string
  /** A read has answered since the start (or the last token). */
  loaded: boolean
  /** The last read's failure in words, '' once one answers. */
  error: string
  /** Why the inbox is not served here ('' when it is): a switchyard answers decisions where the work runs. */
  unavailable: string
  /** This browser's recent actions, newest first. */
  recent: RecentEntry[]
  /** Each recent action's four steps as last read and reconciled, by trackKey. */
  tracks: Record<string, DeliveryStep[]>
}

export function emptyInbox(): InboxState {
  return { items: [], total: 0, next: '', loaded: false, error: '', unavailable: '', recent: [], tracks: {} }
}

/**
 * The live inbox (docs/plans/decision-inbox.md, "Events"): read again on the events that may change it (`session`, `removed`, `run`,
 * `dispatch`, `brakeman`, which useAttention hands it), on the stream's `snapshot` (a stream that comes back replays nothing it
 * missed), on the stream-down cadence and on the page's return to view; at most one read in flight and one queued behind it, so a
 * burst of events costs two reads. While a page tracks (the decision page), each read of the list also reads again the steps of
 * every recent action not yet final, so a step that moves with no change to the open list (a retried task's next attempt starting)
 * is seen. Nothing is read before reads are attached, nor while `ready` says no (no workbench token).
 *
 * What the page knows of an answer (and of a command or an operation) is kept in its recent entry, and only what is known is drawn:
 *
 * - sent: the entry is remembered before the request goes, with no `outcome`; its steps are the server's track alone.
 * - confirmed by the owner (`accepted`): the owner's own word on this request (an answer's `answer_ok`, a receipt); for a hosted
 *   session's question, whose recorded and validated steps the server cannot know, that word fills them (reconcileDelivery).
 * - refused by the owner (`refused: <words>`): the owner's own refusal; the action's steps end there, resumed not applicable.
 * - unknown (`unconfirmed`): the request went and no word will come: an older owner that never confirms an answer, a connection
 *   lost or silent before its word. Nothing is filled: the steps stay as the server's track says, unknown where it cannot know, and
 *   the page says so and offers to read again, never to send again by itself.
 * - not sent: the request never left the browser (a reply from this tab already under way, no connection): its entry is dropped
 *   (`forget`), for it is no action of this browser's.
 *
 * The recent list the page follows is held in memory; the browser's storage keeps it across reloads where it can, and a storage that
 * is empty or throws loses none of the actions the page follows (mergeRecent).
 */
export class InboxLive {
  private state: InboxState = emptyInbox()
  private reads: InboxReads | null = null
  private storage: () => Pick<Storage, 'getItem' | 'setItem'> | null = () => null
  private apply: (s: InboxState) => void = () => {}
  private ready: () => boolean = () => true
  private running: Promise<void> | null = null
  private again = false
  /** Drops what a read sent before a reset brings back. */
  private generation = 0
  /** How many pages of the list are shown (More adds one). */
  private pages = 1
  /** How many pages tracking asks: a page on screen tracks its recent actions. */
  private trackers = 0
  /** How many reads of the list were sent, for the tests. */
  listReads = 0

  /** Hands the live inbox its reads, its storage and where its state goes. */
  attach(reads: InboxReads, o: { storage?: () => Pick<Storage, 'getItem' | 'setItem'> | null; apply?: (s: InboxState) => void; ready?: () => boolean } = {}): void {
    this.reads = reads
    if (o.storage) this.storage = o.storage
    if (o.apply) this.apply = o.apply
    if (o.ready) this.ready = o.ready
    this.state = { ...this.state, recent: mergeRecent(this.state.recent, readRecentActions(this.storage())) }
    this.publish()
  }

  get current(): InboxState {
    return this.state
  }

  private publish() {
    this.apply({ ...this.state })
  }

  /** An event that may change the inbox. */
  event(): Promise<void> {
    return this.changed()
  }

  /** The stream started over: everything is read again. */
  snapshot(): Promise<void> {
    return this.changed()
  }

  /** Reads the inbox again: one read in flight, one queued behind it; resolves once the reads asked so far have answered. */
  changed(): Promise<void> {
    if (!this.reads || !this.ready()) return Promise.resolve()
    if (this.running) {
      this.again = true
      return this.running
    }
    this.running = (async () => {
      do {
        this.again = false
        await this.readOnce()
      } while (this.again)
    })().finally(() => {
      this.running = null
    })
    return this.running
  }

  /** One more page of the list (More), read with the rest. */
  more(): Promise<void> {
    if (this.state.next) this.pages++
    return this.changed()
  }

  /** A page tracks its recent actions while it is shown; the function returned stops it. */
  track(): () => void {
    this.trackers++
    void this.changed()
    let done = false
    return () => {
      if (done) return
      done = true
      this.trackers--
    }
  }

  /** Another workbench token, or none: nothing read through the old one stays; this browser's recent actions do. */
  reset(): void {
    this.generation++
    this.pages = 1
    this.state = { ...emptyInbox(), recent: mergeRecent(this.state.recent, readRecentActions(this.storage())) }
    this.publish()
  }

  private async readOnce(): Promise<void> {
    const reads = this.reads!
    const gen = this.generation
    this.listReads++
    try {
      const got: InboxSummary[] = []
      let after: string | undefined
      let total = 0
      let next = ''
      for (let i = 0; i < this.pages; i++) {
        const p = await reads.list(after)
        got.push(...p.items)
        total = p.total
        next = p.next ?? ''
        if (!p.next) break
        after = p.next
      }
      if (gen !== this.generation) return
      const seen = new Set<string>()
      const items = inboxOrder(got.filter((s) => !seen.has(s.ref) && seen.add(s.ref)))
      this.state = { ...this.state, items, total, next, loaded: true, error: '', unavailable: '' }
    } catch (e) {
      if (gen !== this.generation) return
      if (e instanceof ApiError && e.status === 403 && e.code === 'switchyard') this.state = { ...this.state, items: [], total: 0, next: '', loaded: true, unavailable: e.message, error: '' }
      else this.state = { ...this.state, error: (e as Error).message || 'The inbox could not be read' }
    }
    this.publish()
    if (this.trackers > 0) await this.readTracks(gen)
  }

  /** Reads again the steps of every recent action not yet final. */
  private async readTracks(gen: number): Promise<void> {
    const recent = mergeRecent(this.state.recent, readRecentActions(this.storage()))
    this.state = { ...this.state, recent }
    for (const e of recent) {
      const key = trackKey(e)
      if (stepsFinal(this.state.tracks[key])) continue
      await this.readTrack(e, gen)
    }
    this.publish()
  }

  private async readTrack(e: RecentEntry, gen: number): Promise<void> {
    try {
      const t = await this.reads!.track(e)
      if (gen !== this.generation) return
      const key = trackKey(e)
      const outcome = this.state.recent.find((x) => trackKey(x) === key)?.outcome ?? e.outcome
      this.state = { ...this.state, tracks: { ...this.state.tracks, [key]: reconcileDelivery(this.state.tracks[key], t.steps, outcome) } }
    } catch {
      /* read again with the next read of the list */
    }
  }

  /** Reads one action's steps now (after its reply came back), whatever the list. */
  async trackNow(e: RecentEntry): Promise<void> {
    if (!this.reads) return
    await this.readTrack(e, this.generation)
    this.publish()
  }

  /** Keeps an action in this browser's recent list, before its reply comes back: a reload while it is out still tracks it. */
  remember(e: RecentEntry): void {
    const recent = addRecent(mergeRecent(this.state.recent, readRecentActions(this.storage())), e)
    writeRecentActions(this.storage(), recent)
    this.state = { ...this.state, recent }
    this.publish()
  }

  /** Drops an action whose request never left the browser: nothing was sent, so it is no action of this browser's. */
  forget(e: Pick<RecentEntry, 'ref' | 'receipt' | 'attempt'>): void {
    const recent = withoutRecent(mergeRecent(this.state.recent, readRecentActions(this.storage())), e)
    writeRecentActions(this.storage(), recent)
    const tracks = { ...this.state.tracks }
    delete tracks[trackKey(e)]
    this.state = { ...this.state, recent, tracks }
    this.publish()
  }

  /** Writes the owner's word on an action into its entry, when it comes (ReplyWord). */
  outcome(receipt: string, attempt: string | undefined, word: string): void {
    const recent = withOutcome(mergeRecent(this.state.recent, readRecentActions(this.storage())), receipt, attempt, word)
    writeRecentActions(this.storage(), recent)
    const key = trackKey({ ref: recent.find((x) => x.receipt === receipt && x.attempt === attempt)?.ref ?? '', receipt, attempt })
    const steps = this.state.tracks[key]
    this.state = { ...this.state, recent, tracks: steps ? { ...this.state.tracks, [key]: reconcileDelivery(steps, steps, word) } : this.state.tracks }
    this.publish()
  }
}

let live: InboxLive | undefined
let listening = false
/** The app's own scope for what start installs once: the token's watcher lives as long as the app, not as the page that started it. */
let appScope: ReturnType<typeof effectScope> | undefined

/** The app's one live inbox. */
export function inboxLive(): InboxLive {
  return (live ??= new InboxLive())
}

/**
 * The decision inbox for the whole app: the open items and their count (the bar's Decide tab, the sidebar's row, the palette), and
 * this browser's recent actions with their steps (the decision page). `start` attaches the reads once, through the client calls of
 * useSessions; the events come from useAttention's stream.
 */
export function useInbox() {
  const state = useState<InboxState>('inboxState', () => emptyInbox())
  const admin = useWorkbenchToken()
  const api = useSessions()
  const l = inboxLive()

  function start() {
    if (!import.meta.client || listening) return
    listening = true
    l.attach(
      { list: (after) => api.inbox(after), track: (e) => api.inboxTrack(e.track ?? e.ref, e.receipt, e.attempt) },
      {
        storage: () => {
          try {
            return localStorage
          } catch {
            return null
          }
        },
        apply: (s) => (state.value = s),
        ready: () => !!admin.token.value,
      },
    )
    // A phone back from suspension: the stream may have missed what changed meanwhile.
    document.addEventListener('visibilitychange', () => {
      if (document.visibilityState === 'visible') void l.changed()
    })
    // Whichever page or layout starts the inbox first, the watcher outlives it: a token forgotten or replaced on any page later
    // still drops what was read through the old one.
    appScope ??= effectScope(true)
    appScope.run(() =>
      watch(
        () => admin.token.value,
        () => {
          l.reset()
          void l.changed()
        },
      ),
    )
    void l.changed()
  }

  const count = computed(() => (state.value.unavailable ? 0 : barCount(state.value.loaded ? state.value : null)))

  return { state, count, start, live: l, changed: () => l.changed(), more: () => l.more() }
}
