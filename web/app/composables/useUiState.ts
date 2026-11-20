import { ApiError } from './useApi'
import type { UiDocReply } from '~/utils/protocol'
import { readRecent, writeRecent, type Recent } from '~/utils/palette'
import { readPins, writePins, type Pin } from '~/utils/pins'
import { clampSidebarSize, sidebarBounds, type Folds, type SidebarMode } from '~/utils/sidebar'
import { contentOf, isKept, layoutFromStorage, layoutToStorage, mergeRecents, patchLayout, sameContent, UI_NAMES, UI_WRITE_DELAY, type UiLayout, type UiName, type UiPalette, type UiPins } from '~/utils/uiState'

function storage(): Storage | null {
  try {
    return typeof localStorage === 'undefined' ? null : localStorage
  } catch {
    return null
  }
}

/** The documents' content as last read from or written to the server, by name: what a write compares with, so an unchanged one is not sent. */
const kept: Partial<Record<UiName, object>> = {}
const timers: Partial<Record<UiName, ReturnType<typeof setTimeout>>> = {}

/**
 * The app's screens' state, kept by the server (docs/plans/sync-seams.md, OS-2): the layout (the editor's layout and splits, the
 * sidebar's mode, width and folds, the crew graph's switches, the run and project pages' views), the palette's recents and the pinned
 * sessions. Local storage is the cache read first, so the page draws at once; the server's documents are read once the workbench token
 * is held, after the first paint, and a change is written a second after it is made (UI_WRITE_DELAY), so a drag writes once. The
 * server's layout and pins take the place of the browser's; the recents merge (each key's greater count and later use). A server
 * without the routes (an older one: 404) leaves local storage as it is, and the app works as before; a document the server refuses
 * (past its bound) stays in this browser and the page goes on.
 */
export function useUiState() {
  const layout = useState<UiLayout>('uiLayout', () => layoutFromStorage(storage()))
  const recents = useState<Recent>('uiRecents', () => readRecent(storage()))
  const pins = useState<Pin[]>('uiPins', () => readPins(storage()))
  /** none: not read yet; reading; read; absent: the server has no such routes. */
  const server = useState<'none' | 'reading' | 'read' | 'absent'>('uiServer', () => 'none')
  const { request } = useApi()
  const token = useWorkbenchToken()

  function content(name: UiName): object {
    if (name === 'layout') return layout.value
    if (name === 'palette') return { v: 1, recents: recents.value } satisfies UiPalette
    return { v: 1, pins: pins.value } satisfies UiPins
  }

  /** Writes the document a second after its last change, unless the server holds it as it is. */
  function schedule(name: UiName) {
    if (!import.meta.client || server.value === 'absent') return
    clearTimeout(timers[name])
    timers[name] = setTimeout(() => void flush(name), UI_WRITE_DELAY)
  }

  async function flush(name: UiName) {
    delete timers[name]
    if (server.value !== 'read') return
    const body = content(name)
    const last = kept[name]
    if (last && sameContent(last, body)) return
    try {
      const reply = await request<UiDocReply>(`/api/ui/${name}`, { method: 'PUT', body })
      kept[name] = contentOf(reply)
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) server.value = 'absent'
      // Refused (past its bound, out of its schema) or not sent: kept in this browser, and the page goes on.
    }
  }

  /** Adopts the server's layout: the browser's cache and the settings the app holds in its state. */
  function adoptLayout(l: UiLayout) {
    layout.value = l
    layoutToStorage(storage(), l)
    const sb = l.sidebar
    if (sb?.mode) useState<SidebarMode>('sidebarMode').value = sb.mode
    if (sb?.width !== undefined && import.meta.client) useState<number>('sidebarSize').value = clampSidebarSize(sb.width, useState('sidebarBounds', () => sidebarBounds(Number.NaN)).value)
    if (sb?.folds) {
      const folds = useState<Folds>('sidebarFolds')
      if (folds.value) folds.value = { ...folds.value, ...sb.folds }
    }
  }

  async function read(name: UiName) {
    const reply = await request<UiDocReply>(`/api/ui/${name}`)
    if (!isKept(reply)) {
      // None on the server: this browser's, when it has any, becomes the server's.
      const empty = name === 'layout' ? Object.keys(layout.value).length <= 1 : name === 'palette' ? !Object.keys(recents.value).length : !pins.value.length
      if (!empty) schedule(name)
      return
    }
    const doc: object = contentOf(reply)
    kept[name] = doc
    if (name === 'layout') {
      if (!sameContent(doc, layout.value)) adoptLayout(doc as UiLayout)
    } else if (name === 'palette') {
      const merged = mergeRecents(recents.value, (doc as UiPalette).recents ?? {})
      recents.value = merged
      writeRecent(storage(), merged)
      if (!sameContent(doc, { v: 1, recents: merged })) schedule(name)
    } else {
      pins.value = (doc as UiPins).pins ?? []
      writePins(storage(), pins.value)
    }
  }

  /** Reads the three documents once a token is held. */
  async function load() {
    if (!import.meta.client || server.value !== 'none' || !token.token.value) return
    server.value = 'reading'
    try {
      for (const name of UI_NAMES) await read(name)
      server.value = 'read'
      for (const name of UI_NAMES) if (timers[name]) schedule(name)
    } catch (e) {
      server.value = e instanceof ApiError && e.status === 404 ? 'absent' : 'none'
    }
  }

  return {
    layout: readonly(layout),
    recents: readonly(recents),
    pins: readonly(pins),
    server: readonly(server),
    load,
    /** Lays `patch` over the layout, keeps it in this browser and writes it to the server. */
    setLayout(patch: Omit<Partial<UiLayout>, 'v'>) {
      const next = patchLayout(layout.value, patch)
      if (sameContent(next, layout.value)) return
      layout.value = next
      layoutToStorage(storage(), next)
      schedule('layout')
    },
    setRecents(r: Recent) {
      recents.value = r
      writeRecent(storage(), r)
      schedule('palette')
    },
    setPins(p: Pin[]) {
      pins.value = p
      writePins(storage(), p)
      schedule('pins')
    },
  }
}
