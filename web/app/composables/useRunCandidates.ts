import { ApiError } from './useApi'
import { type DispatchWatch, dispatchLive } from './useDispatchLive'
import type { RunCandidate, RunCandidatesPage, TaskCandidates } from './useDispatch'
import type { Finding } from './useSessions'
import { RUN_CHANGING_RETRY_MS, type TaskDetail, gather, repairReads } from '~/utils/dispatchEvidence'
import { latestReads } from '~/utils/latestReads'
import { RUN_EVENT_WINDOW_MS } from '~/utils/runs'

/** A candidate's key on the page: its task and attempt. */
export const candidateKey = (c: Pick<RunCandidate, 'task' | 'attempt'>) => `${c.task}/${c.attempt}`

/**
 * A run's candidates as DispatchCandidates shows them (Program item DS2, docs/plans/dispatch-evidence.md, "web/app", PR-11): the pages
 * read from the ledger (GET /api/runs/{run}/candidates), a page of tasks at a time, More tasks reading one more by the last page's
 * `next`, every page read again from the start as one reply through the component's ordering gate; read when the run is named,
 * gathered for RUN_EVENT_WINDOW_MS when its revision moves, and at once on a `snapshot` (the stream started over: what it missed is
 * unknown, and a read that failed, or a record's, which has no revision to move, is read again). A read refused while the run moved
 * through every one of its reads (503 run_changing, Retry-After: 1) is no failure, on the first read as on any other: what is shown
 * stays and it is asked again after the second. A failure is said only while no reply asked after it was applied and no snapshot came
 * after it was asked (a 404 is ordered by the gate as any reply), and a reply asked before a failure said leaves it said: what is shown
 * is always the newest answer. Any other failure, one that may pass (the server out of reach, 503 ledger_unavailable), is asked again
 * through the live store's retry (DispatchLive: after the window, doubled up to DISPATCH_RETRY_MAX_MS) until one answers or a snapshot
 * reads the pages, so a record, whose revision never moves, catches up with no further event. Each task's own read (GET /api/runs/{run}/tasks/{task}, `details`): a review's, when it is opened, for
 * its findings, which it has whole; and, after every read of the pages, each task the repairs read (repairReads), for every attempt with
 * when it started, a repair that never submitted included, of a record too. Each task's reads are ordered on their own, so a second
 * review opened while the first one's reply is on its way leaves both.
 */
export function useRunCandidates(o: { runId: () => string; revision: () => number | undefined }) {
  const { api, gate, readOrAbsent } = useDispatch()
  const sessions = useSessions()

  /** The pages read, in their order; null until the first read answered. */
  const pages = ref<RunCandidatesPage[] | null>(null)
  /** How many pages are shown: a re-read reads them all again. */
  const count = ref(1)
  const error = ref('')
  const loading = ref(false)
  // The reads of the pages, numbered as they are asked: the last one applied (a reply or a 404), the last whose failure is said, and the
  // last asked before the latest snapshot.
  let asked = 0
  let appliedN = 0
  let errorN = 0
  let floor = 0

  const tasks = computed<TaskCandidates[]>(() => {
    const out: TaskCandidates[] = []
    for (const p of pages.value ?? []) for (const t of p.candidates ?? []) if (!out.some((x) => x.task === t.task)) out.push(t)
    return out
  })
  const managed = computed(() => pages.value?.[0]?.managed ?? true)
  const next = computed(() => pages.value?.at(-1)?.next ?? '')

  /**
   * One read of the pages: resolves once it answered or was no failure (a run_changing asked again, a 404 ordered, a run left, a read
   * superseded), rejects with a failure that may pass, said first.
   */
  async function readOnce(): Promise<void> {
    const run = o.runId()
    const n = count.value
    const mine = ++asked
    loading.value = true
    try {
      await readOrAbsent(
        `candidates:${run}`,
        async () => {
          const out: RunCandidatesPage[] = []
          let after = ''
          for (let i = 0; i < n; i++) {
            const p = await api.runCandidates(run, after)
            out.push(p)
            if (!p.next) break
            after = p.next
          }
          return out
        },
        {
          apply: (out) => {
            if (run !== o.runId()) return
            pages.value = out
            appliedN = mine
            if (mine > errorN) error.value = ''
            readRepairs()
          },
          absent: (e) => e instanceof ApiError && e.status === 404,
          gone: () => {
            if (run !== o.runId()) return
            appliedN = errorN = mine
            error.value = 'The server knows no such run.'
          },
        },
      )
    } catch (e) {
      if (run !== o.runId()) return
      // Superseded: a reply asked after it was applied, or a snapshot came after it was asked and its read answers instead.
      if (mine <= appliedN || mine <= floor) return
      // The run moved through every read: what is shown stays (nothing, "Reading", before the first answer), asked again after a second.
      if (e instanceof ApiError && e.status === 503 && e.code === 'run_changing') {
        retry.ask()
        return
      }
      errorN = mine
      error.value = (e as Error).message
      throw e
    } finally {
      loading.value = false
    }
  }
  // A snapshot reads the pages shown again, as it does the ticket page (useDispatchLive): no event of Dispatch's reads them otherwise.
  // A read of the watch's that fails is asked again by the live store, after the window, doubled each time.
  const live: DispatchWatch = {
    later: () => false,
    read: readOnce,
    reset: () => {
      gate.reset()
      floor = asked
    },
  }
  const unwatch = dispatchLive().subscribe(live)
  /** A read the page asks of itself (its first, a revision's, More tasks, a run_changing's retry): a failure that may pass is asked again as the live watch's is. */
  function readPages(): Promise<void> {
    const read = readOnce()
    dispatchLive().ask(live, () => read)
    return read.catch(() => undefined)
  }
  const reads = gather(readPages, RUN_EVENT_WINDOW_MS)
  /** A read refused while the run moved through every one of its reads, asked again after the second its Retry-After names. */
  const retry = gather(readPages, RUN_CHANGING_RETRY_MS)
  onBeforeUnmount(() => {
    reads.stop()
    retry.stop()
    unwatch()
  })

  async function more(): Promise<void> {
    if (!next.value) return
    count.value++
    await readPages()
  }

  // Each task's own read (the task route): its attempts whole, for the repairs (details) and a review's findings when it is opened.
  const opened = reactive(new Set<string>())
  const findings = reactive(new Map<string, Finding[]>())
  const findingsError = reactive(new Map<string, string>())
  const details = reactive(new Map<string, TaskDetail>())
  const detailsError = reactive(new Map<string, string>())
  /** By task: its reads, the last one asked applied (a reply carries every attempt of the task, so one task's reads are one thing). */
  const taskReads = new Map<string, ReturnType<typeof latestReads>>()
  function readsOf(task: string) {
    let r = taskReads.get(task)
    if (!r) taskReads.set(task, (r = latestReads()))
    return r
  }
  /** The tasks read since the pages last answered: a review's read names the task it reviews, read then if it was not. */
  let round = new Set<string>()
  function readTask(task: string): Promise<void> {
    round.add(task)
    const run = o.runId()
    const of = (key: string) => key.startsWith(`${task}/`)
    return readsOf(task).read(
      () => sessions.runTask(run, task),
      (r) => {
        if (run !== o.runId()) return
        details.set(task, { task: r.task, attempts: r.attempts })
        detailsError.delete(task)
        for (const a of r.attempts) findings.set(`${task}/${a.n}`, a.submitted?.findings ?? [])
        for (const key of [...findingsError.keys()]) if (of(key)) findingsError.delete(key)
        const sub = r.task.kind === 'review' ? r.task.needs?.[0] : undefined
        if (sub && !round.has(sub)) void readTask(sub)
      },
      (e) => {
        if (run !== o.runId()) return
        detailsError.set(task, (e as Error).message)
        // Every attempt of the task opened and still reading waited on this read: each says it failed, and is read again when reopened.
        for (const key of opened) if (of(key) && !findings.has(key)) findingsError.set(key, (e as Error).message)
      },
    )
  }
  /** After the pages answered: the attempts of every task the repairs read, again, as the pages moved. */
  function readRepairs() {
    round = new Set()
    for (const t of repairReads(tasks.value, details)) void readTask(t)
  }
  function toggleFindings(c: RunCandidate): Promise<void> {
    const k = candidateKey(c)
    if (opened.has(k)) {
      opened.delete(k)
      return Promise.resolve()
    }
    opened.add(k)
    return readTask(c.task)
  }

  watch(
    () => o.runId(),
    () => {
      pages.value = null
      count.value = 1
      details.clear()
      detailsError.clear()
      void readPages()
    },
    { immediate: true },
  )
  watch(
    () => o.revision(),
    (now, before) => {
      if (now !== before) reads.ask()
    },
  )

  return { pages, tasks, managed, next, error, loading, more, opened, findings, findingsError, toggleFindings, details, detailsError }
}
