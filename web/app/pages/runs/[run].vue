<script setup lang="ts">
import type { RunInfo, RunMember, SessionInfo } from '~/composables/useSessions'
import type { ChatMessage } from '~/utils/protocol'
import { scopeItems } from '~/utils/chat'
import { broadcastByName, broadcastSelection, broadcastSummary, crewFeed, memberStatus, runCounts, skipWords, takeViewLink } from '~/utils/crews'
import { stopQuestion, type Action } from '~/utils/actions'
import { taskActions, taskWords } from '~/utils/tasks'
import { rowPrompt } from '~/utils/sidebarActions'
import { sessionRow } from '~/utils/sidebar'
import type { PaletteItem } from '~/composables/usePalette'
import { memberWords, runTitle, startedClock } from '~/utils/crewWords'
import { handoffsOf } from '~/utils/crewGraph'
import { bestGrid, lastItemSpan } from '~/utils/wall'
import { replyFailure } from '~/utils/reply'
import type { RelatedTicket } from '~/composables/useDispatch'
import { runLinkWatch, runTicketRead } from '~/composables/useDispatchLive'

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
async function readRunTicket(): Promise<void> {
  if (!admin.hasToken.value) return
  const id = runId.value
  await runTicketRead(dispatch.gate, id, () => dispatch.api.runTicket(id), (t) => {
    if (id === runId.value) runTicket.value = t
  })
}
const chip = useDispatchLive({ ...runLinkWatch(readRunTicket), reset: () => dispatch.gate.reset() }, () => chip.ask(readRunTicket))

/** An action (add, start, stop) answered with the run as it is now. */
function changed(r: RunInfo) {
  live.applyRun(r)
}

/** The run's sessions in the live store. */
const sessions = computed(() => live.sessions.value.filter((s) => s.crew?.runId === runId.value))

interface Tile {
  name: string
  member?: RunMember
  session?: SessionInfo
}

/**
 * One tile per member in the run's order, then any session of the run the last read did not know yet. A managed run's member has one
 * session per attempt over time: its tile is its latest, and the earlier attempts' sessions get no tile of their own.
 */
const tiles = computed<Tile[]>(() => {
  const out: Tile[] = []
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

const counts = computed(() => (run.value ? runCounts(run.value, live.sessions.value) : { needs: 0, running: 0 }))

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
watch(view, (v) => {
  try {
    localStorage.setItem(VIEW_KEY, v)
  } catch {
    /* storage refused: the choice lasts the page */
  }
})
const viewItems = computed(() => [
  ...(run.value?.managed ? [{ label: 'Tasks', value: 'tasks', icon: 'i-lucide-list-checks' }] : []),
  { label: 'Grid', value: 'grid', icon: 'i-lucide-layout-grid' },
  { label: 'Graph', value: 'graph', icon: 'i-lucide-git-fork' },
  { label: 'Timeline', value: 'timeline', icon: 'i-lucide-chart-gantt' },
])
/** The view shown: Tasks is a managed run's alone, so a legacy run asked for it gets the grid. */
const shownView = computed(() => (view.value === 'tasks' && !run.value?.managed ? 'grid' : view.value))

// The task selected in the Tasks view (`?task=`), in the URL so a link opens on it (#334, row 7).
const selectedTask = computed(() => (typeof route.query.task === 'string' ? route.query.task : ''))
function selectTask(id: string, show = false) {
  const query: Record<string, string | undefined> = { ...(route.query as Record<string, string>), task: id || undefined }
  if (show) {
    query.view = 'tasks'
    view.value = 'tasks'
  }
  router.replace({ query })
}
/** A task opened from the graph: its attempt's session when it has one, else the task in the Tasks view. */
function openTask(id: string) {
  const t = run.value?.tasks?.find((x) => x.id === id)
  if (t?.attempt?.sessionId) router.push(`/sessions/${t.attempt.sessionId}`)
  else selectTask(id, true)
}
const taskCommands = useTaskCommands(() => runId.value)
const states = computed(() => new Map((run.value?.members ?? []).map((m) => [m.name, memberStatus(run.value!, m, live.sessions.value)])))

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
/** A member's question answered from the run's chat: the choice's keys into that member, as the Yard's quick reply types. */
const quick = useQuickReply()
function runChatAnswer(m: ChatMessage, index: number) {
  const o = m.options?.[index]
  const t = tiles.value.find((x) => x.name === m.on)
  if (!o || !t?.session) return
  // The question's line in the chat is the question's own id: a choice clicked once it was answered types nothing.
  quick.send(t.session, o.input, { question: m.id }).catch((e: unknown) => toast.add(replyFailure(e, t.name)))
}
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

function footer(t: Tile): { where: string; diff: string } {
  const m = t.member
  const branch = m?.branch || t.session?.branch || (run.value?.isolation === 'worktree' ? '' : 'shared cwd')
  // A managed run's member is on a task: `build #2 · crew/<run>/build.2`.
  const where = m?.task ? `${m.task} #${m.attempt}${branch ? ` · ${branch}` : ''}` : branch
  const diff = m?.diff ? `+${m.diff.added} −${m.diff.removed}` : ''
  return { where, diff }
}

/** A tile without a session: what its member does, in the run's words (a member a stop cut off is grey, red only for its own error). */
function placeholder(t: Tile): { text: string; tone: 'warning' | 'error' | 'muted' | 'default' } {
  const m = t.member
  if (!m || !run.value) return { text: '', tone: 'muted' }
  const st = memberStatus(run.value, m, live.sessions.value)
  // Started, and its session not in the live store yet.
  return st === 'pending' || st === 'ended' ? memberWords(run.value, m, st) : { text: 'starting…', tone: 'muted' }
}
const tone = { warning: 'text-warning', error: 'text-error', muted: 'text-muted', default: 'text-default' }

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

// Grid: every tile and the activity card fit on screen, as on the wall; on
// a narrow screen they stack and the page scrolls.
const grid = useTemplateRef<HTMLDivElement>('grid')
const box = ref({ w: 0, h: 0 })
watch(
  grid,
  (el, _prev, onCleanup) => {
    if (!el) return
    const observer = new ResizeObserver((entries) => {
      const r = entries[0]?.contentRect
      if (r) box.value = { w: r.width, h: r.height }
    })
    observer.observe(el)
    onCleanup(() => observer.disconnect())
  },
  { immediate: true },
)
const narrow = computed(() => box.value.w > 0 && box.value.w < 640)
const layout = computed(() => bestGrid(tiles.value.length + 1, box.value.w, box.value.h, 12, 1.4))
const feedSpan = computed(() => lastItemSpan(tiles.value.length + 1, layout.value))
const gridStyle = computed(() =>
  narrow.value
    ? { gridTemplateColumns: 'minmax(0, 1fr)', gridAutoRows: '16rem' }
    : { gridTemplateColumns: `repeat(${layout.value.cols}, minmax(0, 1fr))`, gridTemplateRows: `repeat(${layout.value.rows}, minmax(0, 1fr))` },
)

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
const header = useTemplateRef<{ openAdd: () => void; share: () => void; stopNow: () => Promise<void>; resume: () => Promise<void>; togglePause: () => Promise<void> }>('header')
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
    ['grid', 'View as a grid', 'i-lucide-layout-grid'],
    ['graph', 'View as a graph', 'i-lucide-git-fork'],
    ['timeline', 'View as a timeline', 'i-lucide-chart-gantt'],
  ]
  // A managed run's tasks, one row each: Enter shows the task; → offers its attempt's session and its commands (asked as the rows ask).
  const taskRows: PaletteItem[] = (r.tasks ?? []).map((t) => {
    const a = taskActions(t, r)
    const actions: Action[] = []
    if (a.open) actions.push({ id: 'open-attempt', kind: 'page', target: t.id, label: "Open the attempt's session", icon: 'i-lucide-terminal', run: () => router.push(`/sessions/${t.attempt!.sessionId}`) })
    if (a.retry) actions.push({ id: 'retry', kind: 'page', target: t.id, label: 'Retry', icon: 'i-lucide-rotate-ccw', run: () => void taskCommands.send(t, 'retry') })
    if (a.accept) actions.push({ id: 'accept', kind: 'page', target: t.id, label: 'Accept anyway…', icon: 'i-lucide-check', ask: taskCommands.question(t, 'accept'), run: () => void taskCommands.send(t, 'accept') })
    if (a.cancel) actions.push({ id: 'cancel', kind: 'page', target: t.id, label: 'Cancel…', icon: 'i-lucide-x', color: 'error', ask: taskCommands.question(t, 'cancel'), run: () => void taskCommands.send(t, 'cancel') })
    return {
      key: `page:task:${t.id}`,
      label: t.id,
      detail: `${t.kind} · ${t.member} · ${taskWords(t, r).text}`,
      icon: t.kind === 'review' ? 'i-lucide-search-check' : 'i-lucide-hammer',
      state: t.state === 'running' || t.state === 'verifying' ? ('running' as const) : t.state === 'failed' ? ('needs' as const) : t.state === 'succeeded' || t.state === 'cancelled' ? ('exited' as const) : ('idle' as const),
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
      const state = s?.attention?.state === 'needs_input' ? ('needs' as const) : ended ? ('exited' as const) : s ? ('running' as const) : ('idle' as const)
      return {
        key: `page:member:${t.name}`,
        label: t.name,
        detail: s ? s.attention?.message || `${s.agentId} · ${s.status}` : pending ? (m.start.when === 'manual' ? 'waiting for a hand · Enter starts it' : 'waiting to start') : (m?.error ?? m?.status ?? ''),
        agentId: m?.agentId ?? s?.agentId,
        state,
        session: s,
        prompt: s ? rowPrompt(sessionRow(s)) : null,
        run: () => {
          if (s && !ended) openMember(t.name)
          else if (pending && m.start.when === 'manual' && !stopped) void startMember(t.name)
          else if (ended && !stopped) void api.resumeRunMember(runId.value, t.name).then((res) => router.push(`/sessions/${res.session.id}`)).catch((e) => toast.add({ title: 'It did not start', description: (e as Error).message, color: 'error' }))
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
      { key: 'page:add', label: 'Add an agent…', icon: 'i-lucide-user-plus', disabled: stopped || !!r.record, run: () => header.value?.openAdd() },
      ...views.map(([v, label, icon]) => ({ key: `page:view:${v}`, label, icon, on: view.value === v, run: () => (view.value = v) })),
      { key: 'page:share', label: 'Share run', icon: 'i-lucide-share-2', kbds: ['S'], run: () => header.value?.share() },
      ...(r.managed && !stopped && !r.record ? [{ key: 'page:pause', label: r.paused ? 'Resume scheduling' : 'Pause scheduling', icon: r.paused ? 'i-lucide-play' : 'i-lucide-pause', run: () => header.value?.togglePause() }] : []),
      { key: 'page:chat', label: `Open the run's chat${runChatUnread.value ? ` · ${runChatUnread.value} unread` : ''}`, icon: 'i-lucide-message-circle', run: () => (runChatOpen.value = true) },
      ...(stopped
        ? [{ key: 'page:resume', label: 'Resume as new run', icon: 'i-lucide-history', run: () => header.value?.resume() }]
        : [{ key: 'page:stop', label: 'Stop run…', icon: 'i-lucide-square', kbds: ['X'], disabled: !!r.record, ask: stopQuestion({ kind: 'run', name: title }), run: () => header.value?.stopNow() }]),
      ...taskRows,
      ...members,
  ]
  return { label: title, rows }
})
</script>

<template>
  <UDashboardPanel id="run" :ui="{ body: 'p-0 sm:p-0 flex flex-col min-h-0 gap-0 overflow-hidden' }">
    <template #header>
      <CrewRunHeader ref="header" :record="!!run?.record" :run="run" :run-id="runId" :ticket="runTicket" :counts="counts" :now="now" :chat-unread="runChatUnread" :chat-open="runChatOpen" :chat-offline="runChat.offline.value" @changed="changed" @chat="runChatOpen = true" />
    </template>

    <template #body>
      <!-- Padding around, not margins on, the full-width alerts: margins would overflow the body. -->
      <div v-if="error" class="flex-none px-3 pt-3">
        <UAlert color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="error" />
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

      <div v-if="gone" class="flex flex-1 flex-col items-center justify-center gap-3 p-8 text-muted">
        <UIcon name="i-lucide-search-x" class="size-8" />
        <p class="text-sm">No run has the id <code>{{ runId }}</code>. The server forgets runs when it restarts.</p>
        <UButton label="Crews" icon="i-lucide-arrow-left" color="neutral" variant="soft" to="/crews" />
      </div>

      <template v-else>
        <div class="flex flex-none items-center px-3 pt-3">
          <UTabs v-model="view" :items="viewItems" :content="false" size="xs" color="neutral" data-run-view />
        </div>
        <div v-if="shownView === 'graph'" class="flex min-h-0 flex-1 flex-col overflow-y-auto p-3" data-run-graph>
          <CrewGraph
            :members="run?.members ?? []"
            :states="states"
            :handoffs="handoffs"
            :run-id="runId"
            :stopped="!!run?.stoppedAt"
            :selected="selectedMember"
            :now="now"
            :tasks="run?.tasks ?? []"
            :paused="!!run?.paused"
            :selected-task="selectedTask"
            fill
            @select="selectedMember = $event"
            @open="openMember"
            @start="startMember"
            @select-task="selectTask($event)"
            @open-task="openTask"
          />
        </div>
        <div v-else-if="shownView === 'tasks' && run" class="min-h-0 flex-1 overflow-y-auto p-3" data-run-tasks-view>
          <RunTasks :run="run" :selected="selectedTask" :now="now" @select="selectTask($event, true)" @open="router.push(`/sessions/${$event}`)" />
        </div>
        <div v-else-if="shownView === 'timeline' && run" class="min-h-0 flex-1 overflow-y-auto p-3" data-run-timeline-view>
          <RunTimeline :run="run" :sessions="sessions" :feed="events.entries.value" :now="now" />
        </div>
        <div v-else ref="grid" class="min-h-0 flex-1 p-3" :class="narrow ? 'overflow-y-auto' : 'overflow-hidden'">
          <div class="grid h-full w-full gap-3" :class="narrow && 'h-auto'" :style="gridStyle" data-run-grid>
            <template v-for="t in tiles" :key="t.session?.id ?? `m-${t.name}`">
              <SessionTile v-if="t.session" :session="t.session" :create-transport="transportFor(t.session)" :data-member="t.name" @select="router.push(`/sessions/${t.session.id}`)">
                <template #leading>
                  <UCheckbox :model-value="isSelected(t.name)" :aria-label="`Select ${t.name} for the broadcast`" data-broadcast-pick @update:model-value="toggle(t.name, $event)" />
                </template>
                <template #footer>
                  <span class="truncate" data-branch>{{ footer(t).where }}</span>
                  <span class="ml-auto flex-none" data-diff>{{ footer(t).diff }}</span>
                </template>
              </SessionTile>
              <div v-else class="flex min-h-0 flex-col overflow-hidden rounded-lg border border-dashed border-accented" :data-member="t.name">
                <div class="flex items-center gap-2 border-b border-default px-2.5 py-1.5 text-xs">
                  <SessionAvatar :agent-id="t.member?.agentId ?? ''" dashed />
                  <span class="flex-1 truncate text-[13px] font-semibold">{{ t.name }}</span>
                </div>
                <div class="flex flex-1 flex-col items-center justify-center gap-2 p-3 text-center text-sm text-muted">
                  <span :class="tone[placeholder(t).tone]" data-member-words>{{ placeholder(t).text }}</span>
                  <UButton
                    v-if="t.member?.status === 'pending' && !run?.stoppedAt"
                    label="Start now"
                    icon="i-lucide-play"
                    size="xs"
                    color="neutral"
                    variant="outline"
                    :loading="starting === t.name"
                    @click="startMember(t.name)"
                  />
                  <ResumeButton v-else-if="t.member?.status === 'ended'" :run-id="runId" :member="t.name" stay size="xs" />
                </div>
              </div>
            </template>
            <CrewFeed :items="feed" :style="narrow ? undefined : { gridColumn: `span ${feedSpan} / span ${feedSpan}` }" />
          </div>
        </div>
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
    @answer="runChatAnswer"
  />
</template>
