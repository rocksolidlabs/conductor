import type { JoinInfo } from './useSessions'
import { ApiError } from './useApi'
import { usePageMemory } from './usePageMemory'
import { joinedFromInfo, joinedStatusOf, patchJoined, readJoined, removeJoined, upsertJoined, writeJoined, JOINED_KEY, type JoinedEntry } from '~/utils/joined'

/** How often the kept links are looked at again, and the pause between two looks (the join route allows 5 a second). */
const REFRESH_MS = 60_000
const GAP_MS = 300
let started = false

/**
 * The share links joined from this workbench (utils/joined.ts): kept in this browser, listed in the sidebar under "Shared with you",
 * looked at again every minute while the tab is visible so a revoked link or an ended session says so. The page holds the list in its
 * memory (usePageMemory), outside the state Nuxt writes at a reload: each entry carries its link's token.
 */
export function useJoined() {
  const list = usePageMemory<JoinedEntry[]>('joined', () => (import.meta.client ? readJoined(localStorage) : []))
  const api = useSessions()

  function save(next: JoinedEntry[]) {
    list.value = next
    if (import.meta.client) writeJoined(localStorage, next)
  }

  /** Keeps a link joined now. */
  function add(e: Omit<JoinedEntry, 'id' | 'addedAt'>) {
    save(upsertJoined(list.value, { ...e, lastStatus: 'ok', lastSeenAt: new Date().toISOString() }))
  }

  function find(server: string, token: string): JoinedEntry | undefined {
    return list.value.find((e) => e.server === server && e.token === token)
  }

  /** What a join page just read of a kept link; a link not kept stays not kept. */
  function noteInfo(server: string, token: string, info: JoinInfo) {
    const e = find(server, token)
    if (e) save(patchJoined(list.value, e.id, { ...joinedFromInfo(info), lastStatus: 'ok', lastSeenAt: new Date().toISOString() }))
  }

  function forget(id: string) {
    save(removeJoined(list.value, id))
  }

  async function refresh(e: JoinedEntry) {
    try {
      const info = await api.join(e.token, e.server)
      save(patchJoined(list.value, e.id, { ...joinedFromInfo(info), lastStatus: 'ok', lastSeenAt: new Date().toISOString() }))
    } catch (err) {
      const status = err instanceof ApiError ? joinedStatusOf(err) : undefined
      if (status) save(patchJoined(list.value, e.id, { lastStatus: status }))
    }
  }

  async function refreshAll() {
    if (document.visibilityState !== 'visible') return
    for (const e of [...list.value]) {
      if (e.lastStatus === 'revoked' || e.lastStatus === 'gone') continue
      await refresh(e)
      await new Promise((r) => setTimeout(r, GAP_MS))
    }
  }

  /** Once per app: a look now, then every minute, and the list kept in step with other tabs. */
  function start() {
    if (started || !import.meta.client) return
    started = true
    void refreshAll()
    window.setInterval(() => void refreshAll(), REFRESH_MS)
    window.addEventListener('storage', (ev) => {
      if (ev.key === JOINED_KEY) list.value = readJoined(localStorage)
    })
  }

  return { list: readonly(list), add, find, noteInfo, forget, refresh, refreshAll, start }
}
