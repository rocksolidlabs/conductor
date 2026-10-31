import { reactive, watch } from 'vue'
import type { RunInfo } from '~/composables/useSessions'

/** What the linked runs are read from: the live store's runs (useAttention), and a read of one. */
export interface LiveRuns {
  runOf(id: string): RunInfo | undefined
  refreshRun(id: string): Promise<RunInfo | null>
}

/**
 * The records of a ticket's linked runs that the live store does not hold (DispatchRuns.vue, docs/plans/dispatch.md, PR-11): a run the
 * server no longer holds is read once for its record, and null kept for an id the server knows nothing of. What is watched is each
 * linked run with whether the live store holds it, so a run the store drops while the page is open (the engine forgetting it past its
 * limit, a removal after a restart's snapshot, a token's clear) is read again for its record, and a read that answered with a live run is
 * no record: once that run leaves the store it is read again. A run the store holds again drops the record it had, which would be stale.
 */
export function linkedRunRecords(ids: () => readonly string[], live: LiveRuns) {
  const records = reactive(new Map<string, RunInfo | null>())
  const reading = new Set<string>()
  watch(
    () => JSON.stringify(ids().map((id) => [id, !!live.runOf(id)])),
    () => {
      for (const id of ids()) {
        if (live.runOf(id)) {
          records.delete(id)
          continue
        }
        if (records.has(id) || reading.has(id)) continue
        reading.add(id)
        live
          .refreshRun(id)
          .then((r) => {
            // A run the server holds is the live store's now; a record, or nothing, is this section's.
            if (!r || r.record) records.set(id, r)
          })
          .catch(() => {})
          .finally(() => reading.delete(id))
      }
    },
    { immediate: true },
  )
  /** The run a link names, as the live store holds it, else its record; undefined while it is read, null when the server knows nothing of it. */
  function runOf(id: string): RunInfo | null | undefined {
    return live.runOf(id) ?? records.get(id)
  }
  return { records, runOf }
}
