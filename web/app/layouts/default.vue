<script setup lang="ts">
import type { NavigationMenuItem } from '@nuxt/ui'
import { SIDEBAR_SIZE } from '~/utils/sidebar'
import { noticeWords, updateFailedWords, updateReadyWords } from '~/utils/desktop'

const showToken = ref(false)
/** Where the desktop sidebar shows (lg and up, DESKTOP_SIDEBAR): below it the list is a page and the bar is at the foot, so the sidebar's list is not rendered. */
const desktopWide = useMedia('(min-width: 64rem)')
const attention = useAttention()
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
// for the first, 18 % again after mount for the second.
const sidebarDefaultSize = ref(sidebar.size.value)
onMounted(() => (sidebarDefaultSize.value = SIDEBAR_SIZE.default))

// The share links joined from here: looked at again every minute, for the sidebar's Shared with you.
const joined = useJoined()
onMounted(() => joined.start())
// The unread chat counts, as another tab changes them.
const chatUnread = useChatUnread()
onMounted(() => chatUnread.listen())

// A notice the desktop app owes once (an upgrade that changed what sharing does): one toast with the way to Settings.
onMounted(async () => {
  const words = noticeWords((await desktop.bridge.value?.notice?.().catch(() => '')) ?? '')
  if (words) toast.add({ ...words, icon: 'i-lucide-globe', color: 'info', duration: 20000, actions: [{ label: 'Settings', onClick: () => router.push('/settings') }] })
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

/** Saves the full sidebar's width (Nuxt UI's `--width` on its root, in percent) once a drag or a double-click on the handle has set it. */
async function keepWidth() {
  await nextTick()
  if (sidebar.rail.value) return
  sidebar.resize(Number.parseFloat(sidebarRoot()?.style.getPropertyValue('--width') ?? ''))
}
/** A drag ends where the pointer is let go, anywhere on the page: Nuxt UI listens on the document, so does this. */
function keepWidthAfter(end: 'mouseup' | 'touchend') {
  document.addEventListener(end, keepWidth, { once: true })
}

// A run page keeps the Crews page lit in the foot: a run is the Crews page's.
const runRoute = computed(() => route.path.startsWith('/runs/'))
// Dispatch's pages (its projects, a project's list, a ticket) keep it lit.
const dispatchRoute = computed(() => route.path === '/dispatch' || route.path.startsWith('/dispatch/'))

const nav = computed<NavigationMenuItem[]>(() => [
  { label: 'Yard', icon: 'i-lucide-layout-grid', to: '/yard', badge: attention.count.value ? { label: String(attention.count.value), color: 'warning', variant: 'solid' } : undefined },
  { label: 'Roundhouse', icon: 'i-lucide-gallery-horizontal', to: '/roundhouse' },
  { label: 'Agents', icon: 'i-lucide-bot', to: '/agents' },
  {
    label: 'Crews',
    icon: 'i-lucide-users',
    to: '/crews',
    active: route.path.startsWith('/crews') || runRoute.value,
  },
  { label: 'Dispatch', icon: 'i-lucide-ticket', to: '/dispatch', active: dispatchRoute.value },
  { label: 'Events', icon: 'i-lucide-radio-tower', to: '/events' },
  ...(desktop.isDesktop.value ? [{ label: 'Settings', icon: 'i-lucide-settings', to: '/settings' }] : []),
])

// On the rail Nuxt UI's collapsed menu hides the links' labels and the Yard's
// count: each link is named by an aria-label instead, and the Yard's icon
// carries an amber chip while any session needs you.
const railNav = computed<NavigationMenuItem[]>(() =>
  nav.value.map((item) => {
    const n = item.to === '/yard' ? attention.count.value : 0
    return { ...item, 'aria-label': n ? `${item.label}, ${n} ${n === 1 ? 'needs' : 'need'} you` : item.label, chip: n ? { color: 'warning' } : undefined }
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
  'g-y': chord(() => router.push('/yard')),
  'g-r': chord(() => router.push('/roundhouse')),
  'g-a': chord(() => router.push('/agents')),
  'g-e': chord(() => router.push('/events')),
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
  <UDashboardGroup :persistent="false" class="pb-[calc(3.5rem+env(safe-area-inset-bottom))] lg:pb-0">
    <!-- The rail's classes are lg: only: below lg the slideover gets the same header, body and footer classes, and keeps the full sidebar's. -->
    <UDashboardSidebar
      :id="SIDEBAR_ID"
      v-model:collapsed="railModel"
      collapsible
      :resizable="!sidebar.rail.value"
      :min-size="SIDEBAR_SIZE.min"
      :default-size="sidebarDefaultSize"
      :max-size="SIDEBAR_SIZE.max"
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
        <div v-else class="flex items-center gap-1.5 px-1 w-full min-w-0">
          <img src="/brand/conductor-mark.svg" alt="" class="size-7 dark:hidden" />
          <img src="/brand/conductor-mark-reversed.svg" alt="" class="size-7 hidden dark:block" />
          <span class="font-semibold truncate">Conductor</span>
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
        <UNavigationMenu :items="collapsed ? railNav : nav" orientation="vertical" :collapsed="collapsed" tooltip :ui="{ link: collapsed ? 'justify-center' : undefined }" class="w-full" />
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
  </UDashboardGroup>
</template>
