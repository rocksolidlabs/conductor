import { watch } from 'vue'
import type { RemoteKind, RemoteRecord, RemoteReply, SyncStatusReply } from '~/utils/protocol'
import { notOwn } from '~/utils/remote'

/**
 * The sync store's report (GET /api/sync, docs/plans/sync-seams.md, OS-4), read while a sync store is registered (`sync` in GET
 * /api/capabilities) and again by `load`; null with none, so a free server reads nothing.
 */
export function useSyncStatus() {
  const caps = useCapabilities()
  const { request } = useApi()
  const report = useState<SyncStatusReply | null>('syncStatus', () => null)
  async function load() {
    if (!caps.has('sync')) {
      report.value = null
      return
    }
    try {
      report.value = await request<SyncStatusReply>('/api/sync')
    } catch {
      /* what is known stands */
    }
  }
  watch(
    () => caps.has('sync'),
    (on) => {
      if (on) void load()
      else report.value = null
    },
    { immediate: true },
  )
  return { report, load }
}

/**
 * Other machines' records of `kind` (GET /api/remote), of the ticket `ticket()` names when it names one, without this server's own
 * device's: read only, drawn as another machine's. A server with no sync store holds none (`{records: []}`), so its pages draw
 * nothing more.
 */
export function useRemote(kind: RemoteKind, ticket?: () => string) {
  const { request } = useApi()
  const sync = useSyncStatus()
  const records = ref<RemoteRecord[]>([])
  async function load() {
    const q = new URLSearchParams({ kind })
    const t = ticket?.()
    if (t) q.set('ticket', t)
    try {
      const r = await request<RemoteReply>(`/api/remote?${q}`)
      records.value = notOwn(r.records ?? [], sync.report.value)
    } catch {
      /* what is known stands */
    }
  }
  watch([() => ticket?.() ?? '', () => sync.report.value?.device ?? ''], () => void load(), { immediate: true })
  return { records, report: sync.report, load }
}
