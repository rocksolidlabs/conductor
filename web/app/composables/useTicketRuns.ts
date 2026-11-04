import { ApiError } from './useApi'
import type { TicketRunsPage } from './useDispatch'
import { throughOrAbsent, type DispatchOrder } from '~/utils/dispatchOrder'
import { RUN_CHANGING_RETRY_MS, entryRunId, gather, mergeEntries, runsChanged, runsSignature } from '~/utils/dispatchEvidence'
import { RUN_EVENT_WINDOW_MS } from '~/utils/runs'

/**
 * A ticket page's Runs (Program item DS2, docs/plans/dispatch-evidence.md, "web/app", PR-11): the pages of the ticket's runs (GET
 * …/{key}/runs), newest first, every launch, hand-linked run and continuation on some page, Show older reading the next by the last
 * page's `next`. The pages shown are read again from the start as one reply through the page's ordering gate, as its history is: with the
 * ticket on every dispatch event naming it (the page's reload) and on a snapshot, and, gathered for RUN_EVENT_WINDOW_MS, when a run they
 * show changes in the live store (its run events, and its member sessions' changes, which the store reads it again for), a member
 * session of one changes status, or a live run continuing one appears (a record resumed from another page). A question changes a
 * session, not a run: the section counts those from the live sessions without a read (utils/dispatchEvidence.ts, runQuestions).
 * Nothing polls.
 */
export function useTicketRuns(o: { gate: DispatchOrder; key: () => string }) {
  const api = useDispatchApi()
  const live = useAttention()

  /** The pages read, in their order; null until the first read answered. */
  const pages = ref<TicketRunsPage[] | null>(null)
  /** How many pages are shown: a re-read reads them all again. */
  const count = ref(1)
  const loading = ref(false)
  const error = ref('')

  const entries = computed(() => mergeEntries(pages.value ?? []))
  const next = computed(() => pages.value?.at(-1)?.next ?? '')

  function notFound(e: unknown): boolean {
    return e instanceof ApiError && e.status === 404
  }
  function changing(e: unknown): boolean {
    return e instanceof ApiError && e.status === 503 && e.code === 'run_changing'
  }

  /** The reads asked for by a run's change, gathered for the window. */
  const reads = gather(() => read(), RUN_EVENT_WINDOW_MS)
  /** A read refused while a run of the chain is resumed, asked again after the second its Retry-After names. */
  const retry = gather(() => read(), RUN_CHANGING_RETRY_MS)

  /**
   * Reads the pages shown again, from the start, as one reply. Rejects when the read failed. A ticket with no row (a 404) is ordered
   * through the gate as any reply (throughOrAbsent), so a slow one never empties what a later read found. A read refused while a run of
   * a chain shown is resumed (503 run_changing, Retry-After: 1) is no failure, on the first read as on any other: what is shown stays
   * (nothing, "Reading", before the first answer), it is asked again after the second, and it resolves.
   */
  async function read(): Promise<void> {
    const k = o.key()
    if (!k) return
    const n = count.value
    loading.value = true
    try {
      await throughOrAbsent(
        o.gate,
        `runs:${k}`,
        async () => {
          const out: TicketRunsPage[] = []
          let before = ''
          for (let i = 0; i < n; i++) {
            const p = await api.ticketRuns(k, before)
            out.push({ entries: p.entries ?? [], ...(p.next ? { next: p.next } : {}) })
            if (!p.next) break
            before = p.next
          }
          return out
        },
        {
          apply: (out) => {
            if (k !== o.key()) return
            pages.value = out
            error.value = ''
          },
          absent: notFound,
          gone: () => {
            if (k !== o.key()) return
            pages.value = []
            error.value = ''
          },
        },
      )
    } catch (e) {
      // The resume ends by itself, and its run events read the pages again too.
      if (changing(e)) {
        if (k === o.key()) retry.ask()
        return
      }
      if (k === o.key()) error.value = (e as Error).message
      throw e
    } finally {
      loading.value = false
    }
  }

  /** Show older: one page more, read with the others. */
  async function older(): Promise<void> {
    if (!next.value) return
    count.value++
    await read().catch(() => {})
  }

  // The runs shown, as the live store holds them, with their member sessions and the live runs that continue them: a change reads the
  // pages again.
  const shown = computed(() => entries.value.map(entryRunId).filter(Boolean))
  watch(
    () => runsSignature(shown.value, (id) => live.runOf(id), live.sessions.value, live.runs.value),
    (now, before) => {
      if (before && runsChanged(before, now)) reads.ask()
    },
  )
  onBeforeUnmount(() => {
    reads.stop()
    retry.stop()
  })

  // Another ticket on the page: nothing of this one's stays.
  watch(
    () => o.key(),
    (k, old) => {
      if (k === old) return
      if (old) o.gate.drop(`runs:${old}`)
      pages.value = null
      count.value = 1
      error.value = ''
    },
  )

  return { pages, entries, next, loading, error, read, older }
}

export type TicketRuns = ReturnType<typeof useTicketRuns>
