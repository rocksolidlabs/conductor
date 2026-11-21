import { getCurrentScope, onScopeDispose, watch } from 'vue'
import { REMOTE_FILES, REMOTE_PAGE, type RemoteKind, type RemoteRecord, type RemoteReply, type SyncStatusReply } from '~/utils/protocol'
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
 * device's, every page of them: read only, drawn as another machine's. Read only while a sync store is registered (`sync` in GET /api/capabilities): with
 * none the app asks nothing and draws none, as the server answers none.
 */
export function useRemote(kind: RemoteKind, ticket?: () => string) {
  const caps = useCapabilities()
  const { request } = useApi()
  const sync = useSyncStatus()
  const records = ref<RemoteRecord[]>([])
  // The latest load alone draws: one begun before another ends draws nothing.
  let seq = 0
  async function load() {
    const mine = ++seq
    if (!caps.has('sync')) {
      records.value = []
      return
    }
    const t = ticket?.()
    // Every page, followed by its `next`, at most the pages the records' bound fills; what is drawn changes once the whole set is in.
    const all: RemoteRecord[] = []
    let after = ''
    try {
      for (let page = 0; page < REMOTE_FILES / REMOTE_PAGE; page++) {
        const q = new URLSearchParams({ kind })
        if (t) q.set('ticket', t)
        if (after) q.set('after', after)
        const r = await request<RemoteReply>(`/api/remote?${q}`)
        all.push(...(r.records ?? []))
        if (!r.next) break
        after = r.next
      }
    } catch {
      return /* what is known stands */
    }
    if (mine === seq) records.value = notOwn(all, sync.report.value)
  }
  watch([() => caps.has('sync'), () => ticket?.() ?? '', () => sync.report.value?.device ?? ''], () => void load(), { immediate: true })
  refreshing(load)
  return { records, report: sync.report, load }
}

/** How often other machines' records are read again while the page is visible. */
export const REMOTE_POLL_MS = 30_000

/**
 * Reads again what a page shows of other machines (load) as the inbox does (useInbox): when the page becomes visible, when the events
 * stream comes back after a break, and every REMOTE_POLL_MS while visible, since nothing on the stream says a remote record changed.
 * Ends with the scope that made it.
 */
function refreshing(load: () => Promise<void>) {
  if (typeof document === 'undefined') return
  const visible = () => document.visibilityState === 'visible'
  const onVisibility = () => {
    if (visible()) void load()
  }
  document.addEventListener('visibilitychange', onVisibility)
  const timer = setInterval(() => {
    if (visible()) void load()
  }, REMOTE_POLL_MS)
  // The stream's first connection is no return: only one after a break reads again.
  const attention = useAttentionStore()
  let broken = false
  const stopStream = watch(
    () => attention.value.connected,
    (now) => {
      if (!now) broken = true
      else if (broken) {
        broken = false
        void load()
      }
    },
  )
  if (getCurrentScope()) {
    onScopeDispose(() => {
      document.removeEventListener('visibilitychange', onVisibility)
      clearInterval(timer)
      stopStream()
    })
  }
}
