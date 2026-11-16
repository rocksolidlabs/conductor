<script setup lang="ts">
import type { NavigationMenuItem } from '@nuxt/ui'
import { SIDEBAR_SIZE, SIDEBAR_WIDTH } from '~/utils/sidebar'
import { noticeWords, updateFailedWords, updateReadyWords } from '~/utils/desktop'
import { navPages, pageActive, pageTip, type PageId } from '~/utils/pages'
import { needsCount, needsRowShown } from '~/utils/needsYou'

const showToken = ref(false)
/** Where the desktop sidebar shows (lg and up, DESKTOP_SIDEBAR): below it the list is a page and the bar is at the foot, so the sidebar's list is not rendered. */
const desktopWide = useMedia('(min-width: 64rem)')
// The decision inbox: read for the whole app, one of the stores of the one number on Needs you (useNeedsYou).
const inbox = useInbox()
onMounted(() => inbox.start())
const desktop = useDesktop()
const sidebar = useSidebar()
const shortcuts = useShortcutsModal()
const launch = useLaunchModal()
const palette = usePalette()
const cursor = useNeedsCursor()
// Fullscreen: one listener and one F shortcut for every page under this layout.
const fs = useFullscreenToggle()
fs.listen()
fs.shortcuts()
const router = useRouter()
const toast = useToast()
const route = useRoute()
const list = useTemplateRef<{ focusFilter: () => void }>('list')

// The sidebar's collapsed state is the rail, and useSidebar (localStorage) is
// its one source: the group's own persistence is off (persistent false below),
// so no Nuxt UI cookie can bring back another state on load.
const railModel = computed({
  get: () => sidebar.rail.value,
  set: (collapsed: boolean) => (collapsed ? sidebar.collapse() : sidebar.expand()),
})

// The sidebar's fixed id. Nuxt UI names its root `<storageKey>-sidebar-<id>`,
// the group's storageKey being its default, 'dashboard'.
const SIDEBAR_ID = 'main'
const SIDEBAR_ROOT_ID = `dashboard-sidebar-${SIDEBAR_ID}`
function sidebarRoot() {
  return document.getElementById(SIDEBAR_ROOT_ID)
}
/** Where the desktop sidebar, and so the rail, shows: Tailwind's lg (its root is `hidden lg:flex`); below it the sidebar is a slideover. */
const DESKTOP_SIDEBAR = '(min-width: 64rem)'

/**
 * `/`, Alt+S and the rail's search button open the command palette (#302);
 * the sidebar's filter box stays a plain filter of the list, reached by a
 * click, and `list` keeps `focusFilter` for it.
 */
function openPalette() {
  palette.show()
}
void list
// Alt chords on keys defineShortcuts cannot name (it matches an Alt chord by its code, and knows no name for the slash): one
// listener, from a terminal or not, as the Alt twins are. The terminal hands these codes back (ALT_PASSTHROUGH_CODES).
function goSettings() {
  if (desktop.isDesktop.value) void router.push('/settings')
}
/** When a G chord last fired: a page's own letter (the session page's S) stands down for the keydown that completed one. */
const lastChord = useState<number>('lastChordAt', () => 0)
function chord(run: () => void) {
  return () => {
    lastChord.value = Date.now()
    run()
  }
}
const ALT_CHORDS: Record<string, () => void> = { Slash: openPalette, BracketRight: () => cursor.step(1, true), BracketLeft: () => cursor.step(-1, true), Comma: goSettings }
/** On a Mac ⌘K is the palette's from a terminal too (TerminalView lets it through; defineShortcuts pauses a plain chord while xterm's text area has the focus). */
const isMac = import.meta.client && /Mac|iPhone|iPad/.test(navigator.platform)
function onAltChord(e: KeyboardEvent) {
  if (isMac && e.metaKey && !e.ctrlKey && !e.altKey && !e.shiftKey && e.code === 'KeyK') {
    e.preventDefault()
    palette.toggle()
    return
  }
  if (!e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return
  const run = ALT_CHORDS[e.code]
  if (!run) return
  e.preventDefault()
  run()
}
onMounted(() => document.addEventListener('keydown', onAltChord))
onBeforeUnmount(() => document.removeEventListener('keydown', onAltChord))

/** The collapse and expand buttons go away with their mode: focus moves to the other one rather than falling to the page. */
async function collapseByButton() {
  sidebar.collapse()
  await nextTick()
  sidebarRoot()?.querySelector<HTMLElement>('[data-rail-expand]')?.focus()
}
async function expandByButton() {
  sidebar.expand()
  await nextTick()
  sidebarRoot()?.querySelector<HTMLElement>('[data-sidebar-collapse]')?.focus()
}

// The full sidebar's width survives a reload, kept by useSidebar too: with
// the group's persistence off, Nuxt UI keeps it for the visit only. Nuxt UI
// takes the starting width from defaultSize when the sidebar sets up, and
// goes back to defaultSize on a double-click on the handle: the saved width
// for the first, the minimum after mount for the second (#437). The bounds
// follow the window, and SIDEBAR_WIDTH keeps the drawn width within them.
const sidebarDefaultSize = ref(sidebar.size.value)
onMounted(() => (sidebarDefaultSize.value = SIDEBAR_SIZE.min))
sidebar.follow()

// The share links joined from here: looked at again every minute, for the sidebar's Shared with you.
const joined = useJoined()
onMounted(() => joined.start())
// The unread chat counts, as another tab changes them.
const chatUnread = useChatUnread()
onMounted(() => chatUnread.listen())

// A notice the desktop app owes once (an upgrade that changed what sharing does): one toast with the way to Settings.
onMounted(async () => {
  const words = noticeWords((await desktop.bridge.value?.notice?.().catch(() => '')) ?? '')
  if (words) toast.add({ ...words, icon: 'i-lucide-globe', color: 'info', duration: 20000, actions: [{ label: 'Settings', onClick: () => router.push('/settings?section=switchyard') }] })
})

// An update the desktop app downloaded (#290): said once per version, in a toast that offers the restart, never in a dialog of
// the app's own; kept in the sidebar's header until it installs; and one the app announced that did not install, said once. The
// app says when a check found one, and the page asks what to say, so one found while no window was open is said as one loads.
const update = useDesktopUpdate()
async function askUpdate() {
  const b = desktop.bridge.value
  const u = await b?.update?.().catch(() => null)
  if (!u) return
  update.ready.value = u.ready
  if (u.announce && u.ready) {
    toast.add({
      ...updateReadyWords(u.ready),
      icon: 'i-lucide-circle-arrow-up',
      color: 'info',
      duration: 30000,
      actions: [{ label: 'Restart to update', onClick: () => (update.restartFor.value = u.ready) }, { label: 'Later', color: 'neutral', variant: 'outline' }],
    })
  }
  // The tray's Restart to update: asked here, in the dialog that says what it ends.
  if (u.restart) update.restartFor.value = u.restart
  if (u.failed) {
    toast.add({ ...updateFailedWords(u.failed), icon: 'i-lucide-triangle-alert', color: 'warning', duration: 30000, actions: [{ label: 'Show the log', onClick: () => void b?.showLog() }] })
  }
}
// A window hidden in the tray asks nothing: the first to ask is told the update once, and a toast there would go unseen. It
// asks once it shows again.
let askWhenShown = false
function askUpdateShown() {
  if (document.visibilityState !== 'visible') {
    askWhenShown = true
    return
  }
  askWhenShown = false
  void askUpdate()
}
function onVisibility() {
  if (askWhenShown && document.visibilityState === 'visible') askUpdateShown()
}
let offUpdate: (() => void) | undefined
onMounted(() => {
  if (!desktop.bridge.value?.update) return
  document.addEventListener('visibilitychange', onVisibility)
  askUpdateShown()
  offUpdate = desktop.bridge.value.onUpdate?.(askUpdateShown)
})
onBeforeUnmount(() => {
  offUpdate?.()
  document.removeEventListener('visibilitychange', onVisibility)
})

/** The width Nuxt UI holds for the full sidebar (`--width` on its root, in rem); NaN when it holds none. */
function heldWidth(): number {
  return Number.parseFloat(sidebarRoot()?.style.getPropertyValue('--width') ?? '')
}
/**
 * Saves the full sidebar's held width once a drag or a double-click on the handle has set it. After a press, `before` is the
 * width held when it began: a press that moved nothing saves nothing, so a width dragged in a wider window, held while a
 * narrower one draws it clamped, is not overwritten by the narrower window's maximum (#437).
 */
async function keepWidth(before?: number) {
  await nextTick()
  if (sidebar.rail.value) return
  const width = heldWidth()
  if (before !== undefined && width === before) return
  sidebar.resize(width)
}
/** A drag ends where the pointer is let go, anywhere on the page: Nuxt UI listens on the document, so does this. */
function keepWidthAfter(end: 'mouseup' | 'touchend') {
  const before = heldWidth()
  document.addEventListener(end, () => void keepWidth(before), { once: true })
}

// Brakeman's row (docs/plans/brakeman.md, PR-6): the briefs this browser has not opened, counted as they arrive; nothing else
// happens when one does (no toast, no dialog, no navigation). Shown once the server says it has Brakeman (not a switchyard's).
const brakeman = useBrakeman()
const workbench = useWorkbenchToken()
// Read with a token only: a read without one would ask for it. A new token may be another server's, whose br-1 is not the one
// held: what the store holds goes first, and replies to the reads sent with the token before are dropped.
onMounted(() =>
  watch(
    () => workbench.token.value,
    (t, before) => {
      if (before !== undefined) brakeman.reset()
      if (t) void brakeman.load()
    },
    { immediate: true },
  ),
)
const brakemanCount = computed(() => brakeman.unopened.value.length)

// The one number of what needs you (utils/needsYou.ts), on Needs you alone: drawn "9", "9+" past a store cut short, and not at all
// while it is not known. Every place that draws it carries data-count-of="needs".
const needs = useNeedsYou()
const needsWords = computed(() => needsCount(needs.list.value))
// The row leaves when nothing waits and nothing was decided that day (needsRowShown); the clock moves its day on every minute.
const minute = ref(Date.now())
onMounted(() => {
  const t = window.setInterval(() => (minute.value = Date.now()), 60_000)
  onBeforeUnmount(() => window.clearInterval(t))
})
const needsRow = computed(() => needsRowShown(inbox.state.value, needs.list.value, route.path, minute.value))

/** A page's badge in the foot: Needs you's one number in amber, Brakeman's briefs not yet opened. */
function navBadge(id: PageId): NavigationMenuItem['badge'] {
  // The hook rides on the badge as an attribute (Nuxt UI binds the badge object onto its UBadge).
  if (id === 'decisions') return needsWords.value && needs.number.value ? ({ label: needsWords.value, color: 'warning', variant: 'solid', 'data-count-of': 'needs' } as NavigationMenuItem['badge']) : undefined
  if (id === 'brakeman') return brakemanCount.value ? { label: String(brakemanCount.value), color: 'primary', variant: 'subtle' } : undefined
  return undefined
}
/** The hooks the tests find a page's row by. */
const NAV_HOOKS: Partial<Record<PageId, string>> = { decisions: 'data-sidebar-decide', brakeman: 'data-sidebar-brakeman' }

// The foot's pages, from the one table (utils/pages.ts): each one's sentence is its tooltip (pageTip), drawn by the label's slot.
const navEntries = computed(() => navPages({ brakeman: !!brakeman.available.value, desktop: desktop.isDesktop.value, needs: needsRow.value }))
const nav = computed<NavigationMenuItem[]>(() =>
  navEntries.value.map((p) => {
    const hook = NAV_HOOKS[p.id]
    // `tip` is the label slot's alone: Nuxt UI hands a link only its link, aria- and data- keys.
    return { label: p.label, icon: p.icon, to: p.to, active: pageActive(p.id, route.path), badge: navBadge(p.id), tip: pageTip(p), ...(hook ? { [hook]: '' } : {}) }
  }),
)

// On the rail Nuxt UI's collapsed menu hides the links' labels and their
// badges: each link is named by an aria-label instead, and Needs you's icon
// carries an amber chip while anything waits, its label the one number. Its
// tooltip, on hover and on focus, is the page's sentence (Nuxt UI takes
// `text` from the item's tooltip object over the label).
const railNav = computed<NavigationMenuItem[]>(() =>
  nav.value.map((item, i) => {
    const p = navEntries.value[i]!
    const tooltip = { text: pageTip(p) }
    if (p.id === 'decisions') {
      const d = needs.number.value ? needsWords.value : ''
      return { ...item, tooltip, 'aria-label': d ? `${p.title}, ${d} waiting` : p.title, 'data-count-of': 'needs', chip: d ? { color: 'warning' } : undefined }
    }
    if (p.id === 'brakeman') {
      const b = brakemanCount.value
      return { ...item, tooltip, 'aria-label': b ? `${item.label}, ${b} new ${b === 1 ? 'brief' : 'briefs'}` : item.label, chip: b ? { color: 'primary' } : undefined }
    }
    return { ...item, tooltip, 'aria-label': item.label }
  }),
)

// Shortcuts pause while a terminal or a text field has focus (see useShortcuts).
// Plain keys work outside terminals and text fields; the Alt chords also
// work inside a terminal (see ALT_PASSTHROUGH_CODES in useShortcuts).
const inTerminal = { usingInput: true }
defineShortcuts({
  // Ctrl+K outside a terminal (inside one it stays the shell's); on a Mac the listener above takes ⌘K everywhere.
  ...(isMac ? {} : { meta_k: () => palette.toggle() }),
  meta_b: () => sidebar.toggle(),
  '?': () => shortcuts.show(),
  n: () => launch.show(),
  '/': openPalette,
  'g-n': chord(() => router.push('/decisions')),
  'g-y': chord(() => router.push('/yard')),
  'g-r': chord(() => router.push('/roundhouse')),
  'g-a': chord(() => router.push('/agents')),
  'g-e': chord(() => router.push('/events')),
  'g-h': chord(() => router.push('/history')),
  'g-c': chord(() => router.push('/crews')),
  'g-d': chord(() => router.push('/dispatch')),
  'g-s': chord(goSettings),
  ']': () => cursor.step(1),
  '[': () => cursor.step(-1),
  alt_b: { ...inTerminal, handler: () => sidebar.toggle() },
  alt_h: { ...inTerminal, handler: () => shortcuts.show() },
  alt_n: { ...inTerminal, handler: () => launch.show() },
  alt_s: { ...inTerminal, handler: openPalette },
  alt_y: { ...inTerminal, handler: () => router.push('/yard') },
  alt_r: { ...inTerminal, handler: () => router.push('/roundhouse') },
  alt_a: { ...inTerminal, handler: () => router.push('/agents') },
  alt_e: { ...inTerminal, handler: () => router.push('/events') },
  alt_c: { ...inTerminal, handler: () => router.push('/crews') },
  alt_d: { ...inTerminal, handler: () => router.push('/dispatch') },
})
</script>

<template>
  <!-- Below lg the bottom bar needs the room; with viewport-fit=cover its padding reads the safe area. -->
  <UDashboardGroup :persistent="false" unit="rem" class="pb-[calc(3.5rem+env(safe-area-inset-bottom))] lg:pb-0">
    <!-- The rail's classes are lg: only: below lg the slideover gets the same header, body and footer classes, and keeps the full sidebar's. -->
    <UDashboardSidebar
      :id="SIDEBAR_ID"
      v-model:collapsed="railModel"
      collapsible
      :resizable="!sidebar.rail.value"
      :min-size="sidebar.bounds.value.min"
      :default-size="sidebarDefaultSize"
      :max-size="sidebar.bounds.value.max"
      :style="sidebar.rail.value ? undefined : { width: SIDEBAR_WIDTH }"
      :ui="{
        header: sidebar.rail.value ? 'lg:px-0 lg:justify-center' : undefined,
        body: sidebar.rail.value ? 'gap-0 py-2 lg:px-1' : 'gap-0 py-2',
        footer: sidebar.rail.value ? 'border-t border-default flex-col items-stretch lg:items-center gap-1 lg:px-1' : 'border-t border-default flex-col items-stretch gap-1',
      }"
    >
      <template #header="{ collapsed }">
        <div v-if="collapsed" class="grid w-full place-items-center">
          <img src="/brand/conductor-mark.svg" alt="" class="size-7 dark:hidden" />
          <img src="/brand/conductor-mark-reversed.svg" alt="" class="size-7 hidden dark:block" />
        </div>
        <!-- With an update pending the header holds one more button (1.625 rem with its gap). Where the row's content is
             narrower than at the minimum width plus that button (15.8125 rem: 16.75 rem less the header's 1 px border and 2 rem
             padding and the row's 0.5 rem, 14.1875 rem, plus the button), the mark steps aside so that the name keeps at least the
             width it has there without the update (#604); wider, nothing changes. -->
        <div v-else class="@container flex items-center gap-1.5 px-1 w-full min-w-0" data-sidebar-brand>
          <span class="flex shrink-0" :class="update.ready.value && '@max-[15.8125rem]:hidden'" data-sidebar-mark>
            <img src="/brand/conductor-mark.svg" alt="" class="size-7 dark:hidden" />
            <img src="/brand/conductor-mark-reversed.svg" alt="" class="size-7 hidden dark:block" />
          </span>
          <span class="font-semibold truncate" title="Conductor" data-sidebar-name>Conductor</span>
          <!-- Alerts and your menu beside the name (design 3b): the foot holds only the pages. -->
          <SidebarHeaderMenus class="ml-auto" @token="showToken = true" />
          <UTooltip text="Collapse to the rail" :kbds="['meta', 'B']" ignore-non-keyboard-focus>
            <UButton icon="i-lucide-panel-left-close" color="neutral" variant="ghost" size="xs" aria-label="Collapse sidebar" class="hidden lg:inline-flex" data-sidebar-collapse @click="collapseByButton" />
          </UTooltip>
        </div>
      </template>

      <template #default="{ collapsed }">
        <SidebarRail v-if="collapsed && desktopWide" @search="openPalette" />
        <SessionSidebar v-else-if="desktopWide" ref="list" />
      </template>

      <!-- Nuxt UI's handle, with the width saved once a drag or a double-click has set it. -->
      <template #resize-handle="{ onMouseDown, onTouchStart, onDoubleClick, ui }">
        <UDashboardResizeHandle
          v-if="!sidebar.rail.value"
          :aria-controls="SIDEBAR_ROOT_ID"
          data-slot="handle"
          :class="ui.handle()"
          @mousedown="(e: MouseEvent) => { onMouseDown(e); keepWidthAfter('mouseup') }"
          @touchstart="(e: TouchEvent) => { onTouchStart(e); keepWidthAfter('touchend') }"
          @dblclick="(e: MouseEvent) => { onDoubleClick(e); keepWidth() }"
        />
      </template>

      <template #footer="{ collapsed }">
        <!-- On the canvas the theme's raised pill would vanish in light (sage on sage): a hovered link takes the canvas's hover tone and
             the current page's link the page's tone, as the sidebar's rows do. -->
        <UNavigationMenu :items="collapsed ? railNav : nav" orientation="vertical" :collapsed="collapsed" tooltip :ui="{ link: `${collapsed ? 'justify-center ' : ''}hover:before:bg-canvas-hover data-active:before:bg-default` }" class="w-full">
          <!-- The full sidebar's label says what the page is on hover (Nuxt UI draws a tooltip only on the rail). -->
          <template #item-label="{ item }">
            <template v-if="collapsed">{{ item.label }}</template>
            <UTooltip v-else :text="item.tip" :content="{ side: 'right' }" :delay-duration="300">
              <span data-nav-label>{{ item.label }}</span>
            </UTooltip>
          </template>
        </UNavigationMenu>
        <!-- On the rail the header's menus and the expand button stack in the foot. -->
        <div v-if="collapsed" class="flex flex-col items-center gap-1 px-1 pt-1" data-rail-foot>
          <SidebarHeaderMenus collapsed @token="showToken = true" />
          <UTooltip text="Expand sidebar" :kbds="['meta', 'B']" :content="{ side: 'right' }" ignore-non-keyboard-focus>
            <UButton icon="i-lucide-panel-left-open" color="neutral" variant="ghost" size="sm" aria-label="Expand sidebar" class="hidden lg:inline-flex" data-rail-expand @click="expandByButton" />
          </UTooltip>
        </div>
      </template>
    </UDashboardSidebar>

    <slot />

    <BottomBar @token="showToken = true" />
    <WorkbenchTokenGate v-model:open="showToken" />
    <UpdateRestartModal />
    <ShortcutsModal />
    <CommandPalette @token="showToken = true" />
    <NeedsYouPill />
    <LaunchSessionModal v-model:open="launch.open.value" @launched="(s) => navigateTo(`/sessions/${s.id}`)" />
    <RenameSessionModal />
    <CloneSessionModal />
    <DecisionModal />
  </UDashboardGroup>
</template>
