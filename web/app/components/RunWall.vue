<script setup lang="ts">
import type { RunInfo, SessionInfo } from '~/composables/useSessions'
import type { CrewFeedItem } from '~/utils/crews'
import type { ChatMessage } from '~/utils/protocol'
import type { TerminalTransport } from '~/utils/transport/types'
import { memberLook } from '~/utils/crewWords'
import { tileAskOf } from '~/utils/tileAsk'
import { memberOffer } from '~/utils/runs'
import { choiceKey } from '~/utils/attention'
import { evenShares, gutterRange, movedShares, TILE_GAP, TILE_MIN_HEIGHT, TILE_MIN_WIDTH, wallTracks, type RunLayout, type RunWallTile, type WallShares } from '~/utils/wall'

/**
 * The run page's Members view: a tile for every member of one run in the run's order (a live terminal where the member has a session,
 * else a placeholder that says its state and offers Start now or Resume), and, when the person turned it on, the run's Activity as a
 * column at the right. The tiles fit their area, as on the wall, the last one taking the cells its row leaves; on a narrow area they
 * stack, Activity under them, and the area scrolls. The page hands it the run and its tiles and takes back what a person asks of a
 * tile (open, start); a member's question is answered through the page's `answer`.
 */
const props = defineProps<{
  tiles: RunWallTile[]
  run: RunInfo | null
  runId: string
  /** Every session the live store holds: a member's state (memberLook) reads the sessions of its run. */
  sessions: readonly SessionInfo[]
  feed: CrewFeedItem[]
  now: number
  /** The member whose start the page has asked for, '' for none: its Start now spins. */
  starting: string
  createTransport: (s: SessionInfo) => () => TerminalTransport
  /** Whether the page may answer a question line (useRunChatAnswer): a tile offers its choices only then. */
  answerable: (line: ChatMessage) => boolean
  answer: (line: ChatMessage, index: number) => Promise<void>
  /** The members that will get the next line from the run's message bar: their tiles carry the ring. */
  receiving?: readonly string[]
  /** Whether the run's Activity shows beside the tiles. */
  activity: boolean
  /** How the tiles are laid out, the member One large draws large, and the shares the layout's gaps were dragged to. */
  layout: RunLayout
  large: string
  sizes?: WallShares
}>()

const emit = defineEmits<{ open: [sessionId: string]; start: [name: string]; 'show-large': [name: string]; decision: [id: string]; resize: [axis: 'cols' | 'rows', shares: number[]] }>()
/** Whether the tiles stack (a narrow area): no layout applies there, and the page draws no switch. */
const stacked = defineModel<boolean>('narrow', { default: false })

function footer(t: RunWallTile): { where: string; diff: string } {
  const m = t.member
  const branch = m?.branch || t.session?.branch || ''
  // A managed run's member is on a task: `build #2 · crew/<run>/build.2`.
  const where = m?.task ? `${m.task} #${m.attempt}${branch ? ` · ${branch}` : ''}` : branch
  const diff = m?.diff ? `+${m.diff.added} −${m.diff.removed}` : ''
  return { where, diff }
}

/**
 * A member's state in the Crews pages' one vocabulary (C2, memberLook): the tile's foot and a tile without a session say it, with
 * whom it waits for ("after driver") on a member not started.
 */
function looked(t: RunWallTile) {
  return t.member && props.run ? memberLook(props.run, t.member, props.sessions, props.now) : null
}

/**
 * A tile's question (C6): who asks, the question whole and its choices, the same line the run's chat offers, answered through the
 * same path (the member's running session, the question named); none where the page cannot answer it.
 */
function tileAsk(t: RunWallTile) {
  return tileAskOf(t.name, t.session, props.answerable)
}

/**
 * A number key on a tile's frame (not its terminal, not a field): the choice of that number answers the tile's question, as on the
 * sidebar's focused row; a choice that changes the permission mode has no number (choiceKey). One answer at a time a tile.
 */
const answering = new Set<string>()
async function digit(t: RunWallTile, n: number) {
  const a = tileAsk(t)
  if (a?.kind !== 'choices' || answering.has(t.name)) return
  const i = (a.line.options ?? []).findIndex((o, k) => choiceKey(o, k) === n)
  if (i < 0) return
  answering.add(t.name)
  try {
    await props.answer(a.line, i)
  } finally {
    answering.delete(t.name)
  }
}

/** Follows an element's content box. */
function boxOf(el: Readonly<Ref<HTMLDivElement | null>>) {
  const box = ref({ w: 0, h: 0 })
  watch(
    el,
    (node, _prev, onCleanup) => {
      if (!node) return
      const observer = new ResizeObserver((entries) => {
        const r = entries[0]?.contentRect
        if (r) box.value = { w: r.width, h: r.height }
      })
      observer.observe(node)
      onCleanup(() => observer.disconnect())
    },
    { immediate: true },
  )
  return box
}
// The whole area decides whether the tiles stack; the tiles' own box (the area less Activity's column) decides their grid.
const areaEl = useTemplateRef<HTMLDivElement>('area')
const gridEl = useTemplateRef<HTMLDivElement>('grid')
const area = boxOf(areaEl)
const grid = boxOf(gridEl)
const narrow = computed(() => area.value.w > 0 && area.value.w < 640)

/**
 * Shows a member's tile (the message bar's chip of a member that waits on a question): scrolled into view and its frame focused, so its
 * question is in front of the person. False when the member has no tile drawn.
 */
function show(name: string): boolean {
  const tile = gridEl.value?.querySelector<HTMLElement>(`[data-member="${CSS.escape(name)}"]`)
  if (!tile) return false
  tile.scrollIntoView({ block: 'nearest' })
  ;(tile.querySelector<HTMLElement>('[data-session-tile]') ?? tile).focus()
  return true
}
defineExpose({ show })
watch(narrow, (n) => (stacked.value = n), { immediate: true })
const largeIndex = computed(() => props.tiles.findIndex((t) => t.name === props.large))
const tracks = computed(() => wallTracks(props.layout, props.tiles.length, grid.value.w, grid.value.h, largeIndex.value, props.sizes))
const gridStyle = computed(() =>
  narrow.value ? { gridTemplateColumns: 'minmax(0, 1fr)', gridAutoRows: '16rem' } : { gridTemplateColumns: tracks.value.columns, gridTemplateRows: tracks.value.rows },
)
/**
 * A tile's cell. The tiles keep their order on the page whatever the layout, so a change of layout moves them and never draws them
 * again: a terminal is fitted to its new cell, never reconnected. One large's large tile stays in view, the whole height of the
 * wall, while the column beside it scrolls.
 */
function cellStyle(i: number): Record<string, string> | undefined {
  if (narrow.value) return undefined
  const cell = tracks.value.cells[i]
  if (i !== tracks.value.large) return cell
  return { ...cell, position: 'sticky', top: '0', alignSelf: 'start', height: `${grid.value.h}px` }
}
// The gaps a person drags (RunTileGutter): read off the tracks as drawn, after each change of the tracks or of the wall's box.
const drawn = ref<{ cols: number[]; rows: number[] }>({ cols: [], rows: [] })
function measure() {
  const el = gridEl.value
  if (!el || narrow.value) {
    drawn.value = { cols: [], rows: [] }
    return
  }
  const cs = getComputedStyle(el)
  const px = (s: string) => s.split(' ').map(parseFloat).filter((x) => Number.isFinite(x))
  drawn.value = { cols: px(cs.gridTemplateColumns), rows: px(cs.gridTemplateRows) }
}
watch([tracks, narrow], () => nextTick(measure), { immediate: true })
const AXES: Record<RunLayout, Array<'cols' | 'rows'>> = { grid: ['cols', 'rows'], columns: ['cols'], rows: ['rows'], large: ['cols'] }
const gutters = computed(() => {
  const { cols, rows } = drawn.value
  if (narrow.value || props.tiles.length < 2) return []
  const span = (a: number[]) => a.reduce((x, y) => x + y, 0) + TILE_GAP * Math.max(0, a.length - 1)
  return AXES[props.layout].flatMap((axis) => {
    const px = axis === 'cols' ? cols : rows
    const floor = axis === 'cols' ? TILE_MIN_WIDTH : TILE_MIN_HEIGHT
    return px.slice(0, -1).map((_, k) => ({
      key: `${axis}-${k}`,
      axis,
      k,
      px,
      floor,
      at: px.slice(0, k + 1).reduce((x, y) => x + y, 0) + TILE_GAP * k,
      length: span(axis === 'cols' ? rows : cols),
      range: gutterRange(px, k, floor),
      value: (100 * px[k]!) / (px[k]! + px[k + 1]!),
      label: props.layout === 'large' ? 'Resize the large tile and the column' : axis === 'cols' ? `Resize columns ${k + 1} and ${k + 2}` : `Resize rows ${k + 1} and ${k + 2}`,
    }))
  })
})
type Gutter = (typeof gutters.value)[number]
function moveGutter(g: Gutter, delta: number) {
  emit('resize', g.axis, movedShares(g.px, g.k, delta, g.floor))
}
/** A key moves a gap by 5 % of its axis. */
function stepGutter(g: Gutter, sign: 1 | -1) {
  moveGutter(g, sign * 0.05 * g.px.reduce((x, y) => x + y, 0))
}
function evenGutter(g: Gutter) {
  emit('resize', g.axis, evenShares(g.px, g.k))
}

/** Whether a tile is one of One large's small ones, which offer to be drawn large. */
function small(i: number) {
  return !narrow.value && tracks.value.large >= 0 && i !== tracks.value.large
}
</script>

<template>
  <div ref="area" class="flex min-h-0 flex-1 gap-3 p-3" :class="narrow ? 'flex-col overflow-y-auto' : 'overflow-hidden'" :data-activity-shown="props.activity || undefined">
    <div
      ref="grid"
      class="relative grid min-h-0 min-w-0 flex-1 gap-3"
      :class="narrow ? 'flex-none' : ['h-full', tracks.scroll === 'x' ? 'overflow-x-auto overflow-y-hidden' : 'overflow-y-auto']"
      :style="gridStyle"
      :data-run-layout="narrow ? 'stacked' : props.layout"
      data-run-grid
    >
      <template v-for="(t, i) in props.tiles" :key="t.session?.id ?? `m-${t.name}`">
        <SessionTile
          v-if="t.session"
          :style="cellStyle(i)"
          :session="t.session"
          :status="looked(t)?.view"
          :create-transport="props.createTransport(t.session)"
          :receiving="!!props.receiving?.includes(t.name)"
          resume-word
          :data-member="t.name"
          @select="emit('open', t.session.id)"
          @digit="digit(t, $event)"
        >
          <template v-if="tileAsk(t)" #ask>
            <RunTileAsk :ask="tileAsk(t)!" :answer="props.answer" @open="emit('open', t.session!.id)" @decision="emit('decision', $event)" />
          </template>
          <template v-if="small(i)" #header-action>
            <IconButton :label="`Show ${t.name} large`" icon="i-lucide-layout-panel-left" size="xs" color="neutral" variant="ghost" data-tile-show-large @click.stop="emit('show-large', t.name)" />
          </template>
          <template #footer>
            <span class="truncate font-mono" data-mono="commit" data-branch>{{ footer(t).where }}</span>
            <span class="ml-auto flex-none font-mono" data-mono="measure" data-diff>{{ footer(t).diff }}</span>
          </template>
        </SessionTile>
        <div v-else class="flex min-h-0 flex-col overflow-hidden rounded-lg border border-dashed border-accented" :style="cellStyle(i)" :data-member="t.name">
          <div class="flex items-center gap-2 border-b border-default px-2.5 py-1.5 text-xs">
            <SessionAvatar :agent-id="t.member?.agentId ?? ''" dashed />
            <span class="flex-1 truncate text-[13px] font-semibold">{{ t.name }}</span>
            <IconButton v-if="small(i)" :label="`Show ${t.name} large`" icon="i-lucide-layout-panel-left" size="xs" color="neutral" variant="ghost" data-tile-show-large @click.stop="emit('show-large', t.name)" />
          </div>
          <div class="flex flex-1 flex-col items-center justify-center gap-2 p-3 text-center text-sm text-muted">
            <span v-if="looked(t)" class="inline-flex flex-wrap items-center justify-center gap-x-2" data-member-words :data-phase="looked(t)!.phase">
              <StatusMark :status="looked(t)!.view" word />
              <span v-if="looked(t)!.after && looked(t)!.phase === 'waiting'">{{ looked(t)!.after }}</span>
            </span>
            <UButton
              v-if="memberOffer(t.member?.status, props.run) === 'start'"
              label="Start now"
              icon="i-lucide-play"
              size="xs"
              color="neutral"
              variant="outline"
              :loading="props.starting === t.name"
              @click="emit('start', t.name)"
            />
            <ResumeButton v-else-if="memberOffer(t.member?.status, props.run) === 'resume'" :run-id="props.runId" :member="t.name" stay size="xs" />
          </div>
        </div>
      </template>
      <RunTileGutter
        v-for="g in gutters"
        :key="g.key"
        :axis="g.axis === 'cols' ? 'x' : 'y'"
        :at="g.at"
        :length="g.length"
        :gap="TILE_GAP"
        :min="g.range.min"
        :max="g.range.max"
        :value="g.value"
        :label="g.label"
        @move="moveGutter(g, $event)"
        @step="stepGutter(g, $event)"
        @even="evenGutter(g)"
      />
    </div>
    <CrewFeed v-if="props.activity" :items="props.feed" :run-id="props.runId" class="flex-none" :class="narrow ? 'h-80' : 'w-80'" />
  </div>
</template>
