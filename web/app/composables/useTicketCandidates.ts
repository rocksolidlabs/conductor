import { ApiError } from './useApi'
import type { TaskCandidates, TicketRunEntry } from './useDispatch'
import { through, type DispatchOrder } from '~/utils/dispatchOrder'
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
}

/**
 * The runs of a ticket's Runs whose candidates the delivery form and the Decisions read (PR-12): every entry's run but a contested
 * chain's, which no ticket owns (a delivery naming one of its candidates is refused), each once, in the section's order, with what moves
 * it: its summary's state, pause, outcome, stop and task counts, and the live store's revision of it.
 */
export function heldRuns(entries: readonly TicketRunEntry[], revision: (id: string) => number | undefined): HeldRun[] {
  const out: HeldRun[] = []
  const seen = new Set<string>()
  for (const e of entries) {
    const id = entryRunId(e)
    if (!id || seen.has(id) || e.contested) continue
    seen.add(id)
    const s = e.run
    out.push({
      id,
      label: s?.label || e.launch?.label || '',
      signature: JSON.stringify([revision(id) ?? 0, s ? [s.state, s.paused, s.outcome ?? '', s.stoppedAt ?? '', s.tasks] : null]),
    })
  }
  return out
}

/**
 * The candidates of a ticket's runs (Program item DS2, docs/plans/dispatch-evidence.md, PR-12), for what a delivery may name, a
 * delivered candidate withdrawn since, and the Accept anyway receipts under Decisions: each run's every page (GET
 * /api/runs/{run}/candidates), read when the run is first shown and again, gathered for RUN_EVENT_WINDOW_MS, when its signature moves
 * (its summary read again on its run events, or the live store's revision of it); a run moving through every read (503 run_changing)
 * is asked again after the second its Retry-After names. Every read goes through the page's ordering gate, by run. Nothing polls.
 */
export function useTicketCandidates(o: { gate: DispatchOrder; key: () => string; runs: () => readonly HeldRun[] }) {
  const api = useDispatchApi()
  /** Each run's tasks with their candidates, as last read. */
  const byRun = shallowReactive(new Map<string, TaskCandidates[]>())
  /** The signature each run was last asked to be read at. */
  const asked = new Map<string, string>()
  /** The runs to read on the next gathered read. */
  const dirty = new Set<string>()
  const loading = ref(false)

  async function readRun(id: string): Promise<void> {
    const k = o.key()
    try {
      await through(
        o.gate,
        `delivery-candidates:${id}`,
        async () => {
          const tasks: TaskCandidates[] = []
          let after = ''
          for (let i = 0; i < MAX_PAGES; i++) {
            const p = await api.runCandidates(id, after)
            for (const t of p.candidates ?? []) if (!tasks.some((x) => x.task === t.task)) tasks.push(t)
            if (!p.next) break
            after = p.next
          }
          return tasks
        },
        (tasks) => {
          if (k === o.key()) byRun.set(id, tasks)
        },
      )
    } catch (e) {
      if (e instanceof ApiError && e.status === 503 && e.code === 'run_changing') {
        dirty.add(id)
        retry.ask()
        return
      }
      // Another failure: what was read stays, and the next move of the run asks again.
      asked.delete(id)
    }
  }

  async function readDirty(): Promise<void> {
    const ids = [...dirty]
    dirty.clear()
    if (!ids.length) return
    loading.value = true
    try {
      await Promise.all(ids.map(readRun))
    } finally {
      loading.value = false
    }
  }
  const reads = gather(readDirty, RUN_EVENT_WINDOW_MS)
  const retry = gather(readDirty, RUN_CHANGING_RETRY_MS)
  onBeforeUnmount(() => {
    reads.stop()
    retry.stop()
  })

  watch(
    () => o.runs(),
    (runs) => {
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
    },
    { immediate: true },
  )

  // Another ticket on the page: nothing of this one's stays.
  watch(
    () => o.key(),
    (k, old) => {
      if (k === old) return
      for (const id of byRun.keys()) o.gate.drop(`delivery-candidates:${id}`)
      byRun.clear()
      asked.clear()
      dirty.clear()
    },
  )

  /** The runs with their tasks, in the order given, for the form's choices and the Decisions. */
  const runs = computed(() => o.runs().map((r) => ({ run: r.id, label: r.label, tasks: byRun.get(r.id) ?? [] })))

  return { byRun, runs, loading }
}
