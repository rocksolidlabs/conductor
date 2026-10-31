import { getCurrentScope, onScopeDispose, reactive, watch } from 'vue'
import type { RunInfo } from '~/composables/useSessions'

/** What the linked runs are read from: the live store's runs (useAttention), and a read of one. */
export interface LiveRuns {
  runOf(id: string): RunInfo | undefined
  refreshRun(id: string): Promise<RunInfo | null>
}

/** A read that leaves its run unresolved is made again after this, doubled for each one more, up to RETRY_CAP_MS. */
export const RETRY_FIRST_MS = 1000
export const RETRY_CAP_MS = 30_000

/**
 * The records of a ticket's linked runs that the live store does not hold (DispatchRuns.vue, docs/plans/dispatch.md, PR-11): a run the
 * server no longer holds is read once for its record, and null kept for an id the server knows nothing of. What is watched is each
 * linked run with whether the live store holds it, so a run the store drops while the page is open (the engine forgetting it past its
 * limit, a removal after a restart's snapshot, a token's clear) is read again for its record, and a read that answered with a live run is
 * no record: once that run leaves the store it is read again. A run the store holds again drops the record it had, which would be stale.
 * A read that settles with its run still unresolved (it failed, or answered with a live run the store dropped as older than a removal made
 * while it was on its way) is made again after a capped backoff, so the section never stays at "Reading…" waiting for a change of the
 * watched key that may never come; the waits end with the scope that made them.
 */
export function linkedRunRecords(ids: () => readonly string[], live: LiveRuns) {
  const records = reactive(new Map<string, RunInfo | null>())
  const reading = new Set<string>()
  /** The reads in a row that left a run unresolved, and the wait before its next one. */
  const misses = new Map<string, number>()
  const waits = new Map<string, ReturnType<typeof setTimeout>>()
  let stopped = false

  const unresolved = (id: string) => !stopped && ids().includes(id) && !live.runOf(id) && !records.has(id)
  function forget(id: string) {
    misses.delete(id)
    const t = waits.get(id)
    if (t !== undefined) clearTimeout(t)
    waits.delete(id)
  }
  function read(id: string) {
    if (reading.has(id) || waits.has(id)) return
    reading.add(id)
    live
      .refreshRun(id)
      .then((r) => {
        // A run the server holds is the live store's now; a record, or nothing, is this section's.
        if (!r || r.record) records.set(id, r)
      })
      .catch(() => {})
      .finally(() => {
        reading.delete(id)
        if (!unresolved(id)) {
          misses.delete(id)
          return
        }
        const n = (misses.get(id) ?? 0) + 1
        misses.set(id, n)
        waits.set(
          id,
          setTimeout(() => {
            waits.delete(id)
            if (unresolved(id)) read(id)
          }, Math.min(RETRY_CAP_MS, RETRY_FIRST_MS * 2 ** (n - 1))),
        )
      })
  }

  watch(
    () => JSON.stringify(ids().map((id) => [id, !!live.runOf(id)])),
    () => {
      for (const id of ids()) {
        if (live.runOf(id)) {
          records.delete(id)
          forget(id)
          continue
        }
        if (!records.has(id)) read(id)
      }
    },
    { immediate: true },
  )
  if (getCurrentScope()) {
    onScopeDispose(() => {
      stopped = true
      for (const id of [...waits.keys()]) forget(id)
    })
  }
  /** The run a link names, as the live store holds it, else its record; undefined while it is read, null when the server knows nothing of it. */
  function runOf(id: string): RunInfo | null | undefined {
    return live.runOf(id) ?? records.get(id)
  }
  return { records, runOf }
}
