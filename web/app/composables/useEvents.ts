import type { SessionInfo } from './useSessions'
import type { ActivityEntry } from '~/utils/protocol'
import { EntryHold, liveEvent, parseRoutes, pruneMarks, routeEntry, type EventMark, type EventType, type FeedEntry, type RouteRow, type RoutedEvent } from '~/utils/events'

/** Browser storage key of the routing: routes are per browser. */
export const ROUTES_KEY = 'conductor.events.routes'
/** Longest an entry waits for the session change that carries its state. */
const HOLD_MS = 2000

type Listener = (e: RoutedEvent) => void

// Entries waiting for the session change that types them (one hold for the
// app), who hears about typed events, and whether this tab listens to the
// others' route changes.
let hold: EntryHold | undefined
const listeners = new Set<Listener>()
let seq = 0
let listening = false

function readRoutes(): Record<EventType, RouteRow> {
  try {
    return parseRoutes(localStorage.getItem(ROUTES_KEY))
  } catch {
    return parseRoutes(null)
  }
}

/**
 * Events of every session, as the admin stream delivers them (useAttention
 * pushes each `activity` entry here): the live feed, the routing rows and
 * the badges it puts on sessions. Nothing is polled and nothing persists but
 * the routes.
 */
export function useEvents() {
  const feed = useState<FeedEntry[]>('eventsFeed', () => [])
  const routes = useState<Record<EventType, RouteRow>>('eventsRoutes', () => (import.meta.client ? readRoutes() : parseRoutes(null)))
  const marks = useState<Record<string, EventMark>>('eventsMarks', () => ({}))
  const store = useAttentionStore()

  /** The feed, oldest first; a type whose Feed route is off now is left out. */
  const entries = computed(() => feed.value.filter((e) => routes.value[e.event].feed))

  function session(id: string): SessionInfo | undefined {
    return store.value.sessions.get(id)
  }

  function setMarks(next: Readonly<Record<string, EventMark>>) {
    if (next !== marks.value) marks.value = next as Record<string, EventMark>
  }

  function clearMark(sessionId: string) {
    setMarks(pruneMarks(marks.value, routes.value, (id) => id !== sessionId))
  }

  /** Types an entry against its session as the store has it now and sends it where its routes say (routeEntry), then to the listeners. */
  function accept(sessionId: string, entry: ActivityEntry) {
    const s = session(sessionId)
    const { state, type } = routeEntry({ feed: feed.value, marks: marks.value }, sessionId, entry, s, routes.value, ++seq)
    if (!type) return
    if (state.feed !== feed.value) feed.value = state.feed as FeedEntry[]
    setMarks(state.marks)
    // Late (recorded while its host was away, or sent again): shown, never a live event, so no listener hears it.
    if (!liveEvent(entry)) return
    const e: RoutedEvent = { sessionId, session: s, type, entry }
    for (const fn of listeners) {
      try {
        fn(e)
      } catch {
        /* one listener's failure is its own */
      }
    }
  }

  // The refs are app-wide, so the first caller's functions serve every caller.
  hold ??= new EntryHold(session, accept, HOLD_MS)
  const entryHold = hold

  /** Routes as another tab saved them: the same checks as a reload, and badges their Badge route no longer allows go. */
  function onStorage(ev: StorageEvent) {
    if (ev.key !== ROUTES_KEY) return
    routes.value = parseRoutes(ev.newValue)
    setMarks(pruneMarks(marks.value, routes.value))
  }
  if (import.meta.client && !listening) {
    listening = true
    window.addEventListener('storage', onStorage)
  }

  /**
   * Takes one activity entry of a session. The admin stream sends an
   * attention entry just before the session change that carries its state,
   * so an attention entry the store cannot type yet waits for that change
   * (settle), at most two seconds, and so does every later entry of the same
   * session, which keeps them in order.
   */
  function push(sessionId: string, e: ActivityEntry): void {
    entryHold.push(sessionId, e)
  }

  /** The store changed a session (or all of them): release what its state now settles. */
  function settle(sessionId?: string) {
    entryHold.settle(sessionId)
  }

  /** The session left the registry: what it still holds is typed without it, and its badge goes. */
  function forget(sessionId: string) {
    entryHold.forget(sessionId)
    clearMark(sessionId)
  }

  /** A snapshot lists every session there is: forget the holds and badges of any other. */
  function retain(ids: ReadonlySet<string>) {
    entryHold.retain((id) => ids.has(id))
    setMarks(pruneMarks(marks.value, routes.value, (id) => ids.has(id)))
  }

  /** Another workbench token: nothing seen through the old one stays (feed, badges, holds). */
  function reset() {
    entryHold.clear()
    feed.value = []
    marks.value = {}
  }

  /** Changes one route and saves the matrix in this browser. Turning Badge off takes that type's badges away. */
  function setRoute(t: EventType, k: keyof RouteRow, v: boolean) {
    routes.value = { ...routes.value, [t]: { ...routes.value[t], [k]: v } }
    try {
      localStorage.setItem(ROUTES_KEY, JSON.stringify(routes.value))
    } catch {
      /* storage unavailable: the change lasts until reload */
    }
    setMarks(pruneMarks(marks.value, routes.value))
  }

  /** Calls fn with every typed event; returns the function that stops it. */
  function onEvent(fn: Listener): () => void {
    listeners.add(fn)
    return () => {
      listeners.delete(fn)
    }
  }

  return { entries, push, routes, setRoute, marks, clearMark, settle, forget, retain, reset, onEvent }
}
