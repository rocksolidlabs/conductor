import type { RunInfo, SessionInfo } from './useSessions'
import { ApiError } from './useApi'
import { liveSession, attentionFavicon, alerting, needingInput, newlyNeedingInput, playChime } from '~/utils/attention'
import { eventAlert, type RoutedEvent } from '~/utils/events'
import { formedCrew } from '~/utils/activity'
import { desktopBridge } from '~/utils/desktop'
import type { ActivityEntry, ChatMessage, SessionActivity } from '~/utils/protocol'
import { RunStore } from '~/utils/runs'
import { asDispatchEvent, dispatchLive } from './useDispatchLive'
import { asBrakemanEvent, brakemanLive } from './useBrakeman'
import { inboxLive } from './useInbox'
import { notificationTarget } from '~/utils/inbox'

const SETTINGS_KEY = 'conductor.attention.settings'
/** Shortest gap between two chimes, so a burst of routed events plays one. */
const CHIME_GAP_MS = 1500
let lastChime = 0
let stopEvents: (() => void) | undefined
/** The runs of the live store: one per app, made by the first useAttention. */
let runStore: RunStore | undefined

export interface AttentionSettings {
  notifications: boolean
  chime: boolean
}

function readSettings(): AttentionSettings {
  try {
    const raw = localStorage.getItem(SETTINGS_KEY)
    if (raw) return { notifications: false, chime: false, ...JSON.parse(raw) }
  } catch {
    /* ignore */
  }
  return { notifications: false, chime: false }
}

/** Persisted user preferences for attention alerts. */
export function useAttentionSettings() {
  const settings = useState<AttentionSettings>('attentionSettings', () => (import.meta.client ? readSettings() : { notifications: false, chime: false }))
  const permission = useState<NotificationPermission | 'unsupported'>('notificationPermission', () =>
    import.meta.client && 'Notification' in window ? Notification.permission : 'unsupported',
  )

  function save() {
    try {
      localStorage.setItem(SETTINGS_KEY, JSON.stringify(settings.value))
    } catch {
      /* ignore */
    }
  }

  async function setNotifications(on: boolean) {
    if (on && permission.value !== 'granted' && permission.value !== 'unsupported') {
      permission.value = await Notification.requestPermission()
      on = permission.value === 'granted'
    }
    settings.value = { ...settings.value, notifications: on }
    save()
  }

  function setChime(on: boolean) {
    settings.value = { ...settings.value, chime: on }
    save()
    if (on) playChime()
  }

  return { settings, permission, setNotifications, setChime }
}

interface AttentionStore {
  sessions: Map<string, SessionInfo>
  connected: boolean
  started: boolean
  /** The first snapshot of the sessions has been applied: an empty map means no sessions, not none read yet (#359). */
  loaded: boolean
  error: string
}

/** The live session store behind useAttention, for readers that must not start it (useEvents). */
export function useAttentionStore() {
  return useState<AttentionStore>('attentionStore', () => ({ sessions: new Map(), connected: false, started: false, loaded: false, error: '' }))
}

/**
 * Live session state for the whole app: one streaming fetch of
 * /api/events (workbench token in the Authorization header, never in the URL)
 * with polling as a fallback. Drives badges, counters, the tab title, the
 * favicon, browser notifications and the chime, as the Events page routes
 * them, and hands every activity entry to useEvents.
 */
export function useAttention() {
  const store = useAttentionStore()
  const version = useState<number>('attentionVersion', () => 0)
  const admin = useWorkbenchToken()
  const { httpBase } = useApiBase()
  const api = useSessions()
  const events = useEvents()
  const unread = useChatUnread()
  const { settings } = useAttentionSettings()
  const toast = useToast()
  const opens = useLinkOpens()

  const sessions = computed(() => {
    void version.value
    return Array.from(store.value.sessions.values()).sort((a, b) => b.createdAt.localeCompare(a.createdAt))
  })

  // The runs, beside the sessions: read on every snapshot (a stream that
  // starts, or starts again), when a run event names one, and when a member
  // session of one changes status; never on a timer. A reply older than one
  // already applied is dropped (RunStore's tickets).
  const runsVersion = useState<number>('attentionRunsVersion', () => 0)
  const runs: RunStore = (runStore ??= new RunStore(
    {
      get: (id) =>
        api.getRun(id).catch((e) => {
          if (e instanceof ApiError && e.status === 404) return null
          throw e
        }),
      list: () => api.listRuns(),
    },
    () => runsVersion.value++,
  ))
  /** Every run the server keeps, newest first. */
  const runList = computed<RunInfo[]>(() => {
    void runsVersion.value
    return runs.list()
  })
  /** The runs' names by id, for the sidebar's run headers. */
  const runNames = computed<Record<string, string>>(() => Object.fromEntries(runList.value.map((r) => [r.id, r.label || r.name])))
  /** The run with this id as last read, if the store has it. */
  function runOf(id: string): RunInfo | undefined {
    void runsVersion.value
    return runs.runs.get(id)
  }
  const needsInput = computed(() => needingInput(sessions.value))
  const count = computed(() => needsInput.value.length)
  const connected = computed(() => store.value.connected)
  /** Whether a snapshot of the sessions has been read: a page that leaves when there is nothing waits for it (#359). */
  const loaded = computed(() => store.value.loaded)
  const error = computed(() => store.value.error)

  function bump() {
    version.value++
  }

  // Each change also settles the activity entries useEvents holds for the
  // session change that carries their state; a snapshot also drops the holds
  // and badges of sessions it no longer lists.
  function replaceAll(list: SessionInfo[]) {
    const next = new Map(list.map((s) => [s.id, liveSession(s)]))
    react(store.value.sessions, next)
    store.value.sessions = next
    store.value.loaded = true
    bump()
    events.retain(new Set(next.keys()))
    events.settle()
  }

  function upsert(raw: SessionInfo) {
    const s = liveSession(raw)
    const prev = new Map(store.value.sessions)
    // A member that starts, ends or goes changes its run as the run reports it.
    if (s.crew && prev.get(s.id)?.status !== s.status) runs.schedule(s.crew.runId)
    store.value.sessions.set(s.id, s)
    react(prev, store.value.sessions)
    bump()
    events.settle(s.id)
  }

  function remove(id: string) {
    const gone = store.value.sessions.get(id)
    if (gone?.crew) runs.schedule(gone.crew.runId)
    store.value.sessions.delete(id)
    bump()
    events.forget(id)
    unread.forget(`session:${id}`)
  }

  /**
   * Alerts for sessions that newly need input, when the Events page routes
   * needs_input to Browser. They come from the session state, so each prompt
   * alerts once. A quiet session's question (a background Brakeman turn's)
   * stays in Needs you and alerts no one (alerting).
   */
  function react(prev: Map<string, SessionInfo>, next: Map<string, SessionInfo>) {
    if (!import.meta.client) return
    const fresh = alerting(newlyNeedingInput(prev, next))
    if (!fresh.length || !events.routes.value.needs_input.browser) return
    for (const s of fresh) notify(`${s.name} needs input`, s.attention?.message || 'The agent is waiting for you.', `conductor-${s.id}`, s.id, s.attention?.id)
    chime()
  }

  /** A session that formed a crew around itself and asked for it to be offered (conductor crew create --open): a toast with Open. Nothing navigates on its own. */
  function offerFormedCrew(sessionId: string, entry: ActivityEntry) {
    if (!import.meta.client) return
    const s = store.value.sessions.get(sessionId)
    const formed = formedCrew(s?.name || sessionId, entry)
    if (!formed) return
    toast.add({
      title: formed.title,
      description: 'The run is in the sidebar. Open shows it.',
      icon: 'i-lucide-users',
      color: 'neutral',
      actions: [{ label: 'Open', icon: 'i-lucide-arrow-right', onClick: () => navigateTo(formed.path) }],
    })
  }

  /** Alerts for any other event the Events page routes to Browser: artifact, tool_denied, error, exit_nonzero by default (eventAlert). */
  function reactToEvent(e: RoutedEvent) {
    const alert = eventAlert(e, events.routes.value)
    if (!alert) return
    notify(alert.title, alert.body, alert.tag, e.sessionId)
    chime()
  }

  /** A browser notification; tapped, it opens the session, or below lg a question's item on the decision page (`question`). */
  function notify(title: string, body: string, tag: string, sessionId: string, question?: string) {
    if (!settings.value.notifications || !('Notification' in window) || Notification.permission !== 'granted') return
    try {
      const n = new Notification(title, { body, tag })
      n.onclick = () => {
        window.focus()
        navigateTo(notificationTarget(window.innerWidth, sessionId, question))
        n.close()
      }
    } catch {
      /* notification blocked */
    }
  }

  function chime() {
    if (!settings.value.chime || Date.now() - lastChime < CHIME_GAP_MS) return
    lastChime = Date.now()
    playChime()
  }

  let abort: AbortController | undefined
  let pollTimer: number | undefined
  let backoff = 1000

  async function stream() {
    // The stream of the token before goes first, even with no token now: after "Forget token" it must not refill the store.
    abort?.abort()
    if (!admin.token.value) return
    abort = new AbortController()
    const signal = abort.signal
    try {
      // A page in a desktop app that opens agents' links says so (#436): while it streams, an agent's link waits for its claim.
      const headers: Record<string, string> = { Authorization: `Bearer ${admin.token.value}` }
      if (desktopBridge()?.openUrl) headers['X-Conductor-Desktop'] = '1'
      const res = await fetch(`${httpBase.value}/api/events`, { headers, signal })
      if (res.status === 401) {
        admin.needsToken.value = true
        store.value.error = 'Workbench token required'
        return
      }
      if (!res.ok || !res.body) throw new Error(`events ${res.status}`)
      store.value.connected = true
      store.value.error = ''
      backoff = 1000
      const reader = res.body.getReader()
      const decoder = new TextDecoder()
      let buf = ''
      for (;;) {
        const { value, done } = await reader.read()
        if (done) break
        buf += decoder.decode(value, { stream: true })
        let idx: number
        while ((idx = buf.indexOf('\n\n')) >= 0) {
          const block = buf.slice(0, idx)
          buf = buf.slice(idx + 2)
          handleBlock(block)
        }
      }
    } catch (e) {
      if (signal.aborted) return
      store.value.error = (e as Error).message
    } finally {
      store.value.connected = false
    }
    if (signal.aborted) return
    // Poll once while the stream is down, then retry with backoff.
    await poll()
    window.setTimeout(() => {
      if (!signal.aborted) stream()
    }, backoff)
    backoff = Math.min(backoff * 2, 15000)
  }

  function handleBlock(block: string) {
    let event = 'message'
    const data: string[] = []
    for (const line of block.split('\n')) {
      if (line.startsWith('event: ')) event = line.slice(7)
      else if (line.startsWith('data: ')) data.push(line.slice(6))
    }
    if (!data.length) return
    try {
      const payload = JSON.parse(data.join('\n'))
      if (event === 'snapshot') {
        replaceAll(payload as SessionInfo[])
        // What changed while the stream was away: every run, once, and every Dispatch page open.
        runs.readAll().catch(() => {})
        dispatchLive().snapshot()
        brakemanLive().snapshot()
        void inboxLive().snapshot()
      } else if (event === 'session') {
        upsert(payload as SessionInfo)
        void inboxLive().event()
      } else if (event === 'removed') {
        remove((payload as { id: string }).id)
        void inboxLive().event()
      } else if (event === 'run') {
        void inboxLive().event()
        const r = payload as { id: string; removed?: boolean }
        if (r.removed) runs.remove(r.id)
        else runs.schedule(r.id)
      } else if (event === 'dispatch') {
        // Something of Dispatch's changed: the pages it names or touches read again (useDispatchLive).
        const e = asDispatchEvent(payload)
        if (e) dispatchLive().event(e)
        void inboxLive().event()
      } else if (event === 'brakeman') {
        // A Brakeman request changed state: the pages' list and the sidebar's count follow (useBrakeman). Nothing takes the focus.
        const e = asBrakemanEvent(payload)
        if (e) brakemanLive().event(e)
        void inboxLive().event()
      } else if (event === 'activity') {
        const { sessionId, ...entry } = payload as SessionActivity
        offerFormedCrew(sessionId, entry)
        events.push(sessionId, entry)
      } else if (event === 'open') {
        // A link an agent opened (#436): the desktop app opens it, a plain tab offers it once no app took it (useLinkOpens).
        opens.event(payload, (id) => store.value.sessions.get(id)?.name || id)
      } else if (event === 'chat') {
        // A chat message of a session or a run: the unread counts, for the threads no page has open.
        const { sessionId, runId, ...m } = payload as { sessionId?: string; runId?: string } & ChatMessage
        if (sessionId || runId) unread.accept(sessionId ? `session:${sessionId}` : `run:${runId}`, m)
      }
    } catch {
      /* ignore malformed event */
    }
  }

  async function poll() {
    if (!admin.token.value) return
    try {
      replaceAll(await api.list())
      // The fallback while the stream is down reads the runs with the sessions, and the decision inbox.
      void inboxLive().changed()
      await runs.readAll()
      store.value.error = ''
    } catch (e) {
      store.value.error = (e as Error).message
    }
  }

  /** Starts the live connection once per app; safe to call from every page. */
  function start() {
    if (!import.meta.client || store.value.started) return
    store.value.started = true
    stopEvents?.()
    stopEvents = events.onEvent(reactToEvent)
    stream()
    pollTimer = window.setInterval(() => {
      // While the stream is down, or open but without its first snapshot yet (a stall before it), the list is read
      // directly, so that a page waiting for the sessions to be read (#359) is not left waiting.
      if ((!store.value.connected || !store.value.loaded) && document.visibilityState === 'visible') poll()
    }, 3000)
    watch(
      () => admin.token.value,
      () => {
        store.value.sessions = new Map()
        bump()
        runs.clear()
        events.reset()
        stream()
      },
    )
  }

  function stop() {
    abort?.abort()
    stopEvents?.()
    stopEvents = undefined
    window.clearInterval(pollTimer)
    store.value.started = false
    store.value.connected = false
  }

  /** Reads one run now (a page that shows it, as it opens); resolves to it, or null when the server does not have it. */
  function refreshRun(id: string): Promise<RunInfo | null> {
    return runs.read(id)
  }

  /** Takes a run an action answered with (a start, a stop, a member added): it is the newest there is. */
  function applyRun(run: RunInfo) {
    runs.apply(run)
  }

  return { sessions, needsInput, count, connected, loaded, error, start, stop, refresh: poll, runs: runList, runNames, runOf, refreshRun, applyRun }
}

/** Tab title prefix and favicon dot while sessions need input. Call once, in app.vue. */
export function useAttentionHead() {
  const { count } = useAttention()
  const faviconHref = ref('/brand/conductor-favicon.svg')
  if (import.meta.client) {
    watch(
      count,
      async (n) => {
        faviconHref.value = await attentionFavicon(n > 0)
      },
      { immediate: true },
    )
  }
  useHead({
    titleTemplate: (t) => {
      const base = t ? `${t} · Conductor` : 'Conductor'
      return count.value > 0 ? `(${count.value}) ${base}` : base
    },
    link: [{ key: 'icon', rel: 'icon', type: computed(() => (faviconHref.value.startsWith('data:') ? 'image/png' : 'image/svg+xml')), href: faviconHref }],
  })
}
