<script setup lang="ts">
import type { SessionInfo } from '~/composables/useSessions'
import { CAROUSEL_SHORTCUTS } from '~/composables/useShortcuts'
import { isActive } from '~/utils/attention'
import { keptOrder, normalizeSettings, orderSessions, type CarouselSettings } from '~/utils/carousel'
import { followJump, type RoutedEvent } from '~/utils/events'
import { relativeTime, shortCwd } from '~/utils/sessions'
import { pageState } from '~/utils/pageState'
import { sessionStatus } from '~/utils/status'
// A link to a session opens its place: a crew's member inside its crew (UX3 R72).
const placeOf = useSessionPlace()

// The Roundhouse is the Yard's second view (the UX review's P12): its header says Yard, its tab names the view.
useHead({ title: 'Yard · Roundhouse' })

const attention = useAttention()
const events = useEvents()
const admin = useWorkbenchToken()
const { create } = useTerminalTransport()
useShortcutsModal().registerPage(CAROUSEL_SHORTCUTS)
usePageAltCodes().register(['KeyQ'])

// The settings kept per browser (#310 added the strip's order and view): each field checked on the way in.
const SETTINGS_KEY = 'conductor.carousel.settings'
const LEGACY_KEY = 'conductor.wall.settings'
const settings = ref<CarouselSettings>(normalizeSettings(null))
try {
  const raw = localStorage.getItem(SETTINGS_KEY) ?? localStorage.getItem(LEGACY_KEY)
  if (raw) settings.value = normalizeSettings(JSON.parse(raw))
} catch {
  /* ignore */
}
watch(
  settings,
  (v) => {
    try {
      localStorage.setItem(SETTINGS_KEY, JSON.stringify(v))
    } catch {
      /* ignore */
    }
  },
  { deep: true },
)

const intervalItems = [
  { label: '5s', value: 5000 },
  { label: '10s', value: 10000 },
  { label: '20s', value: 20000 },
  { label: '30s', value: 30000 },
  { label: '60s', value: 60000 },
]

// The active sessions in the strip's kept order (#310): the slides, stepping, "Next up" and follow mode all go by it. A session
// started since goes at the end, one that ended leaves it; what shows is what is kept.
const active = computed(() => orderSessions(attention.sessions.value.filter(isActive), settings.value.order))
/** What the page shows when nothing is live (P23): judged once the sessions are read, never before. */
const shownState = computed(() => pageState({ read: attention.loaded.value, error: attention.error.value, count: active.value.length }))
// Written back at once too: the first list shown is the order a session started afterwards is appended to (Codex's round on #310).
watch(
  active,
  (list) => {
    // An empty list (the store not filled yet, as the page loads) keeps the order: it is what the sessions are put back into.
    if (!list.length) return
    const kept = keptOrder(list)
    if (kept.join('\n') !== settings.value.order.join('\n')) settings.value.order = kept
  },
  { immediate: true },
)
/** The strip rearranged: the current session keeps the screen, at its new place. */
async function reorder(ids: string[]) {
  const cur = current.value?.id
  settings.value.order = ids
  await nextTick()
  const i = active.value.findIndex((s) => s.id === cur)
  if (i >= 0 && i !== selected.value) {
    selected.value = i
    carousel.value?.emblaApi?.scrollTo(i, true)
  }
}
// The waiting sessions in the strip's order too (whole-batch round): follow mode's cycle and "Next up" go by it.
const waitingIds = computed(() => new Set(attention.needsInput.value.map((s) => s.id)))
const waiting = computed(() => active.value.filter((s) => waitingIds.value.has(s.id)))
// Follow mode follows input requests while the Alerts rules route needs_input
// to Roundhouse jump; other events routed there jump too (followEvent below).
const followInput = computed(() => settings.value.follow && events.routes.value.needs_input.wall)
interface AutoplayPlugin {
  play: () => void
  stop: () => void
  isPlaying: () => boolean
}
interface EmblaLike {
  scrollTo: (i: number, jump?: boolean) => void
  scrollPrev: () => void
  scrollNext: () => void
  plugins: () => { autoplay?: AutoplayPlugin }
  on: (event: string, cb: () => void) => void
  off: (event: string, cb: () => void) => void
}
const carousel = useTemplateRef<{ emblaApi?: EmblaLike }>('carousel')
const selected = ref(0)
const paused = ref(false)
const typing = ref(false)
const current = computed<SessionInfo | undefined>(() => active.value[selected.value])

interface TerminalHandle {
  focus: () => void
  snapshot: () => string[] | null
}
/** The strip's thumbnail of a slide: its own view's screen, none for a slide that is not connected (#310). */
function snapshotOf(id: string): string[] | null {
  return terminals.get(id)?.snapshot() ?? null
}
const terminals = new Map<string, TerminalHandle>()
function terminalRef(id: string) {
  return (el: unknown) => {
    if (el) terminals.set(id, el as TerminalHandle)
    else terminals.delete(id)
  }
}

// The autoplay plugin stays loaded (re-creating it mid-scroll would snap the
// carousel back); rotation is started and stopped explicitly. It stops while
// a session needs input (follow mode), while a terminal has keyboard focus,
// while paused, and while the mouse hovers over the pane.
// The hover is the page's, from the pointer moving over the pane (#1021): the
// plugin's own hover stop listened for mouseenter, which the browser also
// sends when the pane grows under a pointer at rest. Closing the strip's
// thumbnails does that (the pane grows into the place of the button just
// pressed), and the rotation stopped there while the header said Rotating.
// Follow mode holds on a waiting session for a bounded time (two intervals,
// at least 20 s) so one stray prompt never freezes the rotation; the waiting
// session stays in the rotation and the hold repeats for each new prompt.
const now = ref(Date.now())
/** The status words' clock (useNow); `now` above is the hold's, by the second. */
const clock = useNow()
const holdUntil = ref(0)
const holdMs = computed(() => Math.max(2 * settings.value.intervalMs, 20000))
const holding = computed(() => followInput.value && waiting.value.length > 0 && now.value < holdUntil.value)
const holdLeft = computed(() => Math.max(0, Math.ceil((holdUntil.value - now.value) / 1000)))
const holdingId = computed(() => (holding.value && current.value?.attention?.state === 'needs_input' ? current.value.id : undefined))
function releaseHold() {
  holdUntil.value = 0
}
const autoplayOptions = computed(() =>
  settings.value.autoplay && active.value.length > 1
    ? { delay: settings.value.intervalMs, stopOnMouseEnter: false, stopOnInteraction: false, stopOnFocusIn: false }
    : false,
)
/** The mouse is over the pane, known from its moves there (never from an enter alone, see above). */
const hovering = ref(false)
function onPointerMove(e: PointerEvent) {
  if (e.pointerType === 'mouse') hovering.value = true
}
function onPointerLeave() {
  hovering.value = false
}
// A pane that goes from under the pointer (nothing left running) sends no leave.
watch(shownState, (s) => {
  if (s !== 'content') hovering.value = false
})
/** The rotation as the header shows it: on unless turned off, paused, held or typing. */
const rotationOn = computed(() => !!autoplayOptions.value && !paused.value && !holding.value && !typing.value)
/** The rotation as it runs: also held while the mouse hovers over the pane. */
const rotating = computed(() => rotationOn.value && !hovering.value)
const stateLabel = computed(() => {
  if (typing.value) return 'Typing'
  if (paused.value) return 'Paused'
  if (holding.value) return 'Holding'
  if (!autoplayOptions.value) return 'Manual'
  return 'Rotating'
})

// Progress of the current interval, for the film strip.
const progress = ref(0)
let lastTick = Date.now()
let progressTimer: number | undefined
watch(rotating, (on) => {
  lastTick = Date.now()
  if (!on) progress.value = 0
})

function autoplay() {
  return carousel.value?.emblaApi?.plugins().autoplay
}

function syncRotation() {
  const plugin = autoplay()
  if (!plugin) return
  if (rotating.value) plugin.play()
  else plugin.stop()
}

watch(rotating, syncRotation)
watch(
  () => carousel.value?.emblaApi,
  (api, _prev, onCleanup) => {
    if (!api) return
    // The plugin restarts itself (on its init, at a drag's end); re-apply our state when it does.
    const guard = () => {
      if (!rotating.value) autoplay()?.stop()
    }
    api.on('autoplay:play', guard)
    api.on('reInit', syncRotation)
    onCleanup(() => {
      api.off('autoplay:play', guard)
      api.off('reInit', syncRotation)
    })
    nextTick(syncRotation)
  },
  { immediate: true },
)

function scrollTo(index: number) {
  carousel.value?.emblaApi?.scrollTo(index)
  selected.value = index
}

function onSelect(index: number) {
  selected.value = index
  lastTick = Date.now()
  progress.value = 0
}

/** Puts the keyboard in the current session's terminal. Nothing steals focus on its own. */
function focusCurrent() {
  const s = current.value
  if (s) terminals.get(s.id)?.focus()
}

function leaveTerminal() {
  const el = document.activeElement as HTMLElement | null
  if (el?.closest?.('.terminal-host')) el.blur()
}

function prev() {
  carousel.value?.emblaApi?.scrollPrev()
}

function next() {
  carousel.value?.emblaApi?.scrollNext()
}

function togglePlay() {
  if (holding.value) {
    releaseHold()
    return
  }
  paused.value = !paused.value
}

function onFocusIn(e: FocusEvent) {
  typing.value = !!(e.target as HTMLElement | null)?.closest?.('.terminal-host')
}

function onFocusOut(e: FocusEvent) {
  const to = e.relatedTarget as HTMLElement | null
  typing.value = !!to?.closest?.('.terminal-host')
}

// Follow mode: jump to a session the first time it asks for input and hold
// there for a bounded time. With several waiting, cycle among them while
// holding. Never yank the keyboard away from someone who is typing.
const jumpedAt = ref<string | null>(null)
const seenPrompts = new Map<string, string>() // session id → attention.since already jumped to
let cycleTimer: number | undefined
watch(
  [waiting, followInput],
  ([list, follow]) => {
    window.clearInterval(cycleTimer)
    if (!follow || !list.length) {
      jumpedAt.value = null
      holdUntil.value = 0
      return
    }
    const fresh = list.find((s) => seenPrompts.get(s.id) !== (s.attention?.since ?? ''))
    if (fresh) {
      for (const s of list) seenPrompts.set(s.id, s.attention?.since ?? '')
      holdUntil.value = Date.now() + holdMs.value
      const target = active.value.findIndex((s) => s.id === fresh.id)
      if (target >= 0 && target !== selected.value && !typing.value) scrollTo(target)
      jumpedAt.value = new Date().toISOString()
    }
    if (list.length > 1) {
      // From the current slide, in the kept order.
      let i = Math.max(0, list.findIndex((s) => s.id === current.value?.id))
      cycleTimer = window.setInterval(() => {
        if (typing.value || !holding.value) return
        i = (i + 1) % list.length
        const idx = active.value.findIndex((s) => s.id === list[i]!.id)
        if (idx >= 0) scrollTo(idx)
      }, Math.max(settings.value.intervalMs, 5000))
    }
  },
  { immediate: true },
)

// Any other event the Alerts rules route to Roundhouse jump moves the Roundhouse to
// its session, once, without a hold of its own; never while someone is typing
// or while follow mode holds on a session that needs input (followJump).
function followEvent(e: RoutedEvent) {
  const target = followJump(e, {
    follow: settings.value.follow,
    routes: events.routes.value,
    typing: typing.value,
    holding: holding.value,
    activeIds: active.value.map((s) => s.id),
    selected: selected.value,
  })
  if (target !== null) scrollTo(target)
}
let stopFollowing: (() => void) | undefined

// "Next up" follows the same order follow mode uses: waiting sessions first.
const nextUp = computed<{ session: SessionInfo; waiting: boolean } | null>(() => {
  const n = active.value.length
  if (n < 2) return null
  if (followInput.value && waiting.value.length) {
    const others = waiting.value.filter((s) => s.id !== current.value?.id)
    if (others.length) return { session: others[0]!, waiting: true }
  }
  const s = active.value[(selected.value + 1) % n]
  return s ? { session: s, waiting: s.attention?.state === 'needs_input' } : null
})

let tick: number | undefined

// Plain keys work outside the terminal; the Alt chords also work inside it.
const inTerminal = { usingInput: true }
defineShortcuts({
  arrowleft: prev,
  arrowright: next,
  enter: focusCurrent,
  ' ': togglePlay,
  alt_arrowleft: { ...inTerminal, handler: prev },
  alt_arrowright: { ...inTerminal, handler: next },
  alt_p: { ...inTerminal, handler: togglePlay },
  alt_escape: { ...inTerminal, handler: leaveTerminal },
  // Alt+Q leaves too (#312): Windows takes Alt+Esc for its own window cycling.
  alt_q: { ...inTerminal, handler: leaveTerminal },
})

function transportFor(s: SessionInfo) {
  return () => create({ sessionId: s.id, token: admin.token.value, kind: s.kind })
}

function agentLabel(s: SessionInfo) {
  const names: Record<string, string> = { claude: 'Claude Code', codex: 'Codex', agy: 'Antigravity' }
  return names[s.agentId] ?? s.agentId
}

function meta(s: SessionInfo) {
  const where = s.kind === 'hosted' ? `hosted by ${s.hostUser || '?'} on ${s.hostName || 'dev machine'}` : 'server'
  return `${agentLabel(s)} · ${where} · ${shortCwd(s.cwd)}`
}

// Keep the current slide and its neighbours connected; the rest stay idle.
function near(index: number) {
  const n = active.value.length
  if (n <= 3) return true
  const d = Math.abs(index - selected.value)
  return d <= 1 || d === n - 1
}

onMounted(() => {
  if (!admin.hasToken.value) admin.needsToken.value = true
  attention.start()
  stopFollowing = events.onEvent(followEvent)
  tick = window.setInterval(() => (now.value = Date.now()), 1000)
  progressTimer = window.setInterval(() => {
    progress.value = rotating.value ? Math.min(1, (Date.now() - lastTick) / settings.value.intervalMs) : 0
  }, 250)
  window.addEventListener('keydown', onEscape)
})
function onEscape(e: KeyboardEvent) {
  if (e.key === 'Escape') leaveTerminal()
}
onBeforeUnmount(() => {
  stopFollowing?.()
  window.clearInterval(cycleTimer)
  window.clearInterval(tick)
  window.clearInterval(progressTimer)
  window.removeEventListener('keydown', onEscape)
})

// This page's group in the command palette (#302): the rotation, follow mode, the interval.
usePalette().registerPage(() => ({
  label: 'Roundhouse',
  sessionId: current.value?.id,
  cwd: current.value?.cwd,
  rows: [
    { key: 'page:rotation', label: holding.value ? 'Release the hold and keep rotating' : paused.value ? 'Resume rotation' : 'Pause rotation', detail: stateLabel.value, icon: rotationOn.value ? 'i-lucide-pause' : 'i-lucide-play', kbds: ['space'], run: togglePlay },
    { key: 'page:autoplay', label: 'Auto-rotate', icon: 'i-lucide-rotate-cw', on: settings.value.autoplay, run: () => (settings.value.autoplay = !settings.value.autoplay) },
    { key: 'page:follow', label: 'Follow routed events', icon: 'i-lucide-hand', on: settings.value.follow, run: () => (settings.value.follow = !settings.value.follow) },
    ...intervalItems.map((it) => ({ key: `page:interval:${it.value}`, label: `Every ${it.label}`, icon: 'i-lucide-timer', on: settings.value.intervalMs === it.value, run: () => (settings.value.intervalMs = it.value) })),
  ],
}))
</script>

<template>
  <UDashboardPanel id="carousel" :ui="{ body: 'p-0 sm:p-0 flex flex-col min-h-0 gap-0 overflow-hidden' }">
    <template #header>
      <UDashboardNavbar :toggle="false" title="Yard" :ui="{ root: 'h-14' }" data-roundhouse-header>
        <template #trailing>
          <div class="flex items-center gap-2 ml-2">
            <YardViews current="roundhouse" />
            <span v-if="active.length" class="text-xs text-muted tabular-nums">{{ selected + 1 }} / {{ active.length }}</span>
            <UBadge v-if="!attention.connected.value" label="polling" color="warning" variant="subtle" size="sm" />
          </div>
        </template>
        <template #right>
          <div class="flex items-center gap-0.5 rounded-md border border-default p-0.5">
            <IconButton icon="i-lucide-chevron-left" size="xs" color="neutral" variant="ghost" label="Previous session" @click="prev" />
            <UTooltip :text="holding ? 'Release the hold and keep rotating' : paused ? 'Resume rotation' : 'Pause rotation'" :kbds="['space']">
              <UButton :label="stateLabel" :icon="rotationOn ? 'i-lucide-play' : 'i-lucide-pause'" size="xs" color="neutral" variant="soft" class="font-semibold" @click="togglePlay" />
            </UTooltip>
            <IconButton icon="i-lucide-chevron-right" size="xs" color="neutral" variant="ghost" label="Next session" @click="next" />
          </div>
          <span class="hidden md:flex items-center gap-1.5 text-xs text-muted">Every <USelect v-model="settings.intervalMs" :items="intervalItems" size="xs" class="w-20 font-mono" :disabled="!settings.autoplay" /></span>
          <USwitch v-model="settings.autoplay" label="Auto-rotate" size="sm" class="hidden md:flex" />
          <UTooltip text="Jump to sessions whose events are routed to Roundhouse jump in Alerts: input requests and handoffs unless changed there">
            <USwitch v-model="settings.follow" label="Follow routed events" size="sm" class="hidden lg:flex" />
          </UTooltip>
          <FullscreenButton />
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <UAlert v-if="attention.error.value && shownState !== 'failed'" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="attention.error.value" class="m-3" />

      <PageEmpty
        v-if="shownState !== 'content'"
        page="roundhouse"
        :state="shownState"
        words="Nothing is running. The first agent you launch shows here."
        thing="sessions"
        :error="attention.error.value"
        :actions="[{ label: 'Launch agent', icon: 'i-lucide-play', kbd: 'N', onClick: () => useLaunchModal().show() }]"
        @again="attention.refresh()"
      />

      <div v-else class="flex-1 min-h-0 flex flex-col" @focusin="onFocusIn" @focusout="onFocusOut">
        <div class="flex-1 min-h-0 px-4 pt-3">
          <UCarousel
            ref="carousel"
            v-slot="{ item, index }"
            :items="active"
            :autoplay="autoplayOptions"
            loop
            fade
            class="h-full"
            :ui="{ viewport: 'h-full', container: 'h-full', item: 'h-full basis-full' }"
            @select="onSelect"
            @pointermove="onPointerMove"
            @pointerleave="onPointerLeave"
          >
            <div class="flex h-full flex-col gap-3">
              <div class="flex items-center gap-3">
                <SessionAvatar :agent-id="item.agentId" size="md" solid />
                <div class="flex min-w-0 flex-col">
                  <div class="flex items-center gap-2"><span class="truncate text-base font-semibold">{{ item.name }}</span><StatusMark :status="sessionStatus(item, clock)" word class="text-xs" :data-roundhouse-status="item.id" /></div>
                  <span class="truncate text-[11.5px] text-muted tabular-nums">{{ meta(item) }}</span>
                </div>
                <div class="ml-auto flex items-center gap-2">
                  <UBadge v-if="holdingId === item.id && jumpedAt" color="warning" variant="subtle" size="sm" icon="i-lucide-hand" :label="`Jumped here ${relativeTime(jumpedAt, now)} ago · holding ${holdLeft}s more`" />
                  <UButton label="Open page" icon="i-lucide-square-terminal" size="sm" color="neutral" variant="outline" :to="placeOf(item.id)" />
                </div>
              </div>
              <div class="relative flex-1 min-h-0 rounded-lg" :class="typing && index === selected ? 'ring-[3px] ring-primary ring-offset-2 ring-offset-default' : ''">
                <TerminalView v-if="near(index)" :key="item.id" :ref="terminalRef(item.id)" :create-transport="transportFor(item)" :auto-focus="false" operator :verbatim="item.way === 'protocol' ? !!item.verbatim : undefined" />
                <div v-else class="terminal-host rounded-lg border border-default flex items-center justify-center text-xs text-muted">idle</div>
                <UBadge v-if="typing && index === selected" label="Paused: terminal has focus" icon="i-lucide-pause" color="neutral" variant="solid" size="sm" class="absolute top-3 right-4" />
              </div>
            </div>
          </UCarousel>
        </div>

        <div class="flex flex-col gap-2.5 px-4 pb-4 pt-3">
          <CarouselStrip v-model:view="settings.strip" :sessions="active" :selected="selected" :progress="progress" :holding-id="holdingId" :rotating="rotating" :snapshot="snapshotOf" @select="scrollTo" @reorder="reorder" />
          <div class="flex items-center gap-3 text-xs text-muted">
            <span v-if="nextUp">
              Next up: <b class="text-default">{{ nextUp.session.name }}</b><template v-if="nextUp.waiting">. It also needs input, so it goes before running sessions</template>
            </span>
            <span class="ml-auto hidden md:flex items-center gap-3 font-mono text-[11px]">
              <span>← → step</span><span>Space pause</span><span>Enter type</span><span>Esc leave terminal</span><span>drag the strip to reorder</span>
            </span>
          </div>
        </div>
      </div>
    </template>
  </UDashboardPanel>
</template>
