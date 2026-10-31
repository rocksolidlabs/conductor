<script setup lang="ts">
import type { DispatchTicket, DispatchView, Horizon, ProjectRead, ProjectReply, TicketEdit, TicketStatus, TicketSummary, ViewKind } from '~/composables/useDispatch'
import type { AgentInfo } from '~/composables/useSessions'
import type { PageGroup } from '~/composables/usePalette'
import { ApiError } from '~/composables/useApi'
import { listWatch } from '~/composables/useDispatchLive'
import { DISPATCH_SHORTCUTS } from '~/composables/useShortcuts'
import { PICK_KEYS, PICK_WORDS, TRACKERS, listKeepSelected, listToggle, moveEdit, newestRows, patchRows, pickEdit, ticketSummaryOf, targetWords, ticketPath, validProjectKey, withMoves, type CardMove, type PickField } from '~/utils/dispatch'
import { apiQuery, filterFromURL, filterNarrows, filterToURL, type DispatchFilter } from '~/utils/dispatchQuery'
import { serially } from '~/utils/dispatchOrder'

// A project's page (docs/plans/dispatch.md, PR-8 and PR-9): its tickets as a list, a board or a roadmap, filtered and sorted on the server,
// the filter in the link (utils/dispatchQuery.ts), pages of 200; the saved views as chips (DispatchViewsBar.vue); New ticket. The project
// and the list are read through the page's ordering gate, the list by its filter (a filter changed is a new list, the old one's late pages
// dropped), as the page opens and again after the window on any dispatch event of the project, and on a snapshot, which forgets what the
// gate knew (a store restored from a backup). A read that fails is asked again as a re-read is, the first and a filter's among them.
// Nothing polls. The keys (Owner decisions, 10): J K move through the list, Enter opens, X selects, C makes a ticket, S P A L M O pick the
// status, priority, assignees, labels, milestone and owner of the tickets selected or the one focused, each one operation per ticket;
// a card of the board or the roadmap moves by drag and drop or by ← →. The page's group in the palette offers the views, New ticket and
// what the selection can be set to. The project's description is shown above its tickets, rendered, and edited in place (one operation on
// the project's revision); its labels and milestones are managed from the page (DispatchProjectSettings.vue).

const route = useRoute()
const router = useRouter()
const toast = useToast()
const admin = useWorkbenchToken()
const sessions = useSessions()
const { api, gate, read, readOrAbsent } = useDispatch()

/** The project's key, from the link: a key is upper case. */
const project = computed(() => String(route.params.project ?? '').toUpperCase())

const projectRead = ref<ProjectRead | null>(null)
const projectMissing = ref(false)
const tickets = ref<TicketSummary[]>([])
const total = ref(0)
/** The cursor of the page after those shown, '' when they are all. */
const next = ref('')
/** How many pages of the list are shown: a re-read reads them all again. Back from a ticket by Escape, as many as were (backPages). */
const pages = ref(1)
/** The list (a thing of the gate: its project and its filter) whose tickets are shown. */
const shownThing = ref('')
const loading = ref(false)
const error = ref('')
const agents = ref<AgentInfo[]>([])
const now = ref(Date.now())
const newOpen = ref(false)

useHead({ title: computed(() => (projectRead.value ? `${projectRead.value.project.name} · Dispatch` : project.value)) })

// The view, in the link (?view=) and kept per browser as the run page keeps its own: the list, the board or the roadmap.
const VIEW_KEY = 'conductor.dispatchView'
type DispatchViewKind = ViewKind
function isView(v: unknown): v is DispatchViewKind {
  return v === 'list' || v === 'board' || v === 'roadmap'
}
function readView(): DispatchViewKind {
  if (isView(route.query.view)) return route.query.view
  try {
    const v = localStorage.getItem(VIEW_KEY)
    return isView(v) ? v : 'list'
  } catch {
    return 'list'
  }
}
const view = ref<DispatchViewKind>(readView())
watch(
  () => route.query.view,
  (q) => {
    if (isView(q)) view.value = q
  },
)
watch(view, (v) => {
  try {
    localStorage.setItem(VIEW_KEY, v)
  } catch {
    /* storage refused: the choice lasts the page */
  }
  if (route.query.view !== v) void router.replace({ query: { ...route.query, view: v === 'list' ? undefined : v } })
})
const viewItems = [
  { label: 'List', value: 'list', icon: 'i-lucide-list' },
  { label: 'Board', value: 'board', icon: 'i-lucide-square-kanban' },
  { label: 'Roadmap', value: 'roadmap', icon: 'i-lucide-map' },
]

/** The filter, as the link says it; a change is written to the link, which the list is read by. */
const filter = computed<DispatchFilter>({
  get: () => filterFromURL(route.query),
  set: (f) => void router.replace({ query: { ...(isView(route.query.view) ? { view: route.query.view } : {}), ...filterToURL(f) } }),
})
const names = computed(() => ({ labels: projectRead.value?.labels ?? [], milestones: projectRead.value?.milestones ?? [] }))
/** The list's request for the filter shown, without its page: null when the filter can match nothing (a label the project lacks). */
const query = computed(() => apiQuery(project.value, filter.value, names.value))
/** The list as a thing of the gate: by its project and its filter. */
const listThing = computed(() => `list:${project.value}?${query.value ?? 'nothing'}`)
/** The tickets shown are the filter's. */
const listed = computed(() => shownThing.value === listThing.value)

// Back from a ticket by Escape (its page sets backFocus): the list is read as deep as it was shown when the ticket was opened, so a ticket
// opened after More is listed again and its row focused (the watch of backFocus below). Kept as the page goes, for the project and the
// filter of the link; taken only by a way back to that list, and forgotten once its focus is given.
const backFocus = useState<string>('dispatchBackFocus', () => '')
const backPages = useState<{ project: string; filter: string; pages: number } | null>('dispatchBackPages', () => null)
const filterWords = computed(() => JSON.stringify(filterToURL(filter.value)))
/** How many pages a list read afresh starts with: those shown before the ticket, on the way back to the same list; else one. */
function startPages(): number {
  const b = backPages.value
  const back = backFocus.value.startsWith(`${project.value}-`) && b?.project === project.value && b.filter === filterWords.value
  return back ? b.pages : 1
}
pages.value = startPages()
onBeforeUnmount(() => (backPages.value = { project: project.value, filter: filterWords.value, pages: pages.value }))

/** A page's size: the most the store lists at once. */
const PAGE = 200

/** A 404, or a 400 for what is no project's key: no such project, which reading again does not change. */
function noSuch(e: unknown): boolean {
  return e instanceof ApiError && ((e.status === 404 && e.code === 'not_found') || (e.status === 400 && e.code === 'invalid_request'))
}

/** Reads the project; a key no project has says so, through the gate as any reply, so a slow 404 never hides a project read since. */
async function readProject(): Promise<void> {
  const key = project.value
  if (!validProjectKey(key)) {
    projectMissing.value = true
    return
  }
  await readOrAbsent(`project:${key}`, () => api.project(key), {
    apply: (p) => {
      if (key !== project.value) return
      projectRead.value = p
      projectMissing.value = false
    },
    rev: (p) => p.project.rev,
    absent: noSuch,
    gone: () => {
      if (key === project.value) projectMissing.value = true
    },
  })
}

/** Reads the pages of the list shown, as one reply, for the filter shown. Nothing until the project is read: its names resolve the filter. */
async function readList(): Promise<void> {
  if (!projectRead.value || projectMissing.value) return
  const thing = listThing.value
  const q = query.value
  if (q === null) {
    // The filter names labels or a milestone the project lacks: nothing matches, and nothing is asked.
    tickets.value = []
    total.value = 0
    next.value = ''
    shownThing.value = thing
    return
  }
  const want = pages.value
  loading.value = true
  try {
    await read(
      thing,
      async () => {
        const all: TicketSummary[] = []
        let cursor = ''
        let count = 0
        for (let i = 0; i < want; i++) {
          const paging = new URLSearchParams({ limit: String(PAGE), ...(cursor ? { cursor } : {}) }).toString()
          const page = await api.tickets(q ? `${q}&${paging}` : paging)
          all.push(...(page.tickets ?? []))
          count = page.total
          cursor = page.next ?? ''
          if (!cursor) break
        }
        return { all, count, cursor }
      },
      (r) => {
        // A row read before an edit's answer this page applied shows that answer, the newer revision, until a later read has it.
        tickets.value = newestRows(r.all, latest)
        total.value = r.count
        next.value = r.cursor
        shownThing.value = thing
      },
    )
  } finally {
    loading.value = false
  }
}

/** The project and its list read again; rejects when a read failed (the live store asks again). */
async function reload(): Promise<void> {
  try {
    await readProject()
    await readList()
    error.value = ''
  } catch (e) {
    error.value = (e as Error).message
    throw e
  }
}

/**
 * The project and its list read through the live store (DispatchLive.ask), so that a read that fails is asked again after the window,
 * doubled, as a re-read is, until one answers, with no event: as the page opens too, when the stream is connected already and no
 * snapshot comes. None without the workbench token, which is asked for; the page is read once it is given.
 */
function load(): void {
  if (!admin.hasToken.value) {
    admin.needsToken.value = true
    return
  }
  live.ask(reload)
}

let tick: number | undefined
onMounted(() => {
  sessions
    .catalog()
    .then((a) => (agents.value = a))
    .catch(() => {})
  tick = window.setInterval(() => (now.value = Date.now()), 30_000)
})
onBeforeUnmount(() => window.clearInterval(tick))
watch(() => admin.token.value, load)

// The live events read the page again; its first read is made once it is subscribed to them (useDispatchLive's `first`). A snapshot
// forgets the revisions this page's edits were answered with too: the store may have been restored from a backup.
const live = useDispatchLive(
  {
    ...listWatch(() => project.value, reload),
    reset: () => {
      gate.reset()
      latest.clear()
    },
  },
  load,
)

// Another filter is another list: the old one's late pages are dropped, and the new one is read from its first page, through the live
// store (DispatchLive.ask), so that a read that fails is asked again after the window, doubled, as a re-read is, with no further event.
watch(listThing, (_, before) => {
  gate.drop(before)
  pages.value = startPages()
  live.ask(async () => {
    try {
      await readList()
    } catch (e) {
      error.value = (e as Error).message
      throw e
    }
  })
})
// Another project on the same page: what was shown goes.
watch(project, (_, before) => {
  gate.drop(`project:${before}`)
  projectRead.value = null
  projectMissing.value = false
  tickets.value = []
  shownThing.value = ''
  load()
})

function more() {
  pages.value++
  void readList().catch((e: Error) => (error.value = e.message))
}

/**
 * A ticket made, by its key and title (its reply's ticket's, else its receipt's key and the title sent): said with its key and Open, so a
 * ticket the filter leaves out of the list is said too, and the list read at once (its event reads it again too).
 */
function created(t: { key: string; title: string }) {
  toast.add({ title: `${t.key} made`, description: t.title, icon: 'i-lucide-ticket', color: 'success', actions: [{ label: 'Open', icon: 'i-lucide-arrow-right', onClick: () => void router.push(ticketPath(t.key)) }] })
  void readList().catch(() => {})
}

const archived = computed(() => !!projectRead.value?.project.archivedAt)
const labelsById = computed(() => new Map((projectRead.value?.labels ?? []).map((l) => [l.id, l])))
const milestonesById = computed(() => new Map((projectRead.value?.milestones ?? []).map((m) => [m.id, m])))
const narrows = computed(() => filterNarrows(filter.value))

// ---------------------------------------------------------------------------
// The project itself: its description, shown rendered above its tickets and edited in place, and its settings: its close policy, its
// labels and its milestones.

const ABOUT_KEY = 'conductor.dispatchAbout'
/** Whether the description is shown, per browser: shown unless the person folded it. */
function readAbout(): boolean {
  try {
    return localStorage.getItem(ABOUT_KEY) !== 'folded'
  } catch {
    return true
  }
}
const aboutOpen = ref(readAbout())
watch(aboutOpen, (o) => {
  try {
    localStorage.setItem(ABOUT_KEY, o ? 'shown' : 'folded')
  } catch {
    /* storage refused: the choice lasts the page */
  }
})
const description = computed(() => projectRead.value?.project.description ?? '')
const settingsOpen = ref(false)

/** A project's description's bound, the store's, in bytes. */
const MAX_PROJECT_DESCRIPTION = 16 << 10

/**
 * The description saved, made against `base`, the text its editor opened on: none when the project holds it already, and not sent when
 * another person's description came while it was written (the editor then shows theirs). One operation on the project's revision shown.
 */
async function saveDescription(text: string, base: string): Promise<boolean> {
  const read = projectRead.value
  const key = project.value
  if (!read || archived.value) return false
  if (read.project.description !== base) return false
  if (text === read.project.description) return true
  const reply = await ops.run<ProjectReply>((op) => api.editProject(key, { ...op, description: text }), {
    thing: `project:${key}`,
    what: key,
    expectedRev: read.project.rev,
    apply: (r) => {
      if (r.project && key === project.value && projectRead.value) projectRead.value = { ...projectRead.value, project: r.project }
    },
    rev: (r) => r.project?.rev,
    refused: () => void reload().catch(() => {}),
  })
  return !!reply
}

// ---------------------------------------------------------------------------
// Edits from the list, the board, the roadmap and the pickers: one operation per ticket, one after another.

const ops = useDispatchOps(gate)
/**
 * By key, the ticket as the last edit of this page answered with it: a list read sent before that answer and served after it shows the
 * older row, so the row shown and the next edit's base are the newer of the two by revision (newestRows). Forgotten on a snapshot.
 */
const latest = new Map<string, TicketSummary>()

/** A ticket an edit answered with: kept as the latest known, and shown in its row when its revision is the row's or later. */
function showTicket(t: DispatchTicket) {
  const s = ticketSummaryOf(t)
  const known = latest.get(s.key)
  if (!known || s.rev >= known.rev) latest.set(s.key, s)
  tickets.value = patchRows(tickets.value, s)
}

/** A ticket as the page knows it now: its row, or the newer answer of an edit. */
function current(key: string): TicketSummary | undefined {
  const row = tickets.value.find((t) => t.key === key)
  const known = latest.get(key)
  return row && (!known || row.rev >= known.rev) ? row : (known ?? row)
}

/** The page is gone: a ticket's read after an edit that fails is not asked again for it. */
let gone = false
onBeforeUnmount(() => (gone = true))
/**
 * The page was left before it showed what one of its edits did (editTickets' `stranded`): the edits queued behind are not made on what
 * it shows, which may not be the store's, and each says it was not sent, rather than being taken for no change and said done.
 */
let stranded = false

/**
 * The page's edits, one after another (serially): each is made when it is sent, against the tickets as the edits before it left them, so a
 * card moved twice in a row, or moved back, ends where it was put last; editTickets reads a ticket again after an answer that showed
 * nothing of it, and after a failure, before the next edit is made. Refused, the page is read again once for the batch, through the live
 * store, so that a read that fails is asked again. The page left, the edits queued are made while it shows what the ones before did; once
 * it was left before it showed that, none is sent.
 */
const editQueue = serially(async (keys: readonly string[], make: (t: TicketSummary) => TicketEdit | null, done?: (keys: string[]) => string) => {
  const targets = keys.map(current).filter((t): t is TicketSummary => !!t)
  const out = await ops.editTickets(targets, make, { apply: showTicket, refused: () => live.ask(reload), left: () => gone, stranded: () => stranded, done })
  if (out.stranded) stranded = true
  return out
})

// The board's and the roadmap's moves: a card is shown in the column it was moved to until the store answers; refused (a move to Done
// the close policy refuses, in its own words), its entry goes and the card snaps back.
const moves = ref(new Map<string, CardMove & { n: number }>())
let moveN = 0
/** The tickets as the views show them: each card moved in its new column until the store answers. */
const shown = computed(() => withMoves(tickets.value, moves.value))

async function move(key: string, to: CardMove) {
  if (archived.value) return
  const n = ++moveN
  moves.value = new Map(moves.value).set(key, { ...to, n })
  try {
    await editQueue([key], (t) => moveEdit(t, to))
  } finally {
    if (moves.value.get(key)?.n === n) {
      const next = new Map(moves.value)
      next.delete(key)
      moves.value = next
    }
  }
}

// The selection (X on a focused row of the list), kept to the rows still listed.
const selected = ref(new Set<string>())
watch(tickets, (rows) => {
  if (selected.value.size) selected.value = listKeepSelected(selected.value, rows.map((t) => t.key))
})
/** The tickets selected, in the list's order. */
const selectedKeys = computed(() => shown.value.map((t) => t.key).filter((k) => selected.value.has(k)))
function toggleSelected(key: string) {
  selected.value = listToggle(selected.value, key)
}
function clearSelection() {
  selected.value = new Set()
}

// The pickers (S P A L M O): the field of the tickets selected, or of the one focused.
const pickerOpen = ref(false)
const pickerField = ref<PickField>('status')
const pickerKeys = ref<string[]>([])
const pickerTickets = computed(() => pickerKeys.value.map(current).filter((t): t is TicketSummary => !!t))
function openPicker(field: PickField, keys: readonly string[]) {
  if (!keys.length || archived.value || !projectRead.value) return
  pickerField.value = field
  pickerKeys.value = [...keys]
  pickerOpen.value = true
}
/** A pick: one operation per ticket, those already holding it left alone; several tickets say what was set in one toast. */
function picked(value: string | number, adds: boolean) {
  const field = pickerField.value
  const keys = [...pickerKeys.value]
  void editQueue(keys, (t) => pickEdit(field, t, value, adds), keys.length > 1 ? (done) => `${PICK_WORDS[field].title} set on ${targetWords(done)}` : undefined)
}

// ---------------------------------------------------------------------------
// The keys (Owner decisions, 10): plain keys, which pause in a text field and a terminal as every page key does (defineShortcuts), and
// while a dialog is open. The letter that completes one of the layout's G chords (G C, G S, G A) is the chord's.

useShortcutsModal().registerPage(DISPATCH_SHORTCUTS)
const list = useTemplateRef<{ step: (delta: 1 | -1) => void; leave: () => void; focusKey: (key: string) => void; focus: string | null }>('list')
const lastChord = useState<number>('lastChordAt', () => 0)
/** A dialog, a menu (a chip's) or a list of choices (a filter's) is open: its keys are its own. A tooltip shown is none of them. */
function overlaid(): boolean {
  return !!document.querySelector(OVERLAY_SELECTOR)
}
/** The same, read as each key goes down: while it holds, the page has no keys, so a menu's Escape reaches the menu whole (useOverlaid). */
const overlayOpen = useOverlaid()
function paused(): boolean {
  return Date.now() - lastChord.value <= 150 || pickerOpen.value || newOpen.value || overlaid()
}
/** The ticket the keys act on: the card or row with the focus, else the list's roving focus. */
function focusedKey(): string | null {
  const el = document.activeElement?.closest('[data-ticket-row], [data-ticket-key]')
  const key = el?.getAttribute('data-ticket-row') ?? el?.getAttribute('data-ticket-key')
  if (key) return key
  return view.value === 'list' ? (list.value?.focus ?? null) : null
}
/** The tickets a letter acts on: those selected (in the list), else the focused one. */
function targets(): string[] {
  if (view.value === 'list' && selectedKeys.value.length) return selectedKeys.value
  const key = focusedKey()
  return key ? [key] : []
}
function pick(field: PickField) {
  if (paused()) return
  const keys = targets()
  if (!keys.length) {
    toast.add({ title: 'No ticket to set', description: 'J and K move through the list, X selects; on the board, Tab to a card.', icon: 'i-lucide-info', color: 'neutral' })
    return
  }
  openPicker(field, keys)
}
const pageKeys = {
  j: () => !paused() && list.value?.step(1),
  k: () => !paused() && list.value?.step(-1),
  x: () => {
    if (paused() || view.value !== 'list') return
    const key = list.value?.focus ?? null
    if (key) toggleSelected(key)
  },
  c: () => {
    if (!paused() && projectRead.value && !archived.value) newOpen.value = true
  },
  ...Object.fromEntries(Object.entries(PICK_KEYS).map(([letter, field]) => [letter, () => pick(field)])),
  escape: () => {
    // A dialog's or a menu's Escape is its own.
    if (overlaid()) return
    if (selected.value.size) clearSelection()
    else list.value?.leave()
  },
}
defineShortcuts(computed(() => (overlayOpen.value ? {} : pageKeys)))

// Back from a ticket by Escape (its page sets this): the focus returns to its row or card once the view shows the list read for the
// filter, as many pages as were shown (startPages), a card's folded column (the roadmap's Not planned) unfolded first; a ticket the
// filter leaves out is not waited for.
const cards = useTemplateRef<{ focusCard: (key: string) => Promise<boolean> }>('cards')
watch(
  [shown, view, listed],
  () => {
    const key = backFocus.value
    if (!key || !key.startsWith(`${project.value}-`) || !listed.value) return
    backFocus.value = ''
    backPages.value = null
    if (!shown.value.some((t) => t.key === key)) return
    if (view.value === 'list') list.value?.focusKey(key)
    else void cards.value?.focusCard(key)
  },
  { flush: 'post' },
)

// ---------------------------------------------------------------------------
// The saved views (DispatchViewsBar.vue): a chip applies a view, its filter written to the link and its kind shown.

const bar = useTemplateRef<{ views: DispatchView[]; openSave: () => void; apply: (v: DispatchView) => void }>('bar')
/** The link takes the view's filter and kind first; the kind is shown once the link has it, so the view's own watch keeps the filter. */
async function applyView(f: DispatchFilter, kind: ViewKind) {
  await router.replace({ query: { ...(kind !== 'list' ? { view: kind } : {}), ...filterToURL(f) } })
  view.value = kind
}

// ---------------------------------------------------------------------------
// The page's group in the command palette: New ticket, the views, the saved views, and what the selection can be set to.

const palette = usePalette()
const VIEW_WORDS: Record<ViewKind, { label: string; icon: string }> = {
  list: { label: 'Show the list', icon: 'i-lucide-list' },
  board: { label: 'Show the board', icon: 'i-lucide-square-kanban' },
  roadmap: { label: 'Show the roadmap', icon: 'i-lucide-map' },
}
palette.registerPage((): PageGroup => {
  // The selection is the list's, as its bar is.
  const n = view.value === 'list' ? selectedKeys.value.length : 0
  const rows: PageGroup['rows'] = []
  if (n) {
    for (const [letter, field] of Object.entries(PICK_KEYS)) {
      rows.push({ key: `page:selection:${field}`, label: `${PICK_WORDS[field].set} ${n} selected…`, detail: selectedKeys.value.join(', '), icon: PICK_WORDS[field].icon, kbds: [letter.toUpperCase()], disabled: archived.value, run: () => void nextTick(() => openPicker(field, selectedKeys.value)) })
    }
    rows.push({ key: 'page:selection:clear', label: `Clear the selection of ${n}`, icon: 'i-lucide-x', kbds: ['escape'], run: clearSelection })
  }
  rows.push({ key: 'page:new-ticket', label: `New ticket in ${project.value}`, icon: 'i-lucide-ticket-plus', kbds: ['C'], disabled: !projectRead.value || archived.value, run: () => void nextTick(() => (newOpen.value = true)) })
  for (const kind of ['list', 'board', 'roadmap'] as const) {
    if (kind !== view.value) rows.push({ key: `page:view:${kind}`, label: VIEW_WORDS[kind].label, icon: VIEW_WORDS[kind].icon, run: () => (view.value = kind) })
  }
  for (const v of bar.value?.views ?? []) {
    rows.push({ key: `page:saved-view:${v.id}`, label: `Saved view: ${v.name}`, detail: `${v.projectId ? `a ${v.kind} of ${project.value}` : `a ${v.kind} of every project`}`, icon: v.projectId ? VIEW_WORDS[v.kind].icon : 'i-lucide-globe', run: () => bar.value?.apply(v) })
  }
  rows.push({ key: 'page:save-view', label: 'Save the filter as a view…', icon: 'i-lucide-bookmark-plus', disabled: archived.value, run: () => void nextTick(() => bar.value?.openSave()) })
  return { label: `${project.value} · Dispatch`, rows }
})
</script>

<template>
  <UDashboardPanel id="dispatch-project" :ui="{ body: 'p-0 sm:p-0 gap-0' }">
    <template #header>
      <UDashboardNavbar :toggle="false" :ui="{ left: 'min-w-0 flex-1' }" data-project-header>
        <template #leading>
          <UButton icon="i-lucide-arrow-left" color="neutral" variant="ghost" aria-label="Back to the projects" to="/dispatch" class="lg:hidden" />
        </template>
        <template #title>
          <span class="flex min-w-0 items-baseline gap-2">
            <NuxtLink to="/dispatch" class="hidden text-muted hover:underline sm:inline">Dispatch</NuxtLink>
            <span class="hidden text-dimmed sm:inline">/</span>
            <span class="flex-none font-mono" data-project-key>{{ project }}</span>
            <span v-if="projectRead" class="min-w-0 truncate text-[13px] font-normal text-muted" data-project-name>{{ projectRead.project.name }}</span>
          </span>
        </template>
        <template #right>
          <UButton icon="i-lucide-settings-2" color="neutral" variant="ghost" aria-label="The project's settings: its close policy, labels and milestones" :disabled="!projectRead" data-project-settings-open @click="settingsOpen = true"><span class="hidden md:inline">Settings</span></UButton>
          <UButton icon="i-lucide-plus" aria-label="New ticket" :disabled="!projectRead || archived" data-new-ticket-open @click="newOpen = true"><span class="hidden sm:inline">New ticket</span></UButton>
          <FullscreenButton class="hidden sm:inline-flex" />
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <div v-if="error" class="flex-none px-4 pt-3 sm:px-6">
        <UAlert color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="error" />
      </div>

      <div v-if="projectMissing" class="flex flex-1 flex-col items-center justify-center gap-3 p-8 text-muted" data-project-missing>
        <UIcon name="i-lucide-search-x" class="size-8" />
        <p class="text-sm">No project has the key <code>{{ project }}</code>.</p>
        <UButton label="Dispatch" icon="i-lucide-arrow-left" color="neutral" variant="soft" to="/dispatch" />
      </div>

      <template v-else>
        <div class="flex min-w-0 flex-none flex-col gap-2.5 px-4 pt-3 pb-2 sm:px-6">
          <UAlert v-if="archived" color="neutral" variant="subtle" icon="i-lucide-archive" :title="`${project} is archived.`" description="Its tickets are read as they are; nothing of them can be changed until it is unarchived." />
          <div v-if="projectRead && aboutOpen" class="max-h-72 overflow-y-auto" data-project-about>
            <TicketMarkdown :text="description" label="Description" :empty="`Add a description of ${project}…`" :max-bytes="MAX_PROJECT_DESCRIPTION" :save="saveDescription" :disabled="archived" name="project-description" />
          </div>
          <div class="flex flex-wrap items-center gap-3">
            <UButton :icon="aboutOpen ? 'i-lucide-chevron-up' : 'i-lucide-info'" size="xs" color="neutral" variant="ghost" :label="aboutOpen ? 'Fold' : 'About'" :aria-expanded="aboutOpen" :aria-label="aboutOpen ? 'Fold the description' : `About ${project}`" data-project-about-toggle @click="aboutOpen = !aboutOpen" />
            <UTabs v-model="view" :items="viewItems" :content="false" size="xs" color="neutral" data-project-view />
            <a
              v-if="projectRead?.project.tracker === 'external' && projectRead.project.trackerUrl"
              :href="projectRead.project.trackerUrl"
              target="_blank"
              rel="noopener noreferrer"
              class="flex min-w-0 items-center gap-1 text-xs text-muted hover:underline"
              :title="`Its tickets are tracked at ${projectRead.project.trackerUrl}`"
              data-project-tracker
            >
              <UIcon :name="TRACKERS.external.icon" class="size-3.5 flex-none" />
              <span class="truncate">{{ projectRead.project.trackerUrl }}</span>
            </a>
            <span v-if="listed" class="ml-auto text-xs text-muted tabular-nums" data-list-total :data-count="total">{{ total }} {{ total === 1 ? 'ticket' : 'tickets' }}{{ narrows ? ' match' : '' }}</span>
          </div>
          <DispatchViewsBar v-if="projectRead" ref="bar" :project="project" :filter="filter" :kind="view" :names="names" :archived="archived" @apply="applyView" />
          <DispatchFilters v-model="filter" :labels="projectRead?.labels ?? []" :milestones="projectRead?.milestones ?? []" :agents="agents" />
          <!-- The selection (X): what its tickets can be set to, one operation each, as the letters and the palette's rows do. -->
          <div v-if="view === 'list' && selectedKeys.length" class="flex flex-wrap items-center gap-1.5 rounded-md bg-elevated/60 px-2 py-1.5" data-selection-bar :data-count="selectedKeys.length">
            <span class="mr-1 text-xs font-medium text-highlighted tabular-nums">{{ selectedKeys.length }} selected</span>
            <UButton v-for="(field, letter) in PICK_KEYS" :key="field" size="xs" color="neutral" variant="outline" :icon="PICK_WORDS[field].icon" :disabled="archived" :data-selection-pick="field" @click="openPicker(field, selectedKeys)">
              {{ PICK_WORDS[field].title }}…<UKbd :value="String(letter).toUpperCase()" size="sm" class="ml-0.5 hidden sm:inline-flex" />
            </UButton>
            <UButton size="xs" color="neutral" variant="ghost" icon="i-lucide-x" label="Clear" class="ml-auto" data-selection-clear @click="clearSelection" />
          </div>
        </div>

        <div v-if="!listed" class="flex flex-1 items-center justify-center p-8 text-sm text-muted">Reading the tickets…</div>
        <template v-else-if="view === 'list'">
          <div v-if="!tickets.length" class="flex flex-1 flex-col items-center justify-center gap-3 p-8 text-center text-muted" data-list-empty>
            <UIcon name="i-lucide-ticket" class="size-8" />
            <p class="text-sm">{{ narrows ? 'No ticket matches the filter.' : `${project} has no tickets yet.` }}</p>
            <UButton v-if="!narrows && !archived" label="New ticket" icon="i-lucide-plus" @click="newOpen = true" />
          </div>
          <div v-else class="min-h-0 flex-1 overflow-y-auto border-t border-default">
            <DispatchTicketList ref="list" :tickets="shown" :labels="labelsById" :milestones="milestonesById" :agents="agents" :now="now" :selected="selected" @toggle="toggleSelected" />
            <div v-if="next" class="flex justify-center p-3">
              <UButton :label="`More (${tickets.length} of ${total})`" icon="i-lucide-chevrons-down" color="neutral" variant="soft" size="sm" :loading="loading" data-list-more @click="more" />
            </div>
          </div>
        </template>
        <template v-else>
          <DispatchBoard v-if="view === 'board'" ref="cards" :tickets="shown" :labels="labelsById" :agents="agents" :locked="archived" @move="(key: string, status: TicketStatus) => move(key, { status })" />
          <DispatchRoadmap v-else ref="cards" :tickets="shown" :labels="labelsById" :milestones="projectRead?.milestones ?? []" :agents="agents" :locked="archived" @move="(key: string, horizon: Horizon) => move(key, { horizon })" />
          <div v-if="next" class="flex flex-none justify-center border-t border-default p-2">
            <UButton :label="`More (${tickets.length} of ${total})`" icon="i-lucide-chevrons-down" color="neutral" variant="soft" size="sm" :loading="loading" data-list-more @click="more" />
          </div>
        </template>
      </template>
    </template>
  </UDashboardPanel>

  <DispatchProjectSettings v-if="projectRead" v-model:open="settingsOpen" :project="project" :read="projectRead" :gate="gate" :archived="archived" @changed="() => void reload().catch(() => {})" />
  <DispatchNewTicket v-model:open="newOpen" :project="project" :gate="gate" @created="created" />
  <DispatchPicker v-model:open="pickerOpen" :field="pickerField" :tickets="pickerTickets" :labels="projectRead?.labels ?? []" :milestones="projectRead?.milestones ?? []" :agents="agents" @pick="picked" />
</template>
