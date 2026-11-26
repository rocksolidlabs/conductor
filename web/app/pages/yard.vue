<script setup lang="ts">
import { answersInChat } from '~/utils/attention'
import { emptyTabs, toggleFold } from '~/utils/editorTabs'
import { editorRows } from '~/utils/editorRows'
import type { EditorColumnHandle } from '~/components/EditorColumn.vue'
import type { SessionInfo } from '~/composables/useSessions'
import type { TransportKind, ActivityEntry, Welcome } from '~/utils/protocol'
import type { TransportState } from '~/utils/transport/types'
import { WALL_SHORTCUTS } from '~/composables/useShortcuts'
import { isActive } from '~/utils/attention'
import { sessionStatus } from '~/utils/status'
import { bestGrid, yardFilterOf, yardChips, yardCounts, yardFilter, type YardFilter } from '~/utils/wall'
import { replyFailure, replyOutcomeOf, type ReplyTaken } from '~/utils/reply'
import { pageState } from '~/utils/pageState'
import { QUICK_START_URL } from '~/utils/about'

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

// ?filter= (#303) names the chip a link opens on, read as the page opens and as the query changes; an unknown word opens All.
const filter = ref<YardFilter>(yardFilterOf(route.query.filter))
watch(
  () => route.query.filter,
  (q) => (filter.value = yardFilterOf(q)),
)
// The chips filter the live sessions by one state each (utils/wall.ts, on sessionStatus).
const now = useNow()
const active = computed(() => attention.sessions.value.filter(isActive))
const counts = computed(() => yardCounts(attention.sessions.value, now.value))
const chips = computed(() => yardChips(counts.value, filter.value))
const tiles = computed(() => yardFilter(attention.sessions.value, filter.value, now.value))
// Below sm the chips and the polling badge leave the header for a row of their own under it (the bar shows from sm up).
const phone = useIsPhone()
// Focus mode: /yard?focus=<id> expands one session in place. The grid is one
// Esc, one click or one browser Back away; the URL stays shareable.
const focusId = computed(() => (typeof route.query.focus === 'string' ? route.query.focus : ''))
const focused = computed<SessionInfo | undefined>(() => attention.sessions.value.find((s) => s.id === focusId.value))
// What the page shows when it has nothing to (P23): the grid's state over the live sessions, read before it judges (a Yard opened on
// a busy server never flashes its empty shape); a focused session not in the first read is not found, never before that read.
const gridState = computed(() => pageState({ read: attention.loaded.value, error: attention.error.value, count: active.value.length }))
const focusState = computed(() => (focused.value ? 'content' : attention.loaded.value ? 'missing' : attention.error.value ? 'failed' : 'reading'))
const shownState = computed(() => (focusId.value ? focusState.value : gridState.value))
const emptyActions = [
  { label: 'Launch agent', icon: 'i-lucide-play', kbd: 'N', onClick: () => useLaunchModal().show() },
  { label: 'How to bring one in', icon: 'i-lucide-book-open', to: QUICK_START_URL, external: true },
]
const missingActions = computed(() => [
  { label: 'Back to the grid', icon: 'i-lucide-arrow-left', onClick: backToGrid },
  { label: 'Its record', icon: 'i-lucide-history', to: `/history/sessions/${encodeURIComponent(focusId.value)}` },
])
const focusTerminal = ref<{ focus: () => void; requestFile: (p: string, s?: boolean, x?: FileGetExtra) => Promise<any> } | null>(null)
const viewers = ref(0)
const transport = ref<{ kind: TransportKind; state: TransportState; rtt: number | null }>({ kind: 'ws', state: 'idle', rtt: null })
// The editor area (design 4g): files open above the focused tile's terminal, the Files pane beside it.
const { tabs, editorOpen, openFile, previewFile, linkOpened, linkOffered, openDiff, openCommitDiff } = useEditorTabs()
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
const inTerminal = { usingInput: true }
// A dialog's, a slideover's, a menu's or the palette's Escape is its own (your menu's Alerts… and Open on your phone among them): the
// page's keys are not there while one is open, so the key reaches it whole (useOverlaid), as on a ticket's page.
const overlaid = useOverlaid()
defineShortcuts(
  computed(() =>
    overlaid.value
      ? {}
      : {
          escape: leaveFocus,
          alt_escape: { ...inTerminal, handler: leaveFocus },
          // Alt+Q leaves too (#312): Windows takes Alt+Esc for its own window cycling.
          alt_q: { ...inTerminal, handler: leaveFocus },
        },
  ),
)

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

/**
 * A reply names the question the bar showed (its id): one answered meanwhile takes nothing, and says so. `taken` tells the bar
 * what became of the line, so that a line not sent stays in its field (#654).
 */
function reply(s: SessionInfo, text: string, question?: string, taken?: ReplyTaken) {
  quick.reply(s, text, { question, sent: () => taken?.('left') }).then(
    (sent) => taken?.(sent ? 'sent' : 'held'),
    (e: unknown) => {
      toast.add(replyFailure(e, s.name, true))
      taken?.(replyOutcomeOf(e))
    },
  )
}

function option(s: SessionInfo, index: number, question?: string) {
  const o = s.attention?.options?.[index]
  if (!o) return
  quick.choose(s, o, { question }).catch((e: unknown) => toast.add(replyFailure(e, s.name)))
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

// This page's group in the command palette (#302): the chips, the grid; the focused
// tile's editor rows (#321).
usePalette().registerPage(() => {
  return {
    label: focused.value ? focused.value.name : 'Yard',
    sessionId: focused.value?.id,
    cwd: focused.value?.cwd,
    openFile: focused.value ? openFile : undefined,
    rows: [
      ...chips.value.map((c) => ({ key: `page:filter:${c.key}`, label: `${c.label} · ${c.count}`, icon: 'i-lucide-filter', on: c.active, run: () => (filter.value = c.key) })),
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
              <StatusMark v-if="focused" :status="sessionStatus(focused, now)" word class="text-xs" :data-yard-focused-status="focused.id" />
              <TransportBadge :kind="transport.kind" :state="transport.state" :rtt="transport.rtt" class="hidden sm:inline-flex" />
              <UBadge :label="`${viewers} here`" icon="i-lucide-users" color="neutral" variant="subtle" size="sm" class="hidden sm:inline-flex" />
            </template>
            <div v-else class="flex min-w-0 items-center gap-1.5 text-xs">
              <YardViews current="grid" />
              <YardChips v-if="!phone" :chips="chips" :connected="attention.connected.value" @pick="filter = $event" />
              <AttentionBar v-if="!phone" :sessions="attention.sessions.value" />
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
              <IconButton icon="i-lucide-ellipsis-vertical" color="neutral" variant="ghost" label="Session actions" data-yard-focus-menu />
            </UDropdownMenu>
          </template>
          <FullscreenButton />
        </template>
      </UDashboardNavbar>
      <UDashboardToolbar v-if="phone && !focusId" :ui="{ root: 'h-auto py-1.5', left: 'flex-wrap' }" data-yard-toolbar>
        <template #left>
          <YardChips :chips="chips" :connected="attention.connected.value" wrap @pick="filter = $event" />
        </template>
      </UDashboardToolbar>
    </template>

    <template #body>
      <UAlert v-if="attention.error.value && shownState !== 'failed'" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="attention.error.value" class="m-3" />

      <!-- Scrolls when the terminal's least height and the reply bar's question (#187: whole, up to 500 bytes) outgrow a short
           viewport, a landscape phone's, so that the choices stay reachable; the session page's body scrolls the same way. -->
      <div v-if="focusId" class="flex flex-1 min-h-0 gap-3 overflow-y-auto p-2 sm:p-3" data-yard-focus>
        <EditorColumn v-if="focused" ref="columnRef" v-model:tabs="tabs" :request="requestFile" :nvim="editor.nvim.value" :write="editor.writeFile" :can-edit="editor.canEdit.value" :cwd="focused.cwd" :raw-url="rawUrl" :host-away="focused.status === 'host_disconnected' ? focused.hostName || 'The host' : undefined" :bar-text="focused.attention?.message || focused.agentId">
          <TerminalView
            :key="focused.id"
            ref="focusTerminal"
            :create-transport="transportFor(focused)"
            operator
            :verbatim="focused.way === 'protocol' ? !!focused.verbatim : undefined"
            @welcome="onFocusWelcome"
            @activity="onFocusActivity"
            @nvim="editor.onNvim"
            @closed="editor.onClosed()"
            @viewers="viewers = $event.count"
            @transport="transport = $event"
            @open-file="openFile"
            @link-opened="linkOpened"
            @link-offered="linkOffered"
          />
          <template #bar>
            <QuickReplyBar v-if="!(focused.way === 'protocol' && answersInChat(focused.attention))" :attention="focused.attention" :agent-name="focused.agentId" role="control" :busy="quick.sending.value.has(focused.id)" answers @answer="decisionModal.open" @chat="(focusTerminal as { showQuestion?: () => void } | null)?.showQuestion?.()" @reply="(text, q, taken) => reply(focused!, text, q, taken)" @option="(i, q) => option(focused!, i, q)" @open-plan="openFile({ path: $event })" @preview-plan="previewFile({ path: $event })" />
          </template>
        </EditorColumn>
        <!-- Not in the first read: not found; before it, the reading shape (or the failed one). -->
        <PageEmpty v-else-if="focusState === 'missing'" state="missing" words="That session is no longer on this server." :actions="missingActions" />
        <PageEmpty v-else page="yard" :state="focusState === 'failed' ? 'failed' : 'reading'" thing="sessions" :error="attention.error.value" @again="attention.refresh()" />
        <aside v-if="focused" class="hidden md:flex w-[332px] flex-none flex-col overflow-hidden rounded-md border border-default bg-default" data-files-aside>
          <div class="flex h-9 flex-none items-center gap-2 border-b border-default px-3 text-xs font-semibold text-highlighted"><UIcon name="i-lucide-folder-open" class="size-4 text-muted" /> Files</div>
          <FilesPane :request="requestFile" :cwd="focused.cwd" :raw-url="rawUrl" :activity="focusActivity" class="flex-1 min-h-0" @open="openFile" @open-diff="openDiff" @open-commit-diff="openCommitDiff" />
        </aside>
      </div>
      <!-- Nothing live, once the sessions are read: the Yard's empty shape (the welcome is the home's alone); before, the reading one. -->
      <PageEmpty
        v-else-if="gridState !== 'content'"
        page="yard"
        :state="gridState"
        words="Nothing is running. A terminal shows here when you launch an agent, or bring one in from another machine."
        thing="sessions"
        :error="attention.error.value"
        :actions="emptyActions"
        @again="attention.refresh()"
      />

      <!-- The grid takes the body at every width (the UX review's P22): a question is answered in the sidebar's Needs you, the pill
           (] and [) or the expanded tile's own bar, never in a column beside the grid that shrinks every tile while it waits. -->
      <div v-else ref="grid" class="flex-1 min-h-0 p-3">
        <div v-if="!tiles.length" class="h-full flex items-center justify-center text-sm text-muted">Nothing matches this filter.</div>
        <div v-else class="grid h-full w-full gap-3" :style="gridStyle" data-yard-grid>
          <SessionTile v-for="s in tiles" :key="s.id" :session="s" :create-transport="transportFor(s)" @select="focusSession(s)" />
        </div>
      </div>
    </template>
  </UDashboardPanel>

</template>
