<script setup lang="ts">
/**
 * A button drawn as its icon alone (docs/design/system.md, Tooltips): its label is its tooltip and its accessible name. The tooltip
 * shows while a mouse rests on the button and when Tab brings the focus to it; never for a focus the app puts back on the button
 * when a menu or a dialog it opened closes, and never at a touch (useTipOpen). Every other attribute (icon, size, colour, variant, a
 * click, a link's `to`, a menu's trigger props) goes to the UButton; the default slot is drawn inside it, for a label shown only at
 * some widths or a count beside the icon.
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

const { open, onPointer, onOpen } = useTipOpen()
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
