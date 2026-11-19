<script setup lang="ts">
import type { RunInfo, SessionInfo } from '~/composables/useSessions'
import type { FeedEntry } from '~/utils/events'
import { clockTime } from '~/utils/crewWords'
import { durationLabel, scaleX, tickLabel, ticks, timelineOf } from '~/utils/timeline'

/**
 * The run's timeline: one row per member, its bar from its start to its end
 * or now, amber segments while its session waited for input, a mark where it
 * handed work to another member (with a line to that member's row) and the
 * run's stopped line. Drawn as SVG from the run and the live feed, so it
 * follows the stream; a hover reads the exact times. The names' column is as wide as the longest name, up to a cap (C15); a name
 * longer than that is cut at its end, the whole name in its title.
 */
const props = defineProps<{ run: RunInfo; sessions: SessionInfo[]; feed: FeedEntry[]; now: number }>()

/** The names' column: at least this wide, at most LABEL_CAP, as wide as the longest name between. */
const LABEL_MIN = 80
const LABEL_CAP = 240
const LABEL_PAD = 16
const LABEL_FONT = '12px ui-sans-serif, system-ui, sans-serif'
const ROW_H = 28
const HEAD_H = 22
const PAD_R = 16

const host = useTemplateRef<HTMLDivElement>('host')
const width = ref(640)
watch(
  host,
  (el, _prev, onCleanup) => {
    if (!el) return
    const ro = new ResizeObserver((entries) => {
      const w = entries[0]?.contentRect.width
      if (w) width.value = Math.max(320, Math.floor(w))
    })
    ro.observe(el)
    onCleanup(() => ro.disconnect())
  },
  { immediate: true },
)

const tl = computed(() => timelineOf(props.run, props.sessions, props.feed, props.now))

// A name's width as the SVG draws it (a canvas measures the same font); 7 px a character where no canvas is.
let ctx: CanvasRenderingContext2D | null | undefined
function textWidth(t: string): number {
  if (ctx === undefined) {
    try {
      ctx = document.createElement('canvas').getContext('2d')
      if (ctx) ctx.font = LABEL_FONT
    } catch {
      ctx = null
    }
  }
  return ctx ? ctx.measureText(t).width : t.length * 7
}
const LABEL_W = computed(() => Math.min(LABEL_CAP, Math.max(LABEL_MIN, ...tl.value.rows.map((r) => Math.ceil(textWidth(r.name)) + LABEL_PAD))))
/** A name cut at its end to fit the column ("backfill-and-reconcil…"); the whole when it fits. */
function fitName(name: string): string {
  const room = LABEL_W.value - LABEL_PAD
  if (textWidth(name) <= room) return name
  let lo = 0
  let hi = name.length
  while (lo < hi) {
    const mid = Math.ceil((lo + hi) / 2)
    if (textWidth(`${name.slice(0, mid)}…`) <= room) lo = mid
    else hi = mid - 1
  }
  return `${name.slice(0, lo)}…`
}
const plotW = computed(() => Math.max(80, width.value - LABEL_W.value - PAD_R))
const height = computed(() => HEAD_H + tl.value.rows.length * ROW_H + 8)
const x = (t: number) => LABEL_W.value + scaleX(t, tl.value.from, tl.value.to, plotW.value)
const rowY = (i: number) => HEAD_H + i * ROW_H
const rowIndex = (name: string) => tl.value.rows.findIndex((r) => r.name === name)
const axis = computed(() => ticks(tl.value.from, tl.value.to).map((t) => ({ t, x: x(t), label: tickLabel(t, tl.value.to - tl.value.from) })))
const started = computed(() => tl.value.rows.some((r) => r.start !== undefined))

const time = (t: number) => clockTime(t, { seconds: true })

function barTitle(r: (typeof tl.value.rows)[number]): string {
  if (r.start === undefined) return `${r.name}: not started`
  const end = r.end ?? props.now
  return `${r.name}: ${time(r.start)} → ${r.end === undefined ? 'now' : time(r.end)} (${durationLabel(end - r.start)})${r.error ? ` · ${r.error}` : ''}`
}

const barFill = (status: string) => (status === 'running' || status === 'starting' ? 'var(--ui-success)' : 'var(--ui-text-dimmed)')
</script>

<template>
  <div ref="host" class="flex min-w-0 flex-col gap-2" data-run-timeline>
    <div class="flex flex-wrap items-center gap-3 text-[11px] text-muted">
      <span class="flex items-center gap-1"><span class="inline-block h-2 w-4 rounded-sm" style="background: var(--ui-success)" />running</span>
      <span class="flex items-center gap-1"><span class="inline-block h-2 w-4 rounded-sm" style="background: var(--ui-warning)" />waiting for input</span>
      <span class="flex items-center gap-1"><span class="inline-block h-2 w-4 rounded-sm" style="background: var(--ui-text-dimmed)" />ended</span>
      <span class="flex items-center gap-1"><span class="inline-block size-2 rotate-45 rounded-[1px]" style="background: var(--ui-info)" />handoff</span>
      <span v-if="tl.stoppedAt" class="flex items-center gap-1"><span class="inline-block h-3 w-0 border-l border-dashed" style="border-color: var(--ui-error)" />stopped</span>
    </div>
    <p v-if="!started" class="rounded-md border border-dashed border-accented p-6 text-center text-sm text-muted" data-timeline-empty>Nothing has started yet: the bars appear with the first member.</p>
    <svg v-else :width="width" :height="height" :viewBox="`0 0 ${width} ${height}`" class="max-w-full font-mono text-[10px]" role="img" :aria-label="`Timeline of ${run.name}`">
      <!-- The time axis. -->
      <g v-for="tk in axis" :key="tk.t">
        <line :x1="tk.x" :x2="tk.x" :y1="HEAD_H - 4" :y2="height - 8" stroke="var(--ui-border)" stroke-width="1" />
        <text :x="tk.x" :y="HEAD_H - 8" text-anchor="middle" fill="var(--ui-text-muted)">{{ tk.label }}</text>
      </g>
      <!-- One row per member. -->
      <g v-for="(r, i) in tl.rows" :key="r.name" :data-timeline-row="r.name" :data-status="r.status" :transform="`translate(0, ${rowY(i)})`">
        <text :x="0" :y="ROW_H / 2 + 3" text-anchor="start" fill="var(--ui-text)" class="font-sans text-xs" :data-timeline-name="r.name">
          <title>{{ barTitle(r) }}</title>
          {{ fitName(r.name) }}
        </text>
        <template v-if="r.start !== undefined">
          <rect :x="x(r.start)" :y="ROW_H / 2 - 7" :width="Math.max(2, x(r.end ?? now) - x(r.start))" height="14" rx="3" :fill="barFill(r.status)" :stroke="r.error ? 'var(--ui-error)' : 'none'" stroke-width="1.5" data-timeline-bar>
            <title>{{ barTitle(r) }}</title>
          </rect>
          <rect
            v-for="(w, wi) in r.waits"
            :key="wi"
            :x="x(w.from)"
            :y="ROW_H / 2 - 7"
            :width="Math.max(2, x(w.to) - x(w.from))"
            height="14"
            rx="3"
            fill="var(--ui-warning)"
            data-timeline-wait
            :data-open="w.open"
          >
            <title>{{ r.name }} waited for input: {{ time(w.from) }} → {{ w.open ? 'now' : time(w.to) }} ({{ durationLabel(w.to - w.from) }}){{ w.reason ? ` · ${w.reason}` : '' }}</title>
          </rect>
          <g v-for="(h, hi) in r.handoffs" :key="hi" data-timeline-handoff :data-to="h.to">
            <line
              v-if="rowIndex(h.to) >= 0"
              :x1="x(h.at)"
              :x2="x(h.at)"
              :y1="ROW_H / 2"
              :y2="(rowIndex(h.to) - i) * ROW_H + ROW_H / 2"
              stroke="var(--ui-info)"
              stroke-width="1"
              stroke-dasharray="3 2"
            />
            <rect :x="x(h.at) - 4" :y="ROW_H / 2 - 4" width="8" height="8" rx="1" fill="var(--ui-info)" :transform="`rotate(45 ${x(h.at)} ${ROW_H / 2})`">
              <title>handoff {{ r.name }} → {{ h.to }} at {{ time(h.at) }}{{ h.message ? `: ${h.message}` : '' }}</title>
            </rect>
          </g>
        </template>
        <text v-else :x="LABEL_W + 4" :y="ROW_H / 2 + 3" fill="var(--ui-text-dimmed)">{{ r.status === 'pending' ? 'not started' : r.status }}</text>
      </g>
      <!-- The stopped line. -->
      <g v-if="tl.stoppedAt" data-timeline-stop>
        <line :x1="x(tl.stoppedAt)" :x2="x(tl.stoppedAt)" :y1="HEAD_H - 4" :y2="height - 8" stroke="var(--ui-error)" stroke-width="1" stroke-dasharray="4 3" />
        <text :x="x(tl.stoppedAt) + 3" :y="height - 2" fill="var(--ui-error)">stopped {{ time(tl.stoppedAt) }}</text>
      </g>
    </svg>
  </div>
</template>
