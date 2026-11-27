<script setup lang="ts">
import type { RunInfo, SessionInfo } from '~/composables/useSessions'
import { scopeItems } from '~/utils/chat'
import { broadcastByName, broadcastSelection, broadcastSummary, crewFeed, memberStatus, skipWords, takeViewLink } from '~/utils/crews'
import { stopRunPageQuestion, type Action } from '~/utils/actions'
import { taskActions, taskWords } from '~/utils/tasks'
import { attemptSession, markSection, sessionStatus, taskStatus } from '~/utils/status'
import { rowPrompt } from '~/utils/sidebarActions'
import { sessionRow } from '~/utils/sidebar'
import type { PaletteItem } from '~/composables/usePalette'
import { memberLook, runTitle, startedClock } from '~/utils/crewWords'
import { memberOffer, runOffer } from '~/utils/runs'
import { handoffsOf } from '~/utils/crewGraph'
import { RUN_LAYOUTS, RUN_LAYOUT_LOOK, type RunLayout, type RunWallTile } from '~/utils/wall'
import { choiceFor, choose, crewOfRun, largeOf, readRunTiles, writeRunTiles, type RunTilesChoice } from '~/utils/runTiles'
import type { RelatedTicket } from '~/composables/useDispatch'
import { chainLinkedTicket, latest } from '~/utils/review'
import { runLinkWatch, runTicketRead } from '~/composables/useDispatchLive'
import { previewPath } from '~/utils/capture'
import type { BrakemanRequest } from '~/utils/brakeman'
import { askChips, askDisabled } from '~/utils/brakemanAsk'

// The run page: a tile for every member of one run, its activity and a
// broadcast bar. Sessions and the run come from the live store (useAttention):
// the run, with its member states, branches and diff stats, is read as the page
// opens and again whenever a run event or a member session's change says it
// changed. Nothing polls. The header's chip names the Dispatch ticket the run is
// linked to (PR-11), read with the page and again on every ticket event of
// Dispatch's and every snapshot, through an ordering gate as every Dispatch read.

const route = useRoute()
const router = useRouter()
const api = useSessions()
const admin = useWorkbenchToken()
const live = useAttention()
const events = useEvents()
const toast = useToast()
const { create } = useTerminalTransport()
const previewOpen = usePreviewOpen()
const candidatePreview = useCandidatePreview(() => String(route.params.run))

const runId = computed(() => String(route.params.run))
/** The record of a run the server no longer holds, as the page read it (`record`): shown read-only, never among the live runs. */
const recordRun = ref<RunInfo | null>(null)
/** The run as the live store last read it, else its record. */
const run = computed<RunInfo | null>(() => live.runOf(runId.value) ?? recordRun.value)
const error = ref('')
const gone = ref(false)
const now = ref(Date.now())
/** The view link of the launch that opened this page, shown once: it lives in this page alone and goes with it, or when dismissed. */
const launchLink = ref(takeViewLink(String(route.params.run)))
const copy = useCopy()

useHead({ title: computed(() => (run.value ? `${runTitle(run.value)} · run` : 'Run')) })

/** Reads the run as the page opens: its diff stats are read with it. Later reads are the live store's. */
async function load() {
  if (!admin.hasToken.value) {
    admin.needsToken.value = true
    return
  }
  const id = runId.value
  try {
    const r = await live.refreshRun(id)
    if (id !== runId.value) return
    error.value = ''
    gone.value = !r
    recordRun.value = r?.record ? r : null
  } catch (e) {
    if (id === runId.value) error.value = (e as Error).message
  }
}

// The Dispatch ticket the run is linked to, for the header's chip: the live ticket whose live run link names it, else the latest-linked
// tombstone, said deleted, else none. Read through the page's ordering gate (a slow answer never replaces a later one) as the page opens,
// again after the window on every ticket event (a link's add or remove, a ticket's delete or undelete: the event names no run, and a run
// linked to nothing has no project to narrow by), and on a snapshot, which forgets what the gate knew. A read that fails is asked again
// as a re-read is. A server with no Dispatch store (no data directory, a store that could not open, a switchyard) answers no chip.
const dispatch = useDispatch()
const runTicket = ref<RelatedTicket | null>(null)
/** Counts the answers of the run's link read: each looks for the ticket of a continuation's chain again (chainTicket). */
const runTicketReads = ref(0)
async function readRunTicket(): Promise<void> {
  if (!admin.hasToken.value) return
  const id = runId.value
  await runTicketRead(dispatch.gate, id, () => dispatch.api.runTicket(id), (t) => {
    if (id !== runId.value) return
    runTicket.value = t
    runTicketReads.value++
  })
}
const chip = useDispatchLive({ ...runLinkWatch(readRunTicket), reset: () => dispatch.gate.reset() }, () => chip.ask(readRunTicket))

// The ticket a task's Review and Prepare a repair go to (RV1): the one the run was launched from, else the one whose run link names it,
// else, for a run of a chain linked by hand (no link names this run, no package binds it), the ticket whose link names a run of its
// chain, back along it and then forward, the continuations that continue it (chainLinkedTicket): Dispatch gives the whole chain to that
// ticket. Looked for again whenever the run's link is read again (a link added, removed) and when the runs it continues or that continue
// it move. A ticket none of these reads finds is not shown: the task says to link a run of its chain.
const chainTicket = ref('')
const chainAsks = latest()
watch(
  () => [runId.value, run.value?.ticket?.key ?? '', runTicket.value, run.value?.resumedFrom ?? '', run.value?.resumedBy ?? '', runTicketReads.value] as const,
  async ([id, launched, linked, from, by]) => {
    const current = chainAsks.ask()
    if (launched || linked || (!from && !by) || !admin.hasToken.value) {
      chainTicket.value = ''
      return
    }
    const key = await chainLinkedTicket(
      { from, by },
      { ticket: (r) => dispatch.api.runTicket(r), resumedFrom: async (r) => (await api.getRun(r)).resumedFrom, resumedBy: async (r) => (await api.getRun(r)).resumedBy },
    )
    if (current() && id === runId.value) chainTicket.value = key
  },
  { immediate: true },
)
const reviewTicket = computed(() => run.value?.ticket?.key || runTicket.value?.key || chainTicket.value)

/** An action (add, start, stop) answered with the run as it is now. */
function changed(r: RunInfo) {
  live.applyRun(r)
}

/** The run's sessions in the live store. */
const sessions = computed(() => live.sessions.value.filter((s) => s.crew?.runId === runId.value))

/**
 * One tile per member in the run's order, then any session of the run the last read did not know yet. A managed run's member has one
 * session per attempt over time: its tile is its latest, and the earlier attempts' sessions get no tile of their own.
 */
const tiles = computed<RunWallTile[]>(() => {
  const out: RunWallTile[] = []
  const seen = new Set<string>()
  const names = new Set<string>()
  for (const m of run.value?.members ?? []) {
    const s = sessions.value.find((x) => (m.sessionId ? x.id === m.sessionId : x.crew?.member === m.name))
    if (s) seen.add(s.id)
    names.add(m.name)
    out.push({ name: m.name, member: m, session: s })
  }
  for (const s of sessions.value) {
    if (seen.has(s.id)) continue
    if (run.value?.managed && s.crew?.member && names.has(s.crew.member)) continue
    out.push({ name: s.crew?.member ?? s.name, session: s })
  }
  return out
})


// Broadcast selection: every member whose session runs, unless the person
// unticked it; a member that starts later is selected as it appears. Only the
// person's own ticks are kept (choices), so no read of the run and no run
// event clears one; another run starts afresh.
const choices = ref<Record<string, boolean>>({})
const selection = computed(() =>
  broadcastSelection(
    tiles.value.map((t) => ({
      name: t.name,
      live: !!t.session && (t.session.status === 'running' || t.session.status === 'starting'),
      waiting: t.session?.attention?.state === 'needs_input',
    })),
    choices.value,
  ),
)
function isSelected(name: string) {
  return selection.value.selected.includes(name)
}
function toggle(name: string, on: boolean | 'indeterminate') {
  choices.value = { ...choices.value, [name]: on === true }
}

watch(runId, (_, before) => {
  choices.value = {}
  gone.value = false
  load()
  dispatch.gate.drop(`run-ticket:${before}`)
  runTicket.value = null
  chip.ask(readRunTicket)
})

// Activity: the members' events in the live feed and the run's own log.
const memberOf = computed(() => {
  const m = new Map<string, string>()
  for (const s of sessions.value) m.set(s.id, s.crew?.member ?? s.name)
  for (const x of run.value?.members ?? []) if (x.sessionId) m.set(x.sessionId, x.name)
  return m
})
const feed = computed(() => crewFeed(events.entries.value, run.value?.log ?? [], memberOf.value))

// The view: the tile grid, the graph of the members and their handoffs (a managed run's tasks and their needs), the timeline, or a
// managed run's tasks. The choice is this browser's.
const VIEW_KEY = 'conductor.runView'
type RunView = 'grid' | 'graph' | 'timeline' | 'tasks'
function isView(v: unknown): v is RunView {
  return v === 'grid' || v === 'graph' || v === 'timeline' || v === 'tasks'
}
function readView(): RunView {
  // ?view= (#303) names the view a link opens on; else this browser's last choice.
  if (isView(route.query.view)) return route.query.view
  try {
    const v = localStorage.getItem(VIEW_KEY)
    return isView(v) ? v : 'grid'
  } catch {
    return 'grid'
  }
}
const view = ref<RunView>(readView())
watch(
  () => route.query.view,
  (q) => {
    if (isView(q)) view.value = q
  },
)
// The view is the app's screens' state (useUiState): kept by the server too, and taken up when its copy arrives, unless a link
// named one.
const ui = useUiState()
watch(
  () => ui.layout.value.runView,
  (v) => {
    if (v && !isView(route.query.view) && v !== view.value) view.value = v
  },
)
watch(view, (v) => {
  try {
    localStorage.setItem(VIEW_KEY, v)
  } catch {
    /* storage refused: the choice lasts the page */
  }
  ui.setLayout({ runView: v })
})
const viewItems = computed(() => [
  ...(run.value?.managed ? [{ label: 'Tasks', value: 'tasks', icon: 'i-lucide-list-checks' }] : []),
  { label: 'Members', value: 'grid', icon: 'i-lucide-users' },
  { label: 'Graph', value: 'graph', icon: 'i-lucide-git-fork' },
  { label: 'Timeline', value: 'timeline', icon: 'i-lucide-chart-gantt' },
])
/** The view shown: Tasks is a managed run's alone, so a legacy run asked for it gets the grid. */
const shownView = computed(() => (view.value === 'tasks' && !run.value?.managed ? 'grid' : view.value))

// How the Members view draws the crew's tiles (runTiles): their layout, the member One large draws large, and whether Activity shows
// beside them. Kept for the crew in this app's storage; a crew opened for the first time takes the last choice made for any crew.
function pageStorage(): Storage | null {
  try {
    return localStorage
  } catch {
    return null
  }
}
const tilesRecord = ref(readRunTiles(pageStorage()))
const crew = computed(() => crewOfRun(runId.value, run.value?.crewId))
const tilesChoice = computed(() => choiceFor(tilesRecord.value, crew.value))
function chooseTiles(change: Partial<RunTilesChoice>) {
  tilesRecord.value = choose(tilesRecord.value, crew.value, change)
  writeRunTiles(pageStorage(), tilesRecord.value)
}
function setActivity(on: boolean) {
  chooseTiles({ activity: on })
}
const tilesLayout = computed({ get: () => tilesChoice.value.layout, set: (l: RunLayout) => chooseTiles({ layout: l }) })
const largeMember = computed(() => largeOf(tilesChoice.value, tiles.value.map((t) => t.name)))
/** Whether the wall stacks its tiles (a narrow area), where no layout applies and the switch is not drawn. */
const wallStacked = ref(false)

// The task selected in the Tasks view (`?task=`), in the URL so a link opens on it (#334, row 7).
const selectedTask = computed(() => (typeof route.query.task === 'string' ? route.query.task : ''))
// A history row's link (Program item HS1, PR-6): `&attempt=<n>&step=<phase>/<id>/<k>` beside `?task=` opens that attempt's step and
// its log with its SHA-256; `?command=<id>&actor=<actor>` that command's receipt. Read as `?task=` is, never written back.
const wantedAttempt = computed(() => {
  const n = typeof route.query.attempt === 'string' && /^[1-9]\d{0,5}$/.test(route.query.attempt) ? Number(route.query.attempt) : 0
  return n || undefined
})
const wantedStep = computed(() => (typeof route.query.step === 'string' && route.query.step ? route.query.step : undefined))
const wantedCommand = computed(() => (typeof route.query.command === 'string' && route.query.command ? route.query.command : ''))
// A command ref names no actor where Brakeman's grammar names only the admin's receipt.
const wantedActor = computed(() => (typeof route.query.actor === 'string' && route.query.actor ? route.query.actor : 'admin'))
function selectTask(id: string, show = false) {
  const query: Record<string, string | undefined> = { ...(route.query as Record<string, string>), task: id || undefined }
  // Another task, or none: the attempt and step a link named were the last task's.
  if (id !== selectedTask.value) {
    query.attempt = undefined
    query.step = undefined
  }
  if (show) {
    query.view = 'tasks'
    view.value = 'tasks'
  }
  router.replace({ query })
}
// A decision's link (`&decision=dr-7`, the notification's URL: docs/protocol.md, Decisions) opens its card over the run.
const decisionModal = useDecisionModal()
watch(
  () => route.query.decision,
  (d) => {
    if (typeof d === 'string') decisionModal.open(d)
  },
  { immediate: true },
)
/** A task opened from the graph: its attempt's session when it has one, else the task in the Tasks view. */
function openTask(id: string) {
  const t = run.value?.tasks?.find((x) => x.id === id)
  if (t?.attempt?.sessionId) router.push(`/sessions/${t.attempt.sessionId}`)
  else selectTask(id, true)
}
const taskCommands = useTaskCommands(() => runId.value)

// Brakeman on this run (issue 993, F8): what it said about the run and its tasks, read once per run and again as its records move,
// beside the tasks in the Tasks view (the panel) and under the selected task's row (its answer). An ask from here (the panel's bar,
// a row's menu, the palette's row) is shown where it was asked: the palette's switches to the Tasks view, where the answer stands.
const bk = useBrakeman()
const brakemanAbout = useBrakemanAbout(() => `run:${runId.value}`)
const brakemanAsker = useAskBrakeman()
/** The first task that needs a person, for the panel's handoff chip when no task is selected. */
const needsTask = computed(() => (run.value?.tasks ?? []).find((t) => taskMarks.value.get(t.id)?.key === 'needs')?.id)
const brakemanChips = computed(() => askChips({ run: runId.value, selectedTask: selectedTask.value || undefined, needsTask: needsTask.value }))
function brakemanAsked(r: BrakemanRequest) {
  brakemanAbout.add(r)
}
/** Why the palette's Ask Brakeman… cannot ask, or "" when it can: the ask bar's words. */
const brakemanAskOff = computed(() => askDisabled(bk.state.value))
async function paletteAskBrakeman(text: string) {
  const note = text.trim()
  if (!note) return
  const r = await brakemanAsker.tell({ kind: 'ask', subjects: [`run:${runId.value}`], note })
  if (!r) return
  brakemanAsked(r)
  if (run.value?.managed) selectTask(selectedTask.value, true)
}
const states = computed(() => new Map((run.value?.members ?? []).map((m) => [m.name, memberStatus(run.value!, m, live.sessions.value)])))
// The members' states in the Crews pages' one vocabulary (memberLook) and the tasks' in the status language: the graph, its legend
// and the palette's rows draw these.
const memberMarks = computed(() => new Map((run.value?.members ?? []).map((m) => [m.name, memberLook(run.value!, m, live.sessions.value, now.value).view])))
const taskMarks = computed(() => new Map((run.value?.tasks ?? []).map((t) => [t.id, taskStatus(t, run.value!, attemptSession(t, live.sessions.value), now.value)])))

// The run's chat (design 2e): one thread for everyone on the run, over a quiet connection to a live member; a message may also
// be typed into a member, as the broadcast bar types, a member waiting on a prompt skipped. Counted while the drawer is closed.
const unread = useChatUnread()
const runChat = useRunChat(runId, computed(() => run.value?.members ?? []), { token: admin.token, kept: computed(() => run.value?.chat) })
const runChatOpen = ref(false)
const runChatUnread = computed(() => unread.count(runChat.key.value))
watch(
  [() => runChat.key.value, runChatOpen],
  ([key, open]) => {
    if (open) unread.openThread(key)
    else unread.closeThread(key)
  },
  { immediate: true },
)
onBeforeUnmount(() => unread.closeThread(runChat.key.value))
const runScope = computed(() => scopeItems(run.value?.members ?? [], states.value))
const runTargets = computed(() => runScope.value.slice(1))
const runChatDescription = computed(() => (run.value ? `${runTitle(run.value)} · run started ${startedClock(run.value.startedAt)} · kept with the run` : ''))
function runChatSendTo(ref: string, to: string) {
  if (!runChat.sendTo(ref, to)) toast.add({ title: 'Not connected', color: 'warning' })
}
/**
 * A member's question answered from the run's chat (useRunChatAnswer): the choice's keys into the running session of the member
 * the line is on, naming the question, with the workbench's own credential; a line no running member asked offers no button.
 */
const runChatAnswers = useRunChatAnswer({
  members: () => tiles.value.map((t) => ({ name: t.name, session: t.session && (t.session.status === 'running' || t.session.status === 'starting') ? t.session : undefined })),
  may: () => !run.value?.stoppedAt,
})
watch(runChat.refused, (r) => {
  if (r) toast.add({ title: 'Not sent to the member', description: skipWords(r.reason), color: 'warning' })
})
const handoffs = computed(() => handoffsOf(run.value?.log ?? [], events.entries.value, memberOf.value))
const selectedMember = ref('')
/** A node opened (Enter, a double click, Open): the member's session page, as a tile's select does. */
function openMember(name: string) {
  const t = tiles.value.find((x) => x.name === name)
  if (t?.session) router.push(`/sessions/${t.session.id}`)
}

function transportFor(s: SessionInfo) {
  return () => create({ sessionId: s.id, token: admin.token.value, kind: s.kind })
}

const starting = ref('')
async function startMember(name: string) {
  starting.value = name
  try {
    changed(await api.startRunMember(runId.value, name))
  } catch (e) {
    toast.add({ title: `${name} did not start`, description: (e as Error).message, icon: 'i-lucide-triangle-alert', color: 'error' })
  } finally {
    starting.value = ''
  }
}

let tick: number | undefined
onMounted(() => {
  live.start()
  load()
  // The clock of the header's "up 5 min" and the timeline's open bars: no read of the server.
  tick = window.setInterval(() => (now.value = Date.now()), 5000)
})
onBeforeUnmount(() => {
  window.clearInterval(tick)
})
watch(() => admin.token.value, () => {
  load()
  chip.ask(readRunTicket)
})

// This page's group in the command palette (#302): the header's buttons, the view, the chat, then the members as rows, the one
// asking first. The broadcast takes its line in the row and says who gets it and who is skipped, as the bar does.
const palette = usePalette()
const header = useTemplateRef<{ openAdd: () => void; share: () => void; stopNow: () => Promise<void>; resume: () => Promise<void>; togglePause: () => Promise<void>; isEnded: () => boolean }>('header')
const identity = useIdentity()
async function paletteBroadcast(text: string) {
  const line = text.trim()
  if (!line) return
  try {
    const byName = await broadcastByName(identity.name.value, api.whoami)
    const r = await api.broadcastRun(runId.value, { text: line, members: [...selection.value.selected], byName })
    toast.add({ ...broadcastSummary(r), icon: 'i-lucide-megaphone' })
  } catch (e) {
    toast.add({ title: 'Broadcast failed', description: (e as Error).message, color: 'error' })
  }
}
palette.registerPage(() => {
  const r = run.value
  if (!r) return null
  const stopped = !!r.stoppedAt
  const title = r.label || r.name
  const sel = selection.value
  const views: Array<[RunView, string, string]> = [
    ...(r.managed ? [['tasks', 'View the tasks', 'i-lucide-list-checks'] as [RunView, string, string]] : []),
    ['grid', 'View the members', 'i-lucide-users'],
    ['graph', 'View as a graph', 'i-lucide-git-fork'],
    ['timeline', 'View as a timeline', 'i-lucide-chart-gantt'],
  ]
  // A managed run's tasks, one row each: Enter shows the task; → offers its attempt's session and its commands (asked as the rows ask).
  const taskRows: PaletteItem[] = (r.tasks ?? []).map((t) => {
    const a = taskActions(t, r)
    const actions: Action[] = []
    if (a.open) actions.push({ id: 'open-attempt', kind: 'page', target: t.id, label: "Open the attempt's session", icon: 'i-lucide-terminal', run: () => router.push(`/sessions/${t.attempt!.sessionId}`) })
    // The attempt's preview (EN1): in a new tab (at the label's admin URL when it names its tree's secret), or framed on the preview
    // page, Open in Conductor, where it is captured.
    const preview = a.preview
    const n = t.attempt?.n ?? 0
    // The label is read ahead as the palette lists the row, so choosing it opens the tab at once.
    if (preview) previewOpen.prime({ run: r.id, task: t.id, attempt: n }, preview)
    if (preview) actions.push({ id: 'open-preview', kind: 'page', target: t.id, label: 'Open preview', icon: 'i-lucide-external-link', run: () => void previewOpen.newTab({ run: r.id, task: t.id, attempt: n }, preview) })
    if (a.inConductor) actions.push({ id: 'open-in-conductor', kind: 'page', target: t.id, label: 'Open in Conductor', icon: 'i-lucide-scan', run: () => void router.push(previewPath({ run: r.id, task: t.id, attempt: n })) })
    // A candidate's preview (PV1 PR-8): its checkout made and the preview page opened at its seq; while one is open, Open it.
    if (a.previewing) {
      const seq = a.previewing.seq
      actions.push({ id: 'open-candidate-preview', kind: 'page', target: t.id, label: "Open the candidate's preview", icon: 'i-lucide-scan', run: () => void router.push(previewPath({ run: r.id, task: t.id, attempt: n, seq })) })
    } else if (a.candidate)
      actions.push({
        id: 'preview-candidate',
        kind: 'page',
        target: t.id,
        label: 'Preview this candidate',
        icon: 'i-lucide-git-commit-horizontal',
        run: async () => {
          const opened = await candidatePreview.open(t.id, n)
          if (opened) await router.push(previewPath({ run: r.id, task: t.id, attempt: n, seq: opened.seq }))
          else toast.add({ title: 'Not previewed', description: candidatePreview.refused.value[`${t.id}:${n}`], color: 'neutral' })
        },
      })
    if (a.retry) actions.push({ id: 'retry', kind: 'page', target: t.id, label: 'Retry', icon: 'i-lucide-rotate-ccw', run: () => void taskCommands.send(t, 'retry') })
    if (a.accept) actions.push({ id: 'accept', kind: 'page', target: t.id, label: 'Accept anyway…', icon: 'i-lucide-check', ask: taskCommands.question(t, 'accept'), run: () => void taskCommands.send(t, 'accept') })
    if (a.cancel) actions.push({ id: 'cancel', kind: 'page', target: t.id, label: 'Cancel…', icon: 'i-lucide-x', color: 'error', ask: taskCommands.question(t, 'cancel'), run: () => void taskCommands.send(t, 'cancel') })
    return {
      key: `page:task:${t.id}`,
      label: t.id,
      detail: `${t.kind} · ${t.member} · ${taskWords(t, r).text}`,
      icon: t.kind === 'review' ? 'i-lucide-search-check' : 'i-lucide-hammer',
      state: markSection(taskMarks.value.get(t.id)?.key ?? 'waiting'),
      status: taskMarks.value.get(t.id),
      run: () => selectTask(t.id, true),
      actions,
    }
  })
  const members: PaletteItem[] = [...tiles.value]
    .sort((a, b) => Number(b.session?.attention?.state === 'needs_input') - Number(a.session?.attention?.state === 'needs_input'))
    .map((t) => {
      const s = t.session
      const m = t.member
      const pending = m?.status === 'pending'
      const ended = m?.status === 'ended' || (s && (s.status === 'exited' || s.status === 'stopped'))
      // The member's state in the Crews pages' words (memberLook), else, for a session the last read did not know yet, its session's.
      const mark = m ? memberMarks.value.get(m.name) : s ? sessionStatus(s, now.value) : undefined
      const state = mark ? markSection(mark.key) : ('idle' as const)
      // What the member's tile offers, and nothing else (memberOffer).
      const offer = memberOffer(ended ? 'ended' : m?.status, r)
      return {
        key: `page:member:${t.name}`,
        label: t.name,
        detail: s ? s.attention?.message || `${s.agentId} · ${s.status}` : pending ? `${m.start.when === 'manual' ? 'waiting for a hand' : 'waiting to start'}${offer === 'start' ? ' · Enter starts it' : ''}` : (m?.error ?? m?.status ?? ''),
        agentId: m?.agentId ?? s?.agentId,
        state,
        status: mark,
        session: s,
        prompt: s ? rowPrompt(sessionRow(s)) : null,
        run: () => {
          if (s && !ended) openMember(t.name)
          else if (offer === 'start') void startMember(t.name)
          else if (offer === 'resume') void api.resumeRunMember(runId.value, t.name).then((res) => router.push(`/sessions/${res.session.id}`)).catch((e) => toast.add({ title: 'It did not start', description: (e as Error).message, color: 'error' }))
          else if (s) openMember(t.name)
        },
        href: s ? `/sessions/${s.id}` : undefined,
      }
    })
  const rows: PaletteItem[] = [
      {
        key: 'page:broadcast',
        label: 'Broadcast…',
        detail: sel.sending.length ? `Send to ${sel.sending.length}${sel.waiting.length ? ` · skipped, waiting on a prompt: ${sel.waiting.join(', ')}` : ''}` : 'No member is running',
        icon: 'i-lucide-megaphone',
        disabled: stopped || !sel.sending.length,
        // Enter opens the field; the line is sent from it. Nothing runs without a line.
        run: () => {},
        fieldOnEnter: true,
        field: { placeholder: 'A line for every selected member…', button: `Send to ${sel.sending.length}`, submit: paletteBroadcast },
      },
      { key: 'page:add', label: 'Add an agent…', icon: 'i-lucide-user-plus', disabled: !runOffer(r).add, run: () => header.value?.openAdd() },
      ...views.map(([v, label, icon]) => ({ key: `page:view:${v}`, label, icon, on: view.value === v, run: () => (view.value = v) })),
      // The layouts and Activity are the Members view's: choosing one from another view shows the members too.
      ...RUN_LAYOUTS.map((l) => ({
        key: `page:layout:${l}`,
        label: `Lay the members out: ${RUN_LAYOUT_LOOK[l].label}`,
        icon: RUN_LAYOUT_LOOK[l].icon,
        on: tilesLayout.value === l,
        run: () => {
          tilesLayout.value = l
          view.value = 'grid'
        },
      })),
      tilesChoice.value.activity
        ? { key: 'page:activity', label: 'Hide activity', icon: 'i-lucide-activity', run: () => setActivity(false) }
        : {
            key: 'page:activity',
            label: 'Show activity',
            icon: 'i-lucide-activity',
            run: () => {
              setActivity(true)
              view.value = 'grid'
            },
          },
      { key: 'page:share', label: 'Share run', icon: 'i-lucide-share-2', kbds: ['S'], run: () => header.value?.share() },
      ...(r.managed && !stopped && !r.record && !header.value?.isEnded() ? [{ key: 'page:pause', label: r.paused ? 'Resume scheduling' : 'Pause scheduling', icon: r.paused ? 'i-lucide-play' : 'i-lucide-pause', run: () => header.value?.togglePause() }] : []),
      ...(bk.available.value === true
        ? [
            {
              key: 'page:ask-brakeman',
              label: 'Ask Brakeman…',
              detail: brakemanAskOff.value || `About ${title}: the answer shows beside the tasks`,
              icon: 'i-lucide-notebook-pen',
              disabled: !!brakemanAskOff.value,
              // Enter opens the field; the line is the question. Nothing is asked without one.
              run: () => {},
              fieldOnEnter: true,
              field: { placeholder: 'Ask about this run…', button: 'Ask', submit: paletteAskBrakeman },
            } satisfies PaletteItem,
          ]
        : []),
      { key: 'page:chat', label: `Open the run's chat${runChatUnread.value ? ` · ${runChatUnread.value} unread` : ''}`, icon: 'i-lucide-message-circle', run: () => (runChatOpen.value = true) },
      ...(stopped
        ? [{ key: 'page:resume', label: 'Resume as new run', icon: 'i-lucide-history', run: () => header.value?.resume() }]
        : header.value?.isEnded()
          ? []
          : [{ key: 'page:stop', label: 'Stop run…', icon: 'i-lucide-square', kbds: ['X'], disabled: !!r.record, ask: stopRunPageQuestion(title), run: () => header.value?.stopNow() }]),
      ...taskRows,
      ...members,
  ]
  return { label: title, rows }
})
</script>

<template>
  <UDashboardPanel id="run" :ui="{ body: 'p-0 sm:p-0 flex flex-col min-h-0 gap-0 overflow-hidden' }">
    <template #header>
      <CrewRunHeader ref="header" :record="!!run?.record" :run="run" :run-id="runId" :ticket="runTicket" :now="now" :chat-unread="runChatUnread" :chat-open="runChatOpen" :chat-offline="runChat.offline.value" @changed="changed" @chat="runChatOpen = true" />
    </template>

    <template #body>
      <!-- Padding around, not margins on, the full-width alerts: margins would overflow the body. -->
      <div v-if="error" class="flex-none px-3 pt-3">
        <UAlert color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="error" />
      </div>
      <div v-if="wantedCommand && !gone" class="flex-none px-3 pt-3">
        <RunCommandReceipt :run-id="runId" :command="wantedCommand" :actor="wantedActor" :now="now" />
      </div>
      <div v-if="launchLink" class="flex-none px-3 pt-3">
        <UAlert
          color="neutral"
          variant="subtle"
          icon="i-lucide-link"
          :title="`View link, valid ${Math.round((launchLink!.ttlSeconds / 3600) * 10) / 10} h. Shown once: copy it now.`"
          :actions="[{ label: 'Copy link', icon: 'i-lucide-clipboard', onClick: () => copy(launchLink!.url, 'Link copied', 'Anyone with it can watch every member.') }]"
          close
          :ui="{ description: 'font-mono text-xs break-all select-all' }"
          :description="launchLink!.url"
          data-launch-link
          @update:open="launchLink = null"
        />
      </div>

      <PageEmpty v-if="gone" state="missing" :words="`No run has the id ${runId}. The server forgets runs when it restarts.`" :actions="[{ label: 'Crews', icon: 'i-lucide-arrow-left', to: '/crews' }]" data-run-missing />

      <template v-else>
        <div class="flex flex-none items-center gap-2 px-3 pt-3">
          <UTabs v-model="view" :items="viewItems" :content="false" size="xs" color="neutral" data-run-view />
          <div v-if="shownView === 'grid'" class="ml-auto flex items-center gap-2">
            <RunLayoutSwitch v-if="!wallStacked" v-model="tilesLayout" />
            <UButton
              label="Activity"
              icon="i-lucide-activity"
              size="xs"
              color="neutral"
              :variant="tilesChoice.activity ? 'soft' : 'outline'"
              :aria-pressed="tilesChoice.activity"
              data-activity-toggle
              @click="setActivity(!tilesChoice.activity)"
            />
          </div>
        </div>
        <div v-if="shownView === 'graph'" class="flex min-h-0 flex-1 flex-col overflow-y-auto p-3" data-run-graph>
          <CrewGraph
            :members="run?.members ?? []"
            :states="states"
            :marks="memberMarks"
            :task-marks="taskMarks"
            :handoffs="handoffs"
            :run-id="runId"
            :stopped="!!run?.stoppedAt"
            :run="run"
            :selected="selectedMember"
            :now="now"
            :tasks="run?.tasks ?? []"
            :paused="!!run?.paused"
            :run-state="run?.state"
            :paused-why="run?.pausedWhy"
            :selected-task="selectedTask"
            fill
            @select="selectedMember = $event"
            @open="openMember"
            @start="startMember"
            @select-task="selectTask($event)"
            @open-task="openTask"
            @show-task="selectTask($event, true)"
          />
        </div>
        <!-- The tasks, with Brakeman on this run beside them; on a narrow window the panel wraps under the tasks. -->
        <div v-else-if="shownView === 'tasks' && run" class="flex min-h-0 flex-1 flex-wrap content-start items-start gap-3 overflow-y-auto p-3" data-run-tasks-view>
          <RunTasks class="min-w-0 [flex:3_1_40rem]" :run="run" :ticket="reviewTicket" :selected="selectedTask" :attempt="wantedAttempt" :step="wantedStep" :now="now" :answers="brakemanAbout.byTask.value" @select="selectTask($event, true)" @open="router.push(`/sessions/${$event}`)" @asked="brakemanAsked" />
          <BrakemanSubjectPanel class="[flex:1_1_300px]" :subject="`run:${run.id}`" :label="runTitle(run)" :chips="brakemanChips" :about="brakemanAbout" @asked="brakemanAsked" />
        </div>
        <div v-else-if="shownView === 'timeline' && run" class="min-h-0 flex-1 overflow-y-auto p-3" data-run-timeline-view>
          <RunTimeline :run="run" :sessions="sessions" :feed="events.entries.value" :now="now" />
        </div>
        <RunWall
          v-else
          :tiles="tiles"
          :run="run"
          :run-id="runId"
          :sessions="live.sessions.value"
          :feed="feed"
          :now="now"
          :starting="starting"
          :create-transport="transportFor"
          :answerable="runChatAnswers.answerable"
          :answer="runChatAnswers.answer"
          v-model:narrow="wallStacked"
          :activity="tilesChoice.activity"
          :layout="tilesLayout"
          :large="largeMember"
          @show-large="chooseTiles({ large: $event })"
          @decision="decisionModal.open($event)"
          @open="router.push(`/sessions/${$event}`)"
          @start="startMember"
        >
          <template #leading="{ tile: t }">
            <UCheckbox :model-value="isSelected(t.name)" :aria-label="`Select ${t.name} for the broadcast`" data-broadcast-pick @update:model-value="toggle(t.name, $event)" />
          </template>
        </RunWall>
        <div class="flex-none px-3 pb-3">
          <BroadcastBar :run-id="runId" :members="selection.selected" :will-type="selection.sending.length" :waiting="selection.waiting.length" :disabled="!run || !!run.stoppedAt" />
        </div>
      </template>
    </template>
  </UDashboardPanel>

  <ChatSheet
    v-model:open="runChatOpen"
    scope="run"
    title="Run chat"
    :description="runChatDescription"
    :thread="runChat.thread.value"
    role="control"
    :ended="!!run?.stoppedAt"
    :offline="runChat.offline.value"
    :viewers="runChat.people.value"
    :scope-items="runScope"
    :send-targets="runTargets"
    @send="(text, to) => runChat.send(text, to)"
    @send-to="runChatSendTo"
    @retry="runChat.retry"
    :answerable="runChatAnswers.answerable"
    @answer="runChatAnswers.answer"
  />
</template>
