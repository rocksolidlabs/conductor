<script setup lang="ts">
import type { GraphDirection } from '~/utils/crewGraph'
import { lineLayout, stepFrom, type LinePoint, type LineStep, type LineStation } from '~/utils/lineDiagram'
import type { MarkView } from '~/utils/status'

/**
 * A run drawn as lines (UX1 PR-33, P15): an SVG over lineLayout, as RunTimeline is over utils/timeline.ts. A task is a station, a chain
 * of tasks a line, a branch or a merge a connector; every line and connector is one neutral ink, and only a station carries a state's
 * colour: its mark is StatusMark's svg form, the same table of marks as everywhere else. The layout is a computed keyed on the stations'
 * ids and needs alone, so a state that changes (and the page's clock) recolours a station and never moves it.
 *
 * Sizes: `full`, the Graph view's, fitted to the pane it fills (never above 1, never below 0.5; past that the pane scrolls inside
 * itself), its stations buttons: a click or Space selects, Enter or a double click opens, the arrows move the focus along a line and
 * across the lanes (stepFrom), Tab walks them by depth, then lane. `card` (the same geometry with its labels) and `mini` (no labels) are
 * pictures scaled into `maxWidth`, never larger than drawn; a station with no mark is hollow there, and a `manual` one dashed.
 */
export interface LineStationInfo {
  id: string
  needs?: string[]
  /** The task's own state (`data-task-state`), where there is one. */
  state?: string
  /** Its state's mark; none draws a hollow station. */
  mark?: Pick<MarkView, 'key' | 'word' | 'legend'>
  /** What it does now in words: the station's name for a screen reader and its title, after its id. */
  words?: string
  /** Started by hand (a member's start rule): its station's ring is dashed. */
  manual?: boolean
}

const props = withDefaults(
  defineProps<{
    stations: LineStationInfo[]
    direction?: GraphDirection
    size?: 'full' | 'card' | 'mini'
    selected?: string
    /** `card` and `mini`: the widest the picture is drawn, in px. */
    maxWidth?: number
    /** The drawing's name, for a screen reader. */
    label?: string
  }>(),
  { direction: 'LR', size: 'full', selected: '', maxWidth: 520, label: 'The run as lines' },
)
const emit = defineEmits<{ select: [id: string]; open: [id: string] }>()

const interactive = computed(() => props.size === 'full')

// The layout's input, as a string: a change of state, words or marks leaves it equal, so the layout is not made again.
const shapeKey = computed(() => JSON.stringify(props.stations.map((s) => [s.id, s.needs ?? []])))
const layout = computed(() => {
  const nodes = (JSON.parse(shapeKey.value) as Array<[string, string[]]>).map(([id, needs]) => (needs.length ? { id, needs } : { id }))
  return lineLayout(nodes, { direction: props.direction, size: props.size === 'mini' ? 'mini' : 'full' })
})
const byId = computed(() => new Map(props.stations.map((s) => [s.id, s])))
// The stations in the order Tab walks them: by depth, then lane.
const ordered = computed(() => [...layout.value.stations].sort((a, b) => a.depth - b.depth || a.lane - b.lane))
const g = computed(() => layout.value.geometry)
const at = computed(() => new Map(layout.value.stations.map((s) => [s.id, s])))

/** A path through `points`, each turn rounded by a quarter circle of `corner` (no more than half of either run beside it). */
function pathOf(points: readonly LinePoint[], corner: number): string {
  const [first, ...rest] = points
  if (!first) return ''
  let d = `M${first.x},${first.y}`
  rest.forEach((p, k) => {
    const a = points[k]!
    const c = points[k + 2]
    if (!c || corner <= 0) {
      d += ` L${p.x},${p.y}`
      return
    }
    const r = Math.min(corner, Math.hypot(p.x - a.x, p.y - a.y) / 2, Math.hypot(c.x - p.x, c.y - p.y) / 2)
    const [ix, iy] = [Math.sign(p.x - a.x), Math.sign(p.y - a.y)]
    const [ox, oy] = [Math.sign(c.x - p.x), Math.sign(c.y - p.y)]
    const sweep = ix * oy - iy * ox > 0 ? 1 : 0
    d += ` L${p.x - ix * r},${p.y - iy * r} A${r},${r} 0 0 ${sweep} ${p.x + ox * r},${p.y + oy * r}`
  })
  return d
}

const lines = computed(() =>
  layout.value.lines.map((l) => ({
    id: l.id,
    stations: l.stations.join(' '),
    d: pathOf(
      l.stations.flatMap((id) => at.value.get(id) ?? []),
      0,
    ),
  })),
)
const connectors = computed(() => layout.value.connectors.map((c) => ({ key: `${c.from}-${c.to}`, kind: c.kind, d: pathOf(c.points, c.corner) })))

// The scale: full fits the pane it fills (0.5 to 1); card and mini fit maxWidth (at most 1).
const pane = useTemplateRef<HTMLDivElement>('pane')
const box = ref({ w: 0, h: 0 })
watch(
  pane,
  (el, _prev, onCleanup) => {
    if (!el) return
    const read = () => (box.value = { w: el.clientWidth, h: el.clientHeight })
    read()
    const ro = new ResizeObserver(read)
    ro.observe(el)
    onCleanup(() => ro.disconnect())
  },
  { immediate: true },
)
const scale = computed(() => {
  const { width, height } = layout.value
  if (props.size !== 'full') return Math.min(1, props.maxWidth / width)
  if (!box.value.w || !box.value.h) return 1
  return Math.max(0.5, Math.min(1, box.value.w / width, box.value.h / height))
})
const drawnW = computed(() => Math.floor(layout.value.width * scale.value * 100) / 100)
const drawnH = computed(() => Math.floor(layout.value.height * scale.value * 100) / 100)

/** The station's name: its id, then what it does now. */
function nameOf(s: LineStation): string {
  const words = byId.value.get(s.id)?.words ?? s.warning
  return words ? `${s.id}: ${words}` : s.id
}

const KEYS: Record<GraphDirection, Record<string, LineStep>> = {
  LR: { ArrowRight: 'right', ArrowLeft: 'left', ArrowDown: 'down', ArrowUp: 'up' },
  // Top to bottom a line runs down and the lanes across.
  TB: { ArrowDown: 'right', ArrowUp: 'left', ArrowRight: 'down', ArrowLeft: 'up' },
}
const svg = useTemplateRef<SVGSVGElement>('svg')
function focusStation(id: string) {
  svg.value?.querySelector<SVGGElement>(`[data-station="${CSS.escape(id)}"]`)?.focus()
}
function onKey(e: KeyboardEvent, id: string) {
  if (e.key === 'Enter') {
    e.preventDefault()
    emit('open', id)
    return
  }
  if (e.key === ' ') {
    e.preventDefault()
    emit('select', id)
    return
  }
  const step = KEYS[props.direction][e.key]
  if (!step) return
  e.preventDefault()
  focusStation(stepFrom(layout.value, id, step))
}

/** Fit again: the drawing back at its start, scaled to the pane as it is now. */
function fit() {
  const el = pane.value
  if (!el) return
  box.value = { w: el.clientWidth, h: el.clientHeight }
  el.scrollTo({ left: 0, top: 0 })
}
defineExpose({ fit, focusStation })
</script>

<template>
  <div
    ref="pane"
    :class="size === 'full' ? 'absolute inset-0 overflow-auto' : 'max-w-full'"
    :style="size === 'full' ? undefined : { width: `${drawnW}px` }"
    :data-line-pane="size === 'full' ? '' : undefined"
    :data-line-size="size"
    :data-line-scale="scale.toFixed(3)"
  >
    <div :class="size === 'full' && 'grid min-h-full min-w-full w-max place-items-center'">
      <svg
        ref="svg"
        :width="drawnW"
        :height="drawnH"
        :viewBox="`0 0 ${layout.width} ${layout.height}`"
        class="block font-mono"
        :role="interactive ? 'group' : 'img'"
        :aria-label="label"
        data-line-diagram
        :data-line-direction="direction"
      >
        <g fill="none" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" style="stroke: var(--ui-text-muted)">
          <path v-for="l in lines" :key="l.id" :d="l.d" :data-line="l.id" :data-line-stations="l.stations" />
          <path v-for="c in connectors" :key="c.key" :d="c.d" :data-connector="c.key" :data-connector-kind="c.kind" />
        </g>
        <g
          v-for="s in ordered"
          :key="s.id"
          class="line-station outline-none"
          :class="interactive && 'cursor-pointer'"
          :role="interactive ? 'button' : undefined"
          :tabindex="interactive ? 0 : undefined"
          :aria-label="interactive ? nameOf(s) : undefined"
          :aria-pressed="interactive ? selected === s.id : undefined"
          :data-station="s.id"
          :data-task-state="byId.get(s.id)?.state"
          :data-status-mark="byId.get(s.id)?.mark?.key"
          :data-lane="s.lane"
          :data-depth="s.depth"
          :data-junction="s.junction"
          :data-station-manual="byId.get(s.id)?.manual ? '' : undefined"
          :data-station-selected="selected === s.id ? '' : undefined"
          @click="interactive && emit('select', s.id)"
          @dblclick="interactive && emit('open', s.id)"
          @keydown="interactive && onKey($event, s.id)"
        >
          <title>{{ nameOf(s) }}</title>
          <circle v-if="interactive" :cx="s.x" :cy="s.y" :r="Math.max(22, g.radius)" fill="transparent" data-station-hit />
          <!-- The focus ring: a round box whose outline shows with the keyboard's focus (an SVG shape draws no outline of its own shape). -->
          <foreignObject v-if="interactive" :x="s.x - g.radius - 8" :y="s.y - g.radius - 8" :width="2 * g.radius + 16" :height="2 * g.radius + 16" pointer-events="none">
            <div class="line-station-focus size-full rounded-full" />
          </foreignObject>
          <circle v-if="selected === s.id" :cx="s.x" :cy="s.y" :r="g.radius + 3.5" fill="none" stroke-width="2" style="stroke: var(--ui-primary)" data-station-ring />
          <!-- The disc under the mark keeps the line from showing through a ring. -->
          <circle :cx="s.x" :cy="s.y" :r="g.radius + 1" style="fill: var(--line-station-bg, var(--ui-bg))" />
          <StatusMark v-if="byId.get(s.id)?.mark" :status="byId.get(s.id)!.mark!" svg :x="s.x" :y="s.y" :r="g.radius" />
          <circle
            v-else
            :cx="s.x"
            :cy="s.y"
            :r="g.radius - 0.75"
            fill="none"
            stroke-width="1.5"
            style="stroke: var(--ui-text-muted)"
            :stroke-dasharray="byId.get(s.id)?.manual ? `${g.radius * 0.6} ${g.radius * 0.45}` : undefined"
            data-station-hollow
          />
          <text
            v-if="s.label.box && s.label.text"
            :x="s.label.box.x"
            :y="s.label.box.y + s.label.box.h - 2"
            font-size="11"
            style="fill: var(--ui-text)"
            :data-station-label="s.id"
            pointer-events="none"
          >
            {{ s.label.text }}
          </text>
        </g>
      </svg>
    </div>
  </div>
</template>

<style scoped>
.line-station:focus-visible .line-station-focus {
  outline: 2px solid var(--ui-primary);
  outline-offset: -2px;
}
</style>
