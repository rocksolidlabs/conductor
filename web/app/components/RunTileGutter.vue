<script setup lang="ts">
/**
 * The gap between two tracks of a run's wall, which a person drags to resize them (a separator, focusable). A drag draws a line that
 * follows the pointer and hands the move over only when the pointer is let go, so each terminal is fitted once a drag; a drag the
 * pointer loses (a cancel, the capture lost, the window's blur) ends with nothing changed. Left and Right, or Up and Down, move it by
 * a step; Home or a double-click make the two even. The wall places it and says how far it may move.
 */
const props = defineProps<{
  /** `x`: between two columns (a vertical line); `y`: between two rows. */
  axis: 'x' | 'y'
  /** Where the gap starts along its axis, and how long the line is across, in px of the wall's content. */
  at: number
  length: number
  /** The gap's width, in px. */
  gap: number
  /** How far it may move each way, in px (negative, positive). */
  min: number
  max: number
  /** The first track's share of the two, in percent, for its value. */
  value: number
  label: string
}>()

const emit = defineEmits<{ move: [px: number]; step: [sign: 1 | -1]; even: [] }>()

const dragging = ref(false)
const offset = ref(0)
let start = 0
let pointer = -1

function clamp(v: number) {
  return Math.min(props.max, Math.max(props.min, v))
}
function down(e: PointerEvent) {
  if (e.button !== 0) return
  e.preventDefault()
  ;(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId)
  pointer = e.pointerId
  start = props.axis === 'x' ? e.clientX : e.clientY
  offset.value = 0
  dragging.value = true
  window.addEventListener('blur', cancel)
}
function move(e: PointerEvent) {
  if (!dragging.value || e.pointerId !== pointer) return
  offset.value = clamp((props.axis === 'x' ? e.clientX : e.clientY) - start)
}
function end() {
  dragging.value = false
  pointer = -1
  window.removeEventListener('blur', cancel)
}
function up(e: PointerEvent) {
  if (!dragging.value || e.pointerId !== pointer) return
  const moved = offset.value
  end()
  offset.value = 0
  if (Math.abs(moved) >= 1) emit('move', moved)
}
/** The drag ends with nothing changed. */
function cancel() {
  if (!dragging.value) return
  end()
  offset.value = 0
}
onBeforeUnmount(() => window.removeEventListener('blur', cancel))

function key(e: KeyboardEvent) {
  const back = props.axis === 'x' ? 'ArrowLeft' : 'ArrowUp'
  const on = props.axis === 'x' ? 'ArrowRight' : 'ArrowDown'
  if (e.key === back || e.key === on) {
    e.preventDefault()
    emit('step', e.key === on ? 1 : -1)
  } else if (e.key === 'Home') {
    e.preventDefault()
    emit('even')
  } else if (e.key === 'Escape' && dragging.value) {
    e.preventDefault()
    cancel()
  }
}

const style = computed(() => {
  const shift = `${props.at + offset.value}px`
  return props.axis === 'x'
    ? { left: shift, top: '0px', width: `${props.gap}px`, height: `${props.length}px` }
    : { top: shift, left: '0px', height: `${props.gap}px`, width: `${props.length}px` }
})
</script>

<template>
  <div
    class="group/gutter absolute z-10 flex touch-none items-center justify-center outline-none select-none"
    :class="props.axis === 'x' ? 'cursor-col-resize' : 'cursor-row-resize flex-col'"
    :style="style"
    role="separator"
    tabindex="0"
    :aria-orientation="props.axis === 'x' ? 'vertical' : 'horizontal'"
    :aria-valuenow="Math.round(props.value)"
    aria-valuemin="0"
    aria-valuemax="100"
    :aria-label="props.label"
    :data-run-gutter="props.axis"
    :data-dragging="dragging || undefined"
    @pointerdown="down"
    @pointermove="move"
    @pointerup="up"
    @pointercancel="cancel"
    @lostpointercapture="cancel"
    @dblclick="emit('even')"
    @keydown="key"
  >
    <span
      class="rounded-full transition-colors group-hover/gutter:bg-accented group-focus-visible/gutter:bg-primary"
      :class="[props.axis === 'x' ? 'h-full w-0.5' : 'h-0.5 w-full', dragging ? 'bg-primary' : 'bg-transparent']"
      aria-hidden="true"
    />
  </div>
</template>
