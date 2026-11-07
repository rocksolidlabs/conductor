<script setup lang="ts">
import { emptyTabs, toggleFold } from '~/utils/editorTabs'
import { editorRows } from '~/utils/editorRows'
import type { EditorColumnHandle } from '~/components/EditorColumn.vue'
import type { SessionInfo } from '~/composables/useSessions'
import type { TransportKind, ActivityEntry, Welcome } from '~/utils/protocol'
import type { TransportState } from '~/utils/transport/types'
import { WALL_SHORTCUTS } from '~/composables/useShortcuts'
import { isActive } from '~/utils/attention'
import { bestGrid } from '~/utils/wall'
import { replyFailure } from '~/utils/reply'

useHead({ title: 'Yard' })

const route = useRoute()
const router = useRouter()
const attention = useAttention()
const admin = useWorkbenchToken()
const api = useSessions()
const toast = useToast()
const quick = useQuickReply()
// The question bar's Answer on a durable decision's question opens its card (Program item DR1).
const decisionModal = useDecisionModal()
const { create } = useTerminalTransport()
const { httpBase } = useApiBase()
useShortcutsModal().registerPage(WALL_SHORTCUTS)
usePageAltCodes().register(['KeyQ'])

type Filter = 'all' | 'needs' | 'running'
function isFilter(v: unknown): v is Filter {
  return v === 'all' || v === 'needs' || v === 'running'
}
// ?filter= (#303) names the chip a link opens on, read as the page opens and as the query changes.
const filter = ref<Filter>(isFilter(route.query.filter) ? route.query.filter : 'all')
watch(
  () => route.query.filter,
  (q) => {
    if (isFilter(q)) filter.value = q
  },
)
const active = computed(() => attention.sessions.value.filter(isActive))
const waiting = computed(() => attention.needsInput.value.filter(isActive))
const running = computed(() => active.value.filter((s) => s.attention?.state !== 'needs_input'))
const tiles = computed(() => (filter.value === 'needs' ? waiting.value : filter.value === 'running' ? running.value : active.value))
const answered = computed(() =>
  attention.sessions.value
    .filter((s) => s.lastAnswer)
    .sort((a, b) => (b.lastAnswer?.at ?? '').localeCompare(a.lastAnswer?.at ?? ''))
    .slice(0, 8),
)
// The queue shows only while something needs input (#360); the answered rows ride along inside it then.
const showQueue = computed(() => waiting.value.length > 0)

// Queue keyboard selection (J / K, Enter to type a reply).
const queueSel = ref(0)
const queue = useTemplateRef<{ focusSelected: () => void }>('queue')
watch(waiting, (list) => {
  queueSel.value = Math.min(queueSel.value, Math.max(0, list.length - 1))
})

// Focus mode: /yard?focus=<id> expands one session in place. The grid is one
// Esc, one click or one browser Back away; the URL stays shareable.
const focusId = computed(() => (typeof route.query.focus === 'string' ? route.query.focus : ''))
const focused = computed<SessionInfo | undefined>(() => attention.sessions.value.find((s) => s.id === focusId.value))
const focusTerminal = ref<{ focus: () => void; requestFile: (p: string, s?: boolean, x?: FileGetExtra) => Promise<any> } | null>(null)
const viewers = ref(0)
const transport = ref<{ kind: TransportKind; state: TransportState; rtt: number | null }>({ kind: 'ws', state: 'idle', rtt: null })
// The editor area (design 4g): files open above the focused tile's terminal, the Files pane beside it.
const { tabs, editorOpen, openFile, previewFile, openUrl, openDiff, openCommitDiff } = useEditorTabs()
watch(focusId, () => {
  viewers.value = 0
  transport.value = { kind: 'ws', state: 'idle', rtt: null }
  tabs.value = emptyTabs()
})

function focusSession(s: SessionInfo) {
  router.push({ path: '/yard', query: { focus: s.id } })
}

function backToGrid() {
  router.push({ path: '/yard' })
}

function leaveFocus() {
  // Esc first folds the editor; only a second Esc leaves focus mode.
  if (!focusId.value) return
  if (editorOpen.value) {
    tabs.value = toggleFold(tabs.value)
    return
  }
  backToGrid()
}
function queueNext() {
  if (waiting.value.length) queueSel.value = (queueSel.value + 1) % waiting.value.length
}
function queuePrev() {
  if (waiting.value.length) queueSel.value = (queueSel.value - 1 + waiting.value.length) % waiting.value.length
}
const inTerminal = { usingInput: true }
defineShortcuts({
  escape: leaveFocus,
  j: queueNext,
  k: queuePrev,
  enter: () => {
    if (!focusId.value && waiting.value.length) queue.value?.focusSelected()
  },
  alt_escape: { ...inTerminal, handler: leaveFocus },
  // Alt+Q leaves too (#312): Windows takes Alt+Esc for its own window cycling.
  alt_q: { ...inTerminal, handler: leaveFocus },
  alt_j: { ...inTerminal, handler: queueNext },
  alt_k: { ...inTerminal, handler: queuePrev },
})

// Grid: every active session fits on screen; tiles shrink as sessions are added.
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
const layout = computed(() => bestGrid(tiles.value.length, box.value.w, box.value.h, 12, 1.6))
const gridStyle = computed(() => ({
  gridTemplateColumns: `repeat(${layout.value.cols}, minmax(0, 1fr))`,
  gridTemplateRows: `repeat(${layout.value.rows}, minmax(0, 1fr))`,
}))

function transportFor(s: SessionInfo) {
  return () => create({ sessionId: s.id, token: admin.token.value, kind: s.kind })
}

function fail(title: string) {
  return (e: unknown) => toast.add({ title, description: (e as Error).message, color: 'error' })
}

/** A reply names the question the card or the bar showed (its id): one answered meanwhile takes nothing, and says so. */
function reply(s: SessionInfo, text: string, question?: string) {
  quick.reply(s, text, { question }).catch((e: unknown) => toast.add(replyFailure(e, s.name)))
}

function option(s: SessionInfo, index: number, question?: string) {
  const o = s.attention?.options?.[index]
  if (!o) return
  quick.send(s, o.input, { question }).catch((e: unknown) => toast.add(replyFailure(e, s.name)))
}

async function stop(s: SessionInfo) {
  try {
    await api.stop(s.id)
    toast.add({ title: 'Stop requested', description: s.name, color: 'neutral' })
  } catch (e) {
    fail('Stop failed')(e)
  }
}

// The focused header's actions on a phone, folded into a menu (round 14).
const focusMenu = computed(() => {
  const s = focused.value
  if (!s) return []
  const items: Array<{ label: string; icon: string; to?: string; color?: 'error'; onSelect?: () => void }> = [{ label: 'Open page', icon: 'i-lucide-square-terminal', to: `/sessions/${s.id}` }]
  if (isActive(s)) items.push({ label: 'Stop', icon: 'i-lucide-square', color: 'error', onSelect: () => void stop(s) })
  return items
})

// The editor on the focused tile (design round 12, F6 and F8): saves and Neovim as on the session page.
const editor = useEditorBridge(focusTerminal as unknown as Ref<EditorTerminal | null>, () => (focused.value?.kind === 'hosted' ? focused.value.hostName || 'the host' : 'this server'))
// The focused tile's activity, for the Files pane's Touched section (design 4e): what its connection replays and reports.
const focusActivity = ref<ActivityEntry[]>([])
function onFocusWelcome(w: Welcome) {
  editor.onWelcome(w)
  focusActivity.value = []
}
function onFocusActivity(e: ActivityEntry) {
  focusActivity.value = [...focusActivity.value.slice(-199), e]
}
watch(
  () => focused.value?.id,
  () => {
    editor.reset()
    focusActivity.value = []
  },
)

function requestFile(path: string, stat?: boolean, extra?: FileGetExtra) {
  if (!focusTerminal.value) return Promise.reject(new Error('terminal not ready'))
  return focusTerminal.value.requestFile(path, stat, extra)
}

function rawUrl(path: string) {
  if (focused.value?.kind !== 'server') return null
  return `${httpBase.value}/api/sessions/${encodeURIComponent(focused.value.id)}/files?path=${encodeURIComponent(path)}&raw=1&token=${encodeURIComponent(admin.token.value)}`
}

onMounted(() => {
  if (!admin.hasToken.value) admin.needsToken.value = true
  attention.start()
})

// The focused tile's editor column: its rows in the palette (#321) read its state through this.
const columnRef = useTemplateRef<EditorColumnHandle>('columnRef')

// This page's group in the command palette (#302): the chips, the queue's selected session expanded, the grid; the focused
// tile's editor rows (#321).
usePalette().registerPage(() => {
  const chips: Array<[Filter, string, number]> = [
    ['all', 'All', active.value.length],
    ['needs', 'Needs you', waiting.value.length],
    ['running', 'Running', running.value.length],
  ]
  const sel = waiting.value[queueSel.value]
  return {
    label: focused.value ? focused.value.name : 'Yard',
    sessionId: focused.value?.id,
    cwd: focused.value?.cwd,
    openFile: focused.value ? openFile : undefined,
    rows: [
      ...chips.map(([f, label, n]) => ({ key: `page:filter:${f}`, label: `${label} · ${n}`, icon: 'i-lucide-filter', on: filter.value === f, run: () => (filter.value = f) })),
      ...(sel && !focused.value ? [{ key: 'page:expand', label: `Expand ${sel.name}`, detail: 'The queue’s selected session, in place', icon: 'i-lucide-maximize-2', agentId: sel.agentId, run: () => focusSession(sel) }] : []),
      ...(focused.value ? [{ key: 'page:grid', label: 'Back to the grid', icon: 'i-lucide-layout-grid', kbds: ['escape'], run: backToGrid }] : []),
      ...(focused.value ? editorRows(columnRef.value?.palette, { setKeymap: (k) => columnRef.value?.setKeymap(k), save: () => columnRef.value?.save(), close: (id) => columnRef.value?.requestClose(id), askAgent: () => columnRef.value?.askAgent() }) : []),
    ],
  }
})
</script>

<template>
  <UDashboardPanel id="wall" :ui="{ body: 'p-0 sm:p-0 flex flex-col min-h-0 gap-0 overflow-hidden' }">
    <template #header>
      <!-- At phone width the focused header keeps its title: the transport and viewer badges hide and Open page and Stop fold into a
           menu, as the session page's header does (round 14: everything stayed at every width and ran over the title at 390). -->
      <UDashboardNavbar :toggle="false" :title="focusId ? focused?.name || 'Session' : 'Yard'" :ui="{ root: 'h-14', title: 'min-w-0 truncate' }" data-yard-header>
        <template #leading>
          <UTooltip v-if="focusId" text="Back to the grid" :kbds="['escape']">
            <UButton icon="i-lucide-arrow-left" color="neutral" variant="ghost" aria-label="Back to the grid" @click="backToGrid" />
          </UTooltip>
        </template>
        <template #trailing>
          <div class="flex items-center gap-2 ml-2">
            <template v-if="focusId">
              <SessionStatusBadge v-if="focused" :status="focused.status" :exit-code="focused.exitCode" />
              <AttentionBadge :attention="focused?.attention" />
              <TransportBadge :kind="transport.kind" :state="transport.state" :rtt="transport.rtt" class="hidden sm:inline-flex" />
              <UBadge :label="`${viewers} here`" icon="i-lucide-users" color="neutral" variant="subtle" size="sm" class="hidden sm:inline-flex" />
            </template>
            <div v-else class="flex items-center gap-1.5 text-xs">
              <UButton :label="`All ${active.length}`" size="xs" :variant="filter === 'all' ? 'solid' : 'outline'" color="neutral" data-yard-filter="all" :data-active="filter === 'all' ? '' : undefined" @click="filter = 'all'" />
              <UButton :label="`Needs you ${waiting.length}`" size="xs" :variant="filter === 'needs' ? 'solid' : 'outline'" color="warning" data-yard-filter="needs" :data-active="filter === 'needs' ? '' : undefined" @click="filter = 'needs'" />
              <UButton :label="`Running ${running.length}`" size="xs" :variant="filter === 'running' ? 'solid' : 'outline'" color="neutral" data-yard-filter="running" :data-active="filter === 'running' ? '' : undefined" @click="filter = 'running'" />
              <UBadge v-if="!attention.connected.value" label="polling" color="warning" variant="subtle" size="sm" />
              <AttentionBar :sessions="attention.sessions.value" />
            </div>
          </div>
        </template>
        <template #right>
          <template v-if="focusId && focused">
            <div class="hidden items-center gap-2 sm:flex" data-yard-focus-actions>
              <UButton label="Open page" icon="i-lucide-square-terminal" color="neutral" variant="soft" :to="`/sessions/${focused.id}`" />
              <UButton v-if="isActive(focused)" label="Stop" icon="i-lucide-square" color="error" variant="soft" @click="stop(focused)" />
              <ResumeButton v-else-if="focused.kind === 'server'" :session="focused" />
            </div>
            <ResumeButton v-if="!isActive(focused) && focused.kind === 'server'" :session="focused" icon-only class="sm:hidden" />
            <UDropdownMenu :items="focusMenu" :content="{ align: 'end' }" class="sm:hidden">
              <UButton icon="i-lucide-ellipsis-vertical" color="neutral" variant="ghost" aria-label="Session actions" data-yard-focus-menu />
            </UDropdownMenu>
          </template>
          <FullscreenButton />
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <UAlert v-if="attention.error.value" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="attention.error.value" class="m-3" />

      <!-- Scrolls when the terminal's least height and the reply bar's question (#187: whole, up to 500 bytes) outgrow a short
           viewport, a landscape phone's, so that the choices stay reachable; the session page's body scrolls the same way. -->
      <div v-if="focusId" class="flex flex-1 min-h-0 gap-3 overflow-y-auto p-2 sm:p-3" data-yard-focus>
        <EditorColumn v-if="focused" ref="columnRef" v-model:tabs="tabs" :request="requestFile" :nvim="editor.nvim.value" :write="editor.writeFile" :can-edit="editor.canEdit.value" :cwd="focused.cwd" :raw-url="rawUrl" :host-away="focused.status === 'host_disconnected' ? focused.hostName || 'The host' : undefined" :bar-text="focused.attention?.message || focused.agentId">
          <TerminalView
            :key="focused.id"
            ref="focusTerminal"
            :create-transport="transportFor(focused)"
            @welcome="onFocusWelcome"
            @activity="onFocusActivity"
            @nvim="editor.onNvim"
            @closed="editor.onClosed()"
            @viewers="viewers = $event.count"
            @transport="transport = $event"
            @open-file="openFile"
            @open-url="openUrl"
          />
          <template #bar>
            <QuickReplyBar :attention="focused.attention" :agent-name="focused.agentId" role="control" :busy="quick.sending.value.has(focused.id)" answers @answer="decisionModal.open" @reply="(text, q) => reply(focused!, text, q)" @option="(i, q) => option(focused!, i, q)" @open-plan="openFile({ path: $event })" @preview-plan="previewFile({ path: $event })" />
          </template>
        </EditorColumn>
        <div v-else class="flex flex-1 flex-col items-center justify-center gap-3 text-muted">
          <UIcon name="i-lucide-search-x" class="size-8" />
          <p class="text-sm">That session is gone.</p>
          <UButton label="Back to the grid" icon="i-lucide-arrow-left" color="neutral" variant="soft" @click="backToGrid" />
        </div>
        <aside v-if="focused" class="hidden md:flex w-[332px] flex-none flex-col overflow-hidden rounded-md border border-default bg-default" data-files-aside>
          <div class="flex h-9 flex-none items-center gap-2 border-b border-default px-3 text-xs font-semibold text-highlighted"><UIcon name="i-lucide-folder-open" class="size-4 text-muted" /> Files</div>
          <FilesPane :request="requestFile" :cwd="focused.cwd" :raw-url="rawUrl" :activity="focusActivity" class="flex-1 min-h-0" @open="openFile" @open-diff="openDiff" @open-commit-diff="openCommitDiff" />
        </aside>
      </div>
      <!-- Nothing at all yet: the first-run welcome, which is not the sidebar's empty line (#359). -->
      <WelcomeCard v-else-if="!attention.sessions.value.length" />
      <div v-else-if="!active.length" class="flex-1 flex flex-col items-center justify-center gap-3 text-muted p-8">
        <UIcon name="i-lucide-layout-grid" class="size-10" />
        <p class="text-sm">No active sessions. Launch an agent or start one with <code>conductor host</code>.</p>
        <UButton label="Launch agent" icon="i-lucide-play" @click="useLaunchModal().show()" />
      </div>

      <div v-else class="flex flex-1 min-h-0">
        <WallQueue v-if="showQueue" ref="queue" :sessions="waiting" :answered="answered" :selected="queueSel" :sending="quick.sending.value" @reply="reply" @option="option" @open="focusSession" @select="queueSel = $event" />
        <div ref="grid" class="flex-1 min-h-0 p-3">
          <div v-if="!tiles.length" class="h-full flex items-center justify-center text-sm text-muted">Nothing matches this filter.</div>
          <div v-else class="grid h-full w-full gap-3" :style="gridStyle">
            <SessionTile v-for="s in tiles" :key="s.id" :session="s" :create-transport="transportFor(s)" @select="focusSession(s)" />
          </div>
        </div>
      </div>
    </template>
  </UDashboardPanel>

</template>
