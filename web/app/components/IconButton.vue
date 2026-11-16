<script lang="ts">
/**
 * The key last pressed anywhere on the page, cleared by any press of a pointer: a focus that follows a Tab is a person moving through
 * the page by the keyboard; any other focus that lands on a button (a menu or a dialog handing it back as it closes, after an Escape,
 * an Enter or a tap) is the app's own, and opens no tooltip.
 */
let lastKey = ''
if (typeof window !== 'undefined') {
  window.addEventListener('keydown', (e) => (lastKey = e.key), true)
  window.addEventListener('pointerdown', () => (lastKey = ''), true)
}
</script>

<script setup lang="ts">
/**
 * A button drawn as its icon alone (docs/design/system.md, Tooltips): its label is its tooltip and its accessible name. The tooltip
 * shows while a mouse rests on the button and when Tab brings the focus to it; never for a focus the app puts back on the button
 * when a menu or a dialog it opened closes, and never at a touch. Every other attribute (icon, size, colour, variant, a click, a
 * link's `to`, a menu's trigger props) goes to the UButton; the default slot is drawn inside it, for a label shown only at some
 * widths or a count beside the icon.
 */
defineOptions({ inheritAttrs: false })

defineProps<{
  /** What the button does, in a few words: the tooltip's text and the button's aria-label. */
  label: string
  /** The key that does the same, shown in the tooltip ('N', or ['meta', 'B']). */
  kbd?: string | string[]
  /** Where the tooltip opens; below the button unless said. */
  side?: 'top' | 'right' | 'bottom' | 'left'
}>()

const open = ref(false)
/** A mouse or a pen rests on the button (a touch never counts: it has no hover). */
const hovered = ref(false)

function onPointer(e: PointerEvent, over: boolean) {
  if (e.pointerType !== 'touch') hovered.value = over
}

/** The tooltip asks to open on hover or on focus: it opens for a hover, and for a focus Tab brought; it closes whenever asked. */
function onOpen(want: boolean) {
  open.value = want && (hovered.value || lastKey === 'Tab')
}
</script>

<template>
  <UTooltip
    :open="open"
    :text="label"
    :kbds="kbd === undefined ? undefined : Array.isArray(kbd) ? kbd : [kbd]"
    :content="side ? { side } : undefined"
    ignore-non-keyboard-focus
    @update:open="onOpen"
  >
    <UButton v-bind="$attrs" :aria-label="label" data-icon-button @pointerenter="onPointer($event, true)" @pointerleave="onPointer($event, false)">
      <template v-if="$slots.default" #default>
        <slot />
      </template>
    </UButton>
  </UTooltip>
</template>
