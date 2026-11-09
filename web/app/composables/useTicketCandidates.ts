import { ApiError } from './useApi'
import type { TaskCandidates, TicketLink, TicketRunEntry } from './useDispatch'
import { DISPATCH_RETRY_MAX_MS } from './useDispatchLive'
import { parseCandidate } from '~/utils/dispatch'
import { throughOrAbsent, type DispatchOrder } from '~/utils/dispatchOrder'
import { RUN_CHANGING_RETRY_MS, entryRunId, gather } from '~/utils/dispatchEvidence'
import { RUN_EVENT_WINDOW_MS } from '~/utils/runs'

/** The candidates pages read of one run at most: a crew's 128 tasks, 8 a page. */
const MAX_PAGES = 16

/** A run the ticket holds, as the delivery form and the Decisions read its candidates: its id, its name, what moves it. */
export interface HeldRun {
  id: string
  label: string
  /** Changes whenever the run may have new candidates: its summary's state and tasks, the live store's revision of it. */
  signature: string
  /**
   * Read only because a delivery names one of its candidates, its run not on the pages of Runs read: for whether that candidate was
   * withdrawn since, never offered to a delivery or counted in the Decisions, which are of the runs shown.
   */
  linked?: true
  /** When the run started, as its summary says: the Review view's ordering of candidates (rankCandidates, RV1). */
  startedAt?: string
}

/**
 * The runs of a ticket's Runs whose candidates the delivery form and the Decisions read (PR-12): every entry's run but a contested
 * chain's, which no ticket owns (a delivery naming one of its candidates is refused), each once, in the section's order, with what moves
 * it: its summary's state, pause, outcome, stop and task counts, and the live store's revision of it. Then the run of each live delivery
 * link's candidate that no entry read holds (`linked`): Runs reads its pages one at a time, and a delivery of a run on a later page is
 * still known withdrawn.
 */
export function heldRuns(
  entries: readonly TicketRunEntry[],
  revision: (id: string) => number | undefined,
  links: readonly Pick<TicketLink, 'kind' | 'candidate' | 'deletedAt'>[] = [],
): HeldRun[] {
  const out: HeldRun[] = []
  const seen = new Set<string>()
  for (const e of entries) {
    const id = entryRunId(e)
    if (!id || seen.has(id)) continue
    seen.add(id)
    if (e.contested) continue
    const s = e.run
    out.push({
      id,
      label: s?.label || e.launch?.label || '',
      signature: JSON.stringify([revision(id) ?? 0, s ? [s.state, s.paused, s.outcome ?? '', s.stoppedAt ?? '', s.tasks] : null]),
      ...(s?.startedAt ? { startedAt: s.startedAt } : {}),
    })
  }
  for (const l of links) {
    if (l.kind !== 'delivery' || l.deletedAt) continue
    const id = parseCandidate(l.candidate)?.run
    if (!id || seen.has(id)) continue
    seen.add(id)
    out.push({ id, label: '', signature: JSON.stringify([revision(id) ?? 0, null]), linked: true })
  }
  return out
}

/**
 * The candidates of a ticket's runs (Program item DS2, docs/plans/dispatch-evidence.md, PR-12), for what a delivery may name, a
 * delivered candidate withdrawn since, and the Accept anyway receipts under Decisions: each run's every page (GET
 * /api/runs/{run}/candidates), read when the run is first shown and again, gathered for RUN_EVENT_WINDOW_MS, when its signature moves
 * (its summary read again on its run events, or the live store's revision of it); a run moving through every read (503 run_changing)
 * is asked again after the second its Retry-After names. Any other failure is said (`error`) and asked again after the window, doubled
 * after each failure up to DISPATCH_RETRY_MAX_MS, as a page's re-read is, until one answers: an ended run never moves, so no event
 * would ask again. A failure is of the read that met it: an older read answering after it does not clear it, and a newer read's answer
 * does. A run hidden when its failure is asked again is forgotten, so it is read when it is shown again. A run the server knows nowhere
 * (a 404) has no candidates. A task whose attempts accepted anyway are not all among the six listed has its older candidates read too
 * (`acceptedOlder`), until those are found: the Decisions' Accept anyway rows. Every read goes through the page's ordering gate, by run;
 * a snapshot's reset of the gate drops the replies on their way, so `reset` reads every run again. Nothing polls.
 */
export function useTicketCandidates(o: { gate: DispatchOrder; key: () => string; runs: () => readonly HeldRun[] }) {
  const api = useDispatchApi()
  /** Each run's tasks with their candidates, as last read. */
  const byRun = shallowReactive(new Map<string, TaskCandidates[]>())
  /** Each run's base, as its candidates' first page names it: the Review view's default base (RV1). */
  const bases = shallowReactive(new Map<string, string>())
  /** By run: why its last read failed, until one answers. */
  const errors = shallowReactive(new Map<string, string>())
  /** The signature each run was last asked to be read at. */
  const asked = new Map<string, string>()
  /** The runs to read on the next gathered read. */
  const dirty = new Set<string>()
  /** The runs whose read failed, asked again when `failTimer` fires. */
  const failed = new Set<string>()
  /** By run: the number of its last read asked, the read whose failure is said, and the latest read that answered. */
  const sentReads = new Map<string, number>()
  const failedRead = new Map<string, number>()
  const answeredRead = new Map<string, number>()
  let failTimer: ReturnType<typeof setTimeout> | undefined
  /** The wait before the failed reads are asked again: the window, doubled after each failure up to DISPATCH_RETRY_MAX_MS. */
  let failDelay = RUN_EVENT_WINDOW_MS
  let stopped = false
  /** The reads on their way: `loading` while any is. */
  const pending = ref(0)
  const loading = computed(() => pending.value > 0)

  function askFailedAgain(): void {
    if (stopped || failTimer !== undefined) return
    const wait = failDelay
    failDelay = Math.min(failDelay * 2, DISPATCH_RETRY_MAX_MS)
    failTimer = setTimeout(() => {
      failTimer = undefined
      const held = new Set(o.runs().map((r) => r.id))
      for (const id of failed) {
        if (held.has(id)) dirty.add(id)
        // Not shown now: forgotten, so it is read at once when it is shown again, whatever its signature.
        else asked.delete(id)
      }
      failed.clear()
      void readDirty()
    }, wait)
  }
  /** Read n of the run answered: its failure is cleared unless a newer read met it, which stands and is asked again. */
  function answered(id: string, n: number): void {
    answeredRead.set(id, Math.max(answeredRead.get(id) ?? 0, n))
    if ((failedRead.get(id) ?? 0) > n) return
    errors.delete(id)
    failed.delete(id)
    failedRead.delete(id)
    if (!errors.size) failDelay = RUN_EVENT_WINDOW_MS
  }
  /** Read n of the run failed: said and asked again, unless a newer read answered already. */
  function failedOn(id: string, n: number, why: string): void {
    if ((answeredRead.get(id) ?? 0) > n) return
    errors.set(id, why)
    failedRead.set(id, Math.max(failedRead.get(id) ?? 0, n))
    failed.add(id)
    askFailedAgain()
  }

  /** The task's older candidates, until every attempt accepted anyway that its page did not list is found, or none is older. */
  async function readOlder(id: string, t: TaskCandidates): Promise<TaskCandidates> {
    let want = t.acceptedOlder ?? 0
    const out = [...t.candidates]
    while (want > 0) {
      const before = Math.min(...out.map((c) => c.attempt))
      const older = (await api.olderCandidates(id, t.task, before)).candidates?.find((x) => x.task === t.task)
      const found = (older?.candidates ?? []).filter((c) => c.attempt < before)
      // Each page is older than the one before: none, and there is nothing older to read.
      if (!found.length) break
      out.push(...found)
      want -= found.filter((c) => c.acceptedAnyway).length
      if (!older!.more) break
    }
    return { ...t, candidates: out }
  }

  async function readRun(id: string): Promise<void> {
    const k = o.key()
    const n = (sentReads.get(id) ?? 0) + 1
    sentReads.set(id, n)
    try {
      await throughOrAbsent(
        o.gate,
        `delivery-candidates:${id}`,
        async () => {
          const tasks: TaskCandidates[] = []
          let base = ''
          let after = ''
          for (let i = 0; i < MAX_PAGES; i++) {
            const p = await api.runCandidates(id, after)
            base ||= p.base ?? ''
            for (const t of p.candidates ?? []) if (!tasks.some((x) => x.task === t.task)) tasks.push(t)
            if (!p.next) break
            after = p.next
          }
          for (const [i, t] of tasks.entries()) if ((t.acceptedOlder ?? 0) > 0 && t.candidates.length) tasks[i] = await readOlder(id, t)
          return { tasks, base }
        },
        {
          apply: ({ tasks, base }) => {
            if (k !== o.key()) return
            byRun.set(id, tasks)
            if (base) bases.set(id, base)
            answered(id, n)
          },
          absent: (e) => e instanceof ApiError && e.status === 404,
          // A run the server knows nowhere has no candidates to name.
          gone: () => {
            if (k !== o.key()) return
            byRun.set(id, [])
            answered(id, n)
          },
        },
      )
    } catch (e) {
      if (k !== o.key()) return
      if (e instanceof ApiError && e.status === 503 && e.code === 'run_changing') {
        dirty.add(id)
        retry.ask()
        return
      }
      // Another failure: what was read stays, the failure is said, and it is asked again until one answers.
      failedOn(id, n, (e as Error).message)
    }
  }

  async function readDirty(): Promise<void> {
    const ids = [...dirty]
    dirty.clear()
    if (!ids.length) return
    pending.value++
    try {
      await Promise.all(ids.map(readRun))
    } finally {
      pending.value--
    }
  }
  const reads = gather(readDirty, RUN_EVENT_WINDOW_MS)
  const retry = gather(readDirty, RUN_CHANGING_RETRY_MS)
  onBeforeUnmount(() => {
    stopped = true
    reads.stop()
    retry.stop()
    clearTimeout(failTimer)
  })

  /** The ticket whose runs' candidates these are. */
  let shownKey = o.key()
  /**
   * Another ticket on the page: nothing of the one before's stays, its replies on their way dropped, its failures not asked again. Its
   * key and its runs change one after the other, in either order, and whichever is seen first forgets, so a run the next ticket holds too
   * is read again for it, not taken for one read already.
   */
  function follow(): void {
    const k = o.key()
    if (k === shownKey) return
    shownKey = k
    for (const id of asked.keys()) o.gate.drop(`delivery-candidates:${id}`)
    byRun.clear()
    errors.clear()
    asked.clear()
    dirty.clear()
    failed.clear()
    failedRead.clear()
    answeredRead.clear()
    clearTimeout(failTimer)
    failTimer = undefined
    failDelay = RUN_EVENT_WINDOW_MS
  }

  /** Notes the runs shown at their signatures: the runs not read at them are read, a run seen first at once, a move gathered. */
  function sync(runs: readonly HeldRun[]): void {
    follow()
    let first = false
    for (const r of runs) {
      const before = asked.get(r.id)
      if (before === r.signature) continue
      asked.set(r.id, r.signature)
      dirty.add(r.id)
      if (before === undefined) first = true
    }
    // A run seen for the first time is read at once; a move of one already read is gathered with the others.
    if (first) void readDirty()
    else if (dirty.size) reads.ask()
  }
  watch(() => o.runs(), sync, { immediate: true })
  watch(() => o.key(), follow)

  /**
   * The stream started again (a snapshot): the page's gate forgot every thing, so a reply on its way is dropped, and every run shown is
   * read again at once, whatever its signature. What was read stays shown until the new reads answer.
   */
  function reset(): void {
    asked.clear()
    sync(o.runs())
  }

  /** The runs shown with their tasks, in the order given, for the form's choices and the Decisions: not a run read only for a delivery. */
  const runs = computed(() => o.runs().flatMap((r) => (r.linked ? [] : [{ run: r.id, label: r.label, ...(r.startedAt ? { startedAt: r.startedAt } : {}), tasks: byRun.get(r.id) ?? [] }])))
  /** The first run shown whose read failed, and why, in words: '' when none. */
  const error = computed(() => {
    for (const r of o.runs()) {
      const why = errors.get(r.id)
      if (why) return `The candidates of ${r.label || r.id} could not be read (${why}); asked again.`
    }
    return ''
  })

  return { byRun, bases, runs, loading, error, reset }
}
