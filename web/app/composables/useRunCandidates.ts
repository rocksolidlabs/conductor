import { ApiError } from './useApi'
import { dispatchLive } from './useDispatchLive'
import type { RunCandidate, RunCandidatesPage, TaskCandidates } from './useDispatch'
import type { Finding } from './useSessions'
import { RUN_CHANGING_RETRY_MS, gather } from '~/utils/dispatchEvidence'
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
 * stays and it is asked again after the second. A review's findings, read from the task route (GET /api/runs/{run}/tasks/{task}, which has them whole) when the review is
 * opened, each review task's reads ordered on their own, so a second review opened while the first one's reply is on its way leaves both.
 */
export function useRunCandidates(o: { runId: () => string; revision: () => number | undefined }) {
  const { api, gate, read } = useDispatch()
  const sessions = useSessions()

  /** The pages read, in their order; null until the first read answered. */
  const pages = ref<RunCandidatesPage[] | null>(null)
  /** How many pages are shown: a re-read reads them all again. */
  const count = ref(1)
  const error = ref('')
  const loading = ref(false)

  const tasks = computed<TaskCandidates[]>(() => {
    const out: TaskCandidates[] = []
    for (const p of pages.value ?? []) for (const t of p.candidates ?? []) if (!out.some((x) => x.task === t.task)) out.push(t)
    return out
  })
  const managed = computed(() => pages.value?.[0]?.managed ?? true)
  const next = computed(() => pages.value?.at(-1)?.next ?? '')

  async function readPages(): Promise<void> {
    const run = o.runId()
    const n = count.value
    loading.value = true
    try {
      await read(
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
        (out) => {
          if (run !== o.runId()) return
          pages.value = out
          error.value = ''
        },
      )
    } catch (e) {
      if (run !== o.runId()) return
      // The run moved through every read: what is shown stays (nothing, "Reading", before the first answer), asked again after a second.
      if (e instanceof ApiError && e.status === 503 && e.code === 'run_changing') {
        retry.ask()
        return
      }
      error.value = e instanceof ApiError && e.status === 404 ? 'The server knows no such run.' : (e as Error).message
    } finally {
      loading.value = false
    }
  }
  const reads = gather(readPages, RUN_EVENT_WINDOW_MS)
  /** A read refused while the run moved through every one of its reads, asked again after the second its Retry-After names. */
  const retry = gather(readPages, RUN_CHANGING_RETRY_MS)
  // A snapshot reads the pages shown again, as it does the ticket page (useDispatchLive): no event of Dispatch's reads them otherwise.
  const unwatch = dispatchLive().subscribe({ later: () => false, read: readPages, reset: () => gate.reset() })
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

  // A review's findings, read from the task route when the review is opened: the candidates carry their counts alone.
  const opened = reactive(new Set<string>())
  const findings = reactive(new Map<string, Finding[]>())
  const findingsError = reactive(new Map<string, string>())
  /** By review task: its reads, the last one asked applied (a reply carries every attempt of the task, so one task's reads are one thing). */
  const findingReads = new Map<string, ReturnType<typeof latestReads>>()
  function readsOf(task: string) {
    let r = findingReads.get(task)
    if (!r) findingReads.set(task, (r = latestReads()))
    return r
  }
  function toggleFindings(c: RunCandidate): Promise<void> {
    const k = candidateKey(c)
    if (opened.has(k)) {
      opened.delete(k)
      return Promise.resolve()
    }
    opened.add(k)
    const run = o.runId()
    const of = (key: string) => key.startsWith(`${c.task}/`)
    return readsOf(c.task).read(
      () => sessions.runTask(run, c.task),
      (r) => {
        if (run !== o.runId()) return
        for (const a of r.attempts) findings.set(`${c.task}/${a.n}`, a.submitted?.findings ?? [])
        for (const key of [...findingsError.keys()]) if (of(key)) findingsError.delete(key)
      },
      (e) => {
        if (run !== o.runId()) return
        // Every attempt of the task opened and still reading waited on this read: each says it failed, and is read again when reopened.
        for (const key of opened) if (of(key) && !findings.has(key)) findingsError.set(key, (e as Error).message)
      },
    )
  }

  watch(
    () => o.runId(),
    () => {
      pages.value = null
      count.value = 1
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

  return { pages, tasks, managed, next, error, loading, more, opened, findings, findingsError, toggleFindings }
}
