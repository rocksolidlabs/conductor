<script setup lang="ts">
import { markLook, type MarkView } from '~/utils/status'

/**
 * A state's mark and, with `word`, its word (docs/design/system.md, Status): the one way every surface draws a session's, a member's or
 * a task's state. The mark is 8 px, drawn from STATUS_LOOK and TASK_LOOK alone (markLook: its shape and tone; the line diagram's station
 * draws the same table as circles and rings), and carries the colour; the word is drawn in muted text, never in a status colour. The mark
 * is an image named by the word, with the legend as its title. `quiet` hides the amber dot (the Events page's Badge route for needs input
 * is off), the word stays; `badge` sets the mark on a disc of the surface with a 2 px ring, for a corner over an avatar.
 *
 * `svg` is the second form (the line diagram's station, UX1 PR-33): the same glyph drawn inside a parent SVG, centred on `x`, `y` at
 * the radius `r`, so a station is the same mark and never a sixth. It names nothing itself: the station that holds it carries the
 * state's key, its name and its title.
 */
const props = withDefaults(
  defineProps<{ status: Pick<MarkView, 'key' | 'word' | 'legend'>; word?: boolean; quiet?: boolean; badge?: boolean; svg?: boolean; x?: number; y?: number; r?: number }>(),
  { word: false, quiet: false, badge: false, svg: false, x: 0, y: 0, r: 4 },
)

const look = computed(() => markLook(props.status.key))
// The mark is a dot or a ring: drawn in the status fill colours (the dots' and fills', not the text-safe tones `text-warning` and
// `text-success` draw words with in the light theme, main.css).
const tone: Record<string, string> = { warning: 'text-(--ui-warning)', success: 'text-(--ui-success)', error: 'text-(--ui-error)', neutral: 'text-muted' }
const hidden = computed(() => props.quiet && props.status.key === 'needs')
/** The title: the legend, after the word where the legend does not begin with it ("finished 12m ago · finished: …"). */
const title = computed(() => (props.status.legend.startsWith(`${props.status.word}:`) ? props.status.legend : `${props.status.word} · ${props.status.legend}`))
</script>

<template>
  <component
    :is="svg ? 'g' : 'span'"
    :class="!svg && 'inline-flex min-w-0 flex-none items-center gap-1.5'"
    :role="svg ? undefined : 'img'"
    :aria-label="svg ? undefined : status.word"
    :title="svg ? undefined : title"
    :data-status-mark="svg ? undefined : status.key"
    :data-status-quiet="!svg && hidden ? '' : undefined"
  >
    <svg
      viewBox="0 0 8 8"
      class="flex-none overflow-visible"
      :class="[tone[look.tone], !svg && 'size-2', badge && 'rounded-full bg-default ring-2 ring-default', hidden && 'invisible']"
      :x="svg ? x - r : undefined"
      :y="svg ? y - r : undefined"
      :width="svg ? 2 * r : undefined"
      :height="svg ? 2 * r : undefined"
      aria-hidden="true"
      data-status-glyph
      :data-status-shape="look.shape"
    >
      <circle v-if="look.shape === 'dot'" cx="4" cy="4" r="4" fill="currentColor" />
      <template v-else-if="look.shape === 'ringed-dot'">
        <circle cx="4" cy="4" r="3.5" fill="none" stroke="currentColor" stroke-width="1" />
        <circle cx="4" cy="4" r="2" fill="currentColor" />
      </template>
      <circle v-else-if="look.shape === 'ring'" cx="4" cy="4" r="3.25" fill="none" stroke="currentColor" stroke-width="1.5" />
      <circle v-else-if="look.shape === 'dashed-ring'" cx="4" cy="4" r="3.25" fill="none" stroke="currentColor" stroke-width="1.5" stroke-dasharray="2.2 1.2" />
      <!-- A task's own: a hollow square (waiting), a check (done), a cross (failed), a dash (cancelled). -->
      <rect v-else-if="look.shape === 'square'" x="0.75" y="0.75" width="6.5" height="6.5" rx="1" fill="none" stroke="currentColor" stroke-width="1.5" />
      <path v-else-if="look.shape === 'check'" d="M0.8 4.3 L3 6.4 L7.2 1.6" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" />
      <path v-else-if="look.shape === 'cross'" d="M1.2 1.2 L6.8 6.8 M6.8 1.2 L1.2 6.8" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" />
      <path v-else d="M1 4 L7 4" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" />
    </svg>
    <span v-if="word && !svg" class="truncate text-muted" data-status-word>{{ status.word }}</span>
  </component>
</template>
