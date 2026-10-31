import type { AgentInfo, CrewSummary, SessionInfo } from '~/composables/useSessions'
import type { ProjectSummary, TicketSummary } from '~/composables/useDispatch'
import type { Action } from '~/utils/actions'
import { DispatchOrder, serially, through } from '~/utils/dispatchOrder'
import { allPages, paletteListed, withPatched } from '~/utils/dispatch'
import { MODE_PREFIX, type PaletteMode, type PaletteRow } from '~/utils/palette'
import type { RowPrompt } from '~/utils/sidebarActions'
import type { TouchedSession } from '~/utils/touchedIndex'

/**
 * A row of the command palette (#302): what the pure ranking sees (key,
 * label, detail, words) and what the component renders and runs. Nothing
 * runs before Enter; a row with `ask` asks in the row first; a row with
 * `field` takes a line in the row (a broadcast, a run's name, a reply).
 */
export interface PaletteItem extends PaletteRow {
  icon?: string
  /** An agent chip with the status dot, in the sidebar's grammar. */
  agentId?: string
  state?: 'needs' | 'running' | 'idle' | 'exited'
  /** The keys that run it, shown as a hint. */
  kbds?: string[]
  /** A Needs-you row: the session and its prompt, answered in the row. */
  session?: SessionInfo
  prompt?: RowPrompt | null
  /** Enter. What it returns is not read. */
  run: () => unknown
  /** → reveals these; their keys run them while the row has the focus. */
  actions?: Action[]
  /** The row asks before it runs. */
  ask?: { title: string; detail: string }
  /** The row takes a line (a broadcast, a run's name): the action `field` among its actions opens it, or Enter when `fieldOnEnter`; Enter in it submits. */
  field?: { placeholder: string; maxLength?: number; button?: string; note?: string; submit: (text: string) => unknown; /** What the field opens with: a line refused last time (#322). */ draft?: string }
  fieldOnEnter?: boolean
  /** ⌘↵ opens it in a new tab. */
  href?: string
  /** ⇧↵ shows it in the Yard. */
  yardId?: string
  disabled?: boolean
  /** A switch: shown on or off; Enter flips it. */
  on?: boolean
  /** Enter runs it and the palette stays open (a switch does too). */
  keepOpen?: boolean
  /** Answer all (#303): choice `i` answers every session of the row's group, one send each. */
  answerAll?: (i: number) => void
  /** A line under the detail saying why the row will not act (a crew on another machine). */
  note?: string
  /** A run's member: `R` opens its run. */
  runId?: string
}

/** What a page registers (`usePalette().registerPage`): its own group, first after Needs you and first under `>`. */
export interface PageGroup {
  label: string
  rows: PaletteItem[]
  /** `:` mode: the page's open tabs, changes and commits, in that order. */
  files?: PaletteItem[]
  /** `@` mode: the people on the page's session and its links. */
  people?: PaletteItem[]
  /** The session the page shows: what `:path` opens into, and the directory a relative path is read against. */
  sessionId?: string
  cwd?: string
  /** The page's editor (its own useEditorTabs: one per page): what `:path` and the `open` verb open a file with. */
  openFile?: (loc: { path: string; line?: number }) => void
  /** The session page's inspector: a session row's Open Files / Chat / Activity / People on that page's own session. */
  showView?: (v: 'explorer' | 'chat' | 'activity' | 'people') => void
}

const pageGetter = shallowRef<(() => PageGroup | null) | null>(null)
/** The Touched cache's generation: bumped as the token is forgotten, so a reply on its way is dropped (#304, whole-batch round). */
let touchedGen = 0
/**
 * The Tickets cache's generation, bumped as the token is forgotten, and its ordering gate (utils/dispatchOrder.ts), one for the app: the
 * cache's reads and the palette's edits of tickets (CommandPalette.vue's pickers, usePaletteRows' Assign to me) go through it, so a snapshot
 * that resets it forgets the revisions both knew (a store restored from a backup), and the next edit expects the revision the store has.
 */
let ticketsGen = 0
const ticketsGate = new DispatchOrder()
/** The tickets the palette's edits answered with, by key, until a read carries them at that revision or later (withPatched). */
let ticketsPatched = new Map<string, TicketSummary>()
/** Bumped as the cache is marked behind the store (an event, a snapshot): a read asked before it answers what it read, but not fresh. */
let ticketsStale = 0
/**
 * The palette's edits of tickets (CommandPalette.vue's pickers, usePaletteRows' Assign to me), one after another (serially), as a project
 * page's are: each is made once the one before has answered and the cache shows what it left (editTickets' catch-up), on the ticket as
 * the cache knows it then (editTicket), so a pick made while the one before is on its way (Backlog picked back while Ready is sent) is
 * made on what that one left and sent, not taken for no change on the older row the picker was opened on.
 */
const ticketEdits = serially((edit: () => Promise<unknown>) => edit())

/** The open tickets the palette reads, of every project, the latest updated first: at most this many (five pages of 200). */
export const PALETTE_TICKETS_MAX = 1000
/** A ticket the Tickets group lists: one not done or cancelled. */
export const OPEN_STATUSES = ['backlog', 'ready', 'in_progress', 'in_review'] as const

/** How old the cached crews may be when the palette opens before they are read again. */
const LISTS_STALE_MS = 60_000

export function usePalette() {
  const open = useState<boolean>('paletteOpen', () => false)
  const query = useState<string>('paletteQuery', () => '')
  const crews = useState<CrewSummary[]>('paletteCrews', () => [])
  const catalog = useState<AgentInfo[]>('paletteCatalog', () => [])
  const listsAt = useState<number>('paletteListsAt', () => 0)
  /** The Touched index across sessions (#304): the server's reply, read after the first paint and cached as the lists are. */
  const touched = useState<TouchedSession[]>('paletteTouched', () => [])
  const touchedAt = useState<number>('paletteTouchedAt', () => 0)
  /** A row to open on and ask (X on a session page: its Stop row), taken as the palette opens. */
  const pendingAsk = useState<string | null>('paletteAsk', () => null)
  /**
   * Dispatch in the palette (docs/plans/dispatch.md, "web/app"): the open tickets of every project and the projects, read after the first
   * paint of an open palette and cached a minute as the crews are, read again on a dispatch event while it is open.
   */
  const tickets = useState<TicketSummary[]>('paletteTickets', () => [])
  const projects = useState<ProjectSummary[]>('paletteProjects', () => [])
  const ticketsAt = useState<number>('paletteTicketsAt', () => 0)
  /** A new ticket asked for from a row (New ticket in CND): the project's key, taken by the palette, which opens the dialog. */
  const newTicketFor = useState<string>('paletteNewTicket', () => '')
  const api = useSessions()
  const dispatch = useDispatchApi()
  const admin = useWorkbenchToken()

  /** A page's group, registered in its setup and gone with it, as useShortcutsModal().registerPage does. */
  function registerPage(get: () => PageGroup | null) {
    pageGetter.value = get
    onBeforeUnmount(() => {
      if (pageGetter.value === get) pageGetter.value = null
    })
  }
  const page = computed<PageGroup | null>(() => pageGetter.value?.() ?? null)

  /**
   * The two lists that need a request (principle 1): read after the first
   * paint of an open palette and cached for the session, read again when
   * they are a minute old or a run started since.
   */
  let loading: Promise<void> | null = null
  function loadLists(force = false): Promise<void> {
    if (!admin.hasToken.value) return Promise.resolve()
    if (loading) return loading
    if (!force && listsAt.value && Date.now() - listsAt.value < LISTS_STALE_MS) return Promise.resolve()
    loading = (async () => {
      try {
        const [c, a] = await Promise.all([api.listCrews().catch(() => ({ crews: crews.value, total: 0 })), api.catalog().catch(() => catalog.value)])
        crews.value = c.crews
        catalog.value = a
        listsAt.value = Date.now()
      } finally {
        loading = null
      }
    })()
    return loading
  }

  /**
   * The Touched index (#304), read as the lists are: after the first paint,
   * again when it is a minute old at an open, and (the component's doing)
   * when a `file` event arrives while the palette is open. A read that
   * fails keeps what is cached: the feed's own entries lay over it.
   */
  let loadingTouched: Promise<void> | null = null
  function loadTouched(force = false): Promise<void> {
    if (!admin.hasToken.value) return Promise.resolve()
    if (loadingTouched) return loadingTouched
    if (!force && touchedAt.value && Date.now() - touchedAt.value < LISTS_STALE_MS) return Promise.resolve()
    const gen = touchedGen
    loadingTouched = (async () => {
      try {
        const r = await api.touched()
        // A reply for a token since forgotten is dropped with it.
        if (gen !== touchedGen) return
        touched.value = r.sessions
        touchedAt.value = Date.now()
      } catch {
        /* the cache stands */
      } finally {
        loadingTouched = null
      }
    })()
    return loadingTouched
  }
  /**
   * The cache goes with the token (whole-batch round): another token, or none, must not see the last one's sessions and
   * files, and a reply on its way for the old token is dropped. The component watches the token and calls this.
   */
  function forgetTouched() {
    touchedGen++
    touched.value = []
    touchedAt.value = 0
  }

  /**
   * The open tickets of every project (at most PALETTE_TICKETS_MAX, the latest updated first) and the projects not archived, read as the
   * lists are: after the first paint, again when they are a minute old at an open, and on a dispatch event while the palette is open
   * (CommandPalette.vue). Through the app's Tickets gate (ticketsGate): a slow reply never replaces a newer one, nor the ticket an edit
   * answered with since (withPatched). A read that fails keeps what is cached, and says it failed.
   */
  let loadingTickets: Promise<void> | null = null
  /** A forced read asked while one was on its way, shared by every forced ask meanwhile: that one may have been served before what asked it. */
  let followUp: Promise<void> | null = null
  /**
   * Reads the cache; resolves once read, rejects when the read failed, which keeps what is cached (the live store asks again, as it does a
   * page's re-read: CommandPalette.vue). A forced read asked while one is on its way follows it, and answers as that follow-up does.
   */
  function loadTickets(force = false): Promise<void> {
    if (!admin.hasToken.value) return Promise.resolve()
    if (loadingTickets) {
      if (!force) return loadingTickets
      followUp ??= loadingTickets
        .catch(() => {})
        .then(() => {
          followUp = null
          return loadTickets(true)
        })
      return followUp
    }
    if (!force && ticketsAt.value && Date.now() - ticketsAt.value < LISTS_STALE_MS) return Promise.resolve()
    const gen = ticketsGen
    const stale = ticketsStale
    loadingTickets = (async () => {
      try {
        await through(
          ticketsGate,
          'tickets',
          async () => {
            const open = new URLSearchParams(OPEN_STATUSES.map((s) => ['status', s]))
            open.set('sort', 'updated')
            open.set('desc', '1')
            open.set('limit', '200')
            const all = await allPages(async (cursor) => {
              const page = await dispatch.tickets(`${open.toString()}${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`)
              return { items: page.tickets ?? [], next: page.next }
            }, PALETTE_TICKETS_MAX)
            // Every page of the projects: each not archived gains its New ticket row.
            const projects = await allPages(async (cursor) => {
              const page = await dispatch.projects(cursor)
              return { items: page.projects ?? [], next: page.next }
            })
            return { tickets: all, projects: projects.filter((x) => !x.archivedAt) }
          },
          (r) => {
            if (gen !== ticketsGen) return
            // A read served before an edit's commit and answered after its answer shows that answer's ticket, not the older row.
            const merged = withPatched(r.tickets, ticketsPatched)
            ticketsPatched = merged.keep
            tickets.value = merged.rows
            projects.value = r.projects
            // Marked behind the store while it was on its way: what it read stands, and the next open reads again.
            ticketsAt.value = stale === ticketsStale ? Date.now() : 0
          },
        )
      } finally {
        loadingTickets = null
      }
    })()
    return loadingTickets
  }
  /** The cache is behind the store (an event came while the palette was closed): the next open reads it again. */
  function staleTickets() {
    ticketsStale++
    ticketsAt.value = 0
  }
  /** A stream started again: what the Tickets gate knew is forgotten (a store restored from a backup), and the cache is read again at the next open. */
  function resetTickets() {
    ticketsGate.reset()
    ticketsPatched = new Map()
    ticketsStale++
    ticketsAt.value = 0
  }
  /** A ticket an edit made from the palette answered with: shown at once, and gone from the group once it is no longer open. */
  function patchTicket(t: TicketSummary) {
    const known = ticketsPatched.get(t.key)
    if (!known || t.rev >= known.rev) ticketsPatched.set(t.key, t)
    tickets.value = tickets.value.flatMap((x) => (x.key !== t.key || t.rev < x.rev ? [x] : paletteListed(t) ? [t] : []))
  }
  /** A ticket as the cache knows it now: its row, or the newer answer of an edit; `t` itself when it knows neither (no longer open). */
  function knownTicket(t: TicketSummary): TicketSummary {
    const row = tickets.value.find((x) => x.key === t.key)
    const patched = ticketsPatched.get(t.key)
    if (row && patched) return patched.rev > row.rev ? patched : row
    return patched ?? row ?? t
  }
  /** An edit of a ticket from the palette: queued behind the palette's others (ticketEdits), and made on the ticket as the cache knows it when it starts. */
  function editTicket<R>(t: TicketSummary, edit: (t: TicketSummary) => Promise<R>): Promise<R> {
    return ticketEdits(() => edit(knownTicket(t))) as Promise<R>
  }
  /** The cache goes with the token, as Touched's does: another token must not see the last one's tickets, nor a reply on its way. */
  function forgetTickets() {
    ticketsGen++
    ticketsPatched = new Map()
    tickets.value = []
    projects.value = []
    ticketsAt.value = 0
  }
  /** Asks the palette to open New ticket for a project (a row's). */
  function askNewTicket(project: string) {
    newTicketFor.value = project
  }

  function show(mode: PaletteMode = 'search') {
    query.value = mode === 'search' ? '' : MODE_PREFIX[mode]
    open.value = true
  }
  function close() {
    open.value = false
  }
  function toggle() {
    if (open.value) close()
    else show()
  }
  /** Opens on the row `key`, asking its question (a Stop): Enter stops, Escape cancels. */
  function askRow(key: string) {
    pendingAsk.value = key
    query.value = ''
    open.value = true
  }

  return { open, query, show, close, toggle, askRow, pendingAsk, registerPage, page, crews, catalog, loadLists, touched, loadTouched, forgetTouched, tickets, projects, loadTickets, staleTickets, resetTickets, patchTicket, editTicket, forgetTickets, newTicketFor, askNewTicket, ticketsGate }
}
