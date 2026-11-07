import type { DecisionEvent } from '~/utils/protocol'
import type { Decision, DecisionRef } from '~/utils/decisions'
import { isDecisionId } from '~/utils/decisions'

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

/** What the store reads: one state's page of the operator's list, newest first. */
export interface DecisionReads {
  list: (state: 'open' | 'answered') => Promise<Decision[]>
}

/** What a page that shows decisions is told: a decision changed (`e`), or the stream came back or is down (null: read it all again). */
export type DecisionWatcher = (e: DecisionEvent | null) => void

export class DecisionsLive {
  /** The open and answered decisions, oldest first. */
  decisions: Decision[] = []
  loaded = false
  error = ''
  /** The pages' counter: bumped after every change of the store. */
  changed: () => void = () => {}
  private reads: DecisionReads | null = null
  private inflight: Promise<void> | null = null
  private again = false
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
      this.tell(null)
    }
  }

  /** The stream is down: read on the sessions' cadence (useAttention.ts), as the sessions are. */
  down() {
    void this.read()
    this.tell(null)
  }

  private tell(e: DecisionEvent | null) {
    for (const w of this.watchers) {
      try {
        w(e)
      } catch {
        /* a page's watcher never stops the others */
      }
    }
  }

  /**
   * Reads the open and the answered decisions. One read at a time: one asked while another is under way is made once after it, so a
   * burst of events reads twice at most and the last read always starts after the last event.
   */
  read(): Promise<void> {
    if (!this.reads) return Promise.resolve()
    if (this.inflight) {
      this.again = true
      return this.inflight
    }
    const reads = this.reads
    this.inflight = (async () => {
      try {
        do {
          this.again = false
          const [open, answered] = await Promise.all([reads.list('open'), reads.list('answered')])
          this.decisions = [...open, ...answered].sort((a, b) => a.createdAt.localeCompare(b.createdAt) || a.id.localeCompare(b.id))
          this.loaded = true
          this.error = ''
          this.changed()
        } while (this.again)
      } catch (e) {
        this.error = (e as Error).message
        this.changed()
      } finally {
        this.inflight = null
      }
    })()
    return this.inflight
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
    list: async (state) => {
      // Only the operator reads them: without the workbench token there is nothing to read (and no prompt for one).
      if (!admin.token.value) return []
      const r = await request<{ decisions?: Decision[] }>('/api/decisions', { query: { state } })
      return r.decisions ?? []
    },
  })
  store.changed = () => version.value++

  const decisions = computed(() => {
    void version.value
    return store.decisions
  })
  const open = computed(() => decisions.value.filter((d) => d.state === 'open'))
  function start() {
    if (import.meta.client) void store.read()
  }
  return { decisions, open, start, read: () => store.read() }
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
 * One decision whole (`GET /api/decisions/{id}`, with its evidence's resolutions), read when the id is set, again on its own `decision`
 * event and when the stream comes back, and after each of the card's writes (`read`). Only the last read started is shown.
 */
export function useDecisionRecord(id: () => string) {
  const { request } = useApi()
  const decision = ref<Decision | null>(null)
  const refs = ref<DecisionRef[]>([])
  const error = ref('')
  const loading = ref(false)
  let seq = 0
  async function read(): Promise<void> {
    const d = id()
    const mine = ++seq
    if (!d) {
      decision.value = null
      refs.value = []
      error.value = ''
      return
    }
    loading.value = true
    try {
      const r = await request<{ decision: Decision; refs?: DecisionRef[] }>(`/api/decisions/${encodeURIComponent(d)}`)
      if (mine !== seq) return
      decision.value = r.decision
      refs.value = r.refs ?? []
      error.value = ''
    } catch (e) {
      if (mine === seq) error.value = (e as Error).message
    } finally {
      if (mine === seq) loading.value = false
    }
  }
  /** Takes a decision a write answered with: it is the newest there is. */
  function apply(d: Decision) {
    if (d.id !== id()) return
    seq++
    decision.value = { ...d, history: d.history ?? decision.value?.history, responses: d.responses ?? decision.value?.responses }
    loading.value = false
  }
  const stop = decisionsLive().subscribe((e) => {
    if (!e || e.id === id()) void read()
  })
  watch(id, () => void read(), { immediate: true })
  onBeforeUnmount(stop)
  return { decision, refs, error, loading, read, apply }
}
