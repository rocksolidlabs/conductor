<script setup lang="ts">
/**
 * A labelled button whose tooltip says what pressing it does (#374: the welcome's choices, Crews' Load the examples). The label stays
 * the button's name; the tooltip opens as an IconButton's does (useTipOpen): while a mouse rests on the button and when Tab brings the
 * focus to it, never for a focus the app hands back and never at a touch, where the label alone is shown. An empty `tip` draws the
 * button with no tooltip. Every other attribute and every slot goes to the UButton.
 */
defineOptions({ inheritAttrs: false })

defineProps<{
  /** What happens when the button is pressed, in a sentence: the tooltip's text. */
  tip: string
  /** Where the tooltip opens; below the button unless said. */
  side?: 'top' | 'right' | 'bottom' | 'left'
}>()

const { open, onPointer, onOpen } = useTipOpen()

/**
 * A sentence wraps inside a bounded box, never cut to one line as a tooltip's few words are. The box is taller than a button and can
 * lie over the next one (the welcome's agents, one under another): it closes as the pointer leaves its button
 * (`disable-hoverable-content`: there is nothing in it to reach) and is gone at once, with no closing animation to keep it over the
 * next button for a moment, so a press on the button below lands on that button.
 */
const ui = { content: 'h-auto max-w-72 data-[state=closed]:animate-none', text: 'text-clip whitespace-normal' }
</script>

<template>
  <UTooltip :open="open" :text="tip" :disabled="!tip" :content="side ? { side } : undefined" :ui="ui" ignore-non-keyboard-focus disable-hoverable-content @update:open="onOpen">
    <UButton v-bind="$attrs" data-tip-button @pointerenter="onPointer($event, true)" @pointerleave="onPointer($event, false)">
      <template v-for="(_, name) in $slots" #[name]="slotProps">
        <slot :name="name" v-bind="slotProps ?? {}" />
      </template>
    </UButton>
  </UTooltip>
</template>
