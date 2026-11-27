import { ApiError } from './useApi'
import { SerialReads, decisionsLive } from './useDecisions'
import { type DispatchWatch, dispatchLive } from './useDispatchLive'
import { RUN_CHANGING_RETRY_MS, gather } from '~/utils/dispatchEvidence'
import { comparisonRuns, type Comparison } from '~/utils/compare'

/**
 * The comparison of a run (Program item CP1, docs/plans/compare-crew.md, PR-4): GET /api/runs/{run}/comparison, any run of the chain,
 * one fetch at a time (SerialReads: a read asked while one is out is made once after it, so the replies come in the order they were
 * asked). It is read when the run is named and read again on:
 *
 * - the run's change: a revision of the run asked, of the chain's launched run or of a candidate's run, as the live store holds them
 *   (useAttention's runs, read on each `run` event);
 * - a `dispatch` event naming its ticket (by the ticket's id, as a ticket page watches it);
 * - a `brakeman` event naming the comparison's request (`supporting.brakeman.request`, PR-5), as the live store of Brakeman's
 *   requests moves it: the run sits idle while it selects and its ticket does not change, so without this a draft that arrived would
 *   stay "queued";
 * - a `decision` event (DR1's) naming the comparison's selection or a record of a run it is of (PR-7): an answer made on the card,
 *   the CLI or another client changes neither the run's events while the run is not held nor the ticket's, and without this an open
 *   comparison would keep the old selection;
 * - the event stream's `snapshot` (a reconnect: what was missed is unknown).
 *
 * A run of no comparison (404) is said, not asked again; a read refused while the chain moved through every read (503 run_changing,
 * Retry-After: 1) keeps what is shown and is asked again after the second; any other failure is said and asked again through the
 * live store's retry (DispatchLive: after the window, doubled), until one answers or a snapshot reads it.
 */
export function useComparison(o: { run: () => string; ticket?: () => string }) {
  const { request } = useApi()
  const attention = useAttention()
  const brakeman = useBrakeman()

  const comparison = ref<Comparison | null>(null)
  /** The last read's failure, '' once one answered. */
  const error = ref('')
  /** The server's words when the run is of no comparison (a 404); '' otherwise. */
  const missing = ref('')
  /** The last read was refused while the chain moved: what is shown stays, and it is read again. */
  const changing = ref(false)
  /** Whether a read has answered (a comparison, or a 404). */
  const loaded = ref(false)
  /** How many reads were sent (for the tests' "one fetch at a time"). */
  const sent = ref(0)

  async function once(): Promise<void> {
    const run = o.run()
    if (!run) return
    sent.value++
    try {
      const c = await request<Comparison>(`/api/runs/${encodeURIComponent(run)}/comparison`)
      if (run !== o.run()) return
      comparison.value = c
      missing.value = ''
      error.value = ''
      changing.value = false
      loaded.value = true
    } catch (e) {
      if (run !== o.run()) return
      if (e instanceof ApiError && e.status === 404) {
        comparison.value = null
        missing.value = e.message || 'This run is not a trial.'
        error.value = ''
        changing.value = false
        loaded.value = true
        return
      }
      if (e instanceof ApiError && e.status === 503 && e.code === 'run_changing') {
        changing.value = true
        retry.ask()
        return
      }
      changing.value = false
      error.value = (e as Error).message || 'The trial could not be read.'
    }
  }
  const serial = new SerialReads(once)
  /** One read (or the one after the read under way); rejects when it failed, so the live store asks again. */
  function read(): Promise<void> {
    return serial.run().then(() => {
      if (error.value) throw new Error(error.value)
    })
  }
  const watch_: DispatchWatch = {
    now: (e) => !!o.ticket?.() && e.ticket === o.ticket?.(),
    later: () => false,
    read,
  }
  const unwatch = dispatchLive().subscribe(watch_)
  /** A read the page asks of itself: a failure is asked again as the live watch's is. */
  function ask(): void {
    dispatchLive().ask(watch_, read)
  }
  // A read refused while the chain moved is asked again after the second through the live store, so a failure of that read is asked
  // again as any other's is (after the window, doubled).
  const retry = gather(async () => ask(), RUN_CHANGING_RETRY_MS)

  // Another run named: what was shown was the other's.
  watch(
    o.run,
    (run, before) => {
      if (before !== undefined && run !== before) {
        comparison.value = null
        missing.value = ''
        error.value = ''
        changing.value = false
        loaded.value = false
      }
      if (run) ask()
    },
    { immediate: true },
  )

  // The run's change: a revision (or a continuation) of any run the comparison is of, as the live store holds them.
  const revisions = computed(() => {
    const out: Record<string, string> = {}
    for (const id of comparisonRuns(o.run(), comparison.value)) {
      const r = attention.runOf(id)
      if (r) out[id] = `${r.managed?.revision ?? 0}:${r.state}:${r.resumedBy ?? ''}:${r.stoppedAt ?? ''}`
    }
    return out
  })
  watch(revisions, (now, before) => {
    if (Object.entries(now).some(([id, v]) => before[id] !== undefined && before[id] !== v)) ask()
  })

  // Brakeman's request for the comparison, as the live store of requests moves it.
  const request_ = computed(() => comparison.value?.supporting?.brakeman?.request ?? '')
  const requestState = computed(() => {
    const id = request_.value
    if (!id) return ''
    return `${id}|${brakeman.requests.value.find((r) => r.id === id)?.state ?? ''}`
  })
  watch(requestState, (now, before) => {
    if (!now || !before) return
    if (now.split('|')[0] === before.split('|')[0] && now !== before) ask()
  })

  // DR1's `decision` events: the selection, or any record of a run the comparison is of (a new selection opened, one closed).
  const undecide = decisionsLive().subscribe((e) => {
    if (!e || !o.run()) return
    const c = comparison.value
    if (e.id === c?.selection?.decision || comparisonRuns(o.run(), c).includes(e.run)) ask()
  })

  onBeforeUnmount(() => {
    serial.stop()
    retry.stop()
    unwatch()
    undecide()
  })

  return { comparison, error, missing, changing, loaded, sent, read: ask }
}
