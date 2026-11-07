import type { Decision } from '~/utils/decisions'
import { decisionsLive } from './useDecisions'

/**
 * A ticket page's durable decisions (Program item DR1, docs/plans/decisions.md, "web/app"): the records whose run was launched from the
 * ticket (`GET /api/dispatch/tickets/{key}/decisions`, newest first, at most 100), a gate's among them, with their states and receipts.
 * Read when the page names the ticket, again on every `decision` event of the app's one stream (the event names the run, not the
 * ticket, so any decision may be the ticket's), when the stream comes back and on its fallback's cadence while it is down: one read at a
 * time, and one more after it when something asked meanwhile. Only the ticket shown is kept.
 */
export function useTicketDecisions(key: () => string) {
  const { request } = useApi()
  const decisions = ref<Decision[] | null>(null)
  const error = ref('')
  let inflight: Promise<void> | null = null
  let again = false

  async function once(k: string) {
    const r = await request<{ decisions?: Decision[] }>(`/api/dispatch/tickets/${encodeURIComponent(k)}/decisions`)
    if (k !== key()) return
    decisions.value = r.decisions ?? []
    error.value = ''
  }
  function read(): Promise<void> {
    if (inflight) {
      again = true
      return inflight
    }
    inflight = (async () => {
      try {
        do {
          again = false
          const k = key()
          if (!k) return
          await once(k)
        } while (again)
      } catch (e) {
        error.value = (e as Error).message
      } finally {
        inflight = null
      }
    })()
    return inflight
  }

  const stop = decisionsLive().subscribe(() => void read())
  watch(
    key,
    (k, old) => {
      if (k === old) return
      decisions.value = null
      error.value = ''
      if (k) void read()
    },
    { immediate: true },
  )
  onBeforeUnmount(stop)
  return { decisions, error, read }
}
