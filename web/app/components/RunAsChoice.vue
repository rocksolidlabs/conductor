<script setup lang="ts">
import type { SessionWay } from '~/utils/protocol'
import { RUN_AS_CHAT_LINE, RUN_AS_SIDES, sideForKey } from '~/utils/launchWay'

/**
 * The Launch agent dialog's Run as: Terminal | Chat (issue 1174), in the order and shape of the header's switch (WaySwitch.vue).
 * One press picks a side, and the arrow keys move between the two; while Chat is picked one line says what a launch as a chat does.
 */
const way = defineModel<SessionWay>({ default: 'terminal' })
const group = ref<HTMLElement>()

function pick(next: SessionWay) {
  way.value = next
}

function onKey(e: KeyboardEvent) {
  const next = sideForKey(way.value, e.key)
  if (!next) return
  e.preventDefault()
  pick(next)
  void nextTick(() => group.value?.querySelector<HTMLElement>(`[data-run-as-side="${next}"]`)?.focus())
}
</script>

<template>
  <UFormField label="Run as" name="runAs" data-launch-run-as>
    <div ref="group" class="flex" role="radiogroup" aria-label="Run as" :data-run-as="way" @keydown="onKey">
      <UFieldGroup size="sm">
        <UButton
          v-for="side in RUN_AS_SIDES"
          :key="side.way"
          type="button"
          :label="side.label"
          color="neutral"
          :variant="side.way === way ? 'soft' : 'ghost'"
          role="radio"
          :aria-checked="side.way === way"
          :tabindex="side.way === way ? 0 : -1"
          :data-run-as-side="side.way"
          @click="pick(side.way)"
        />
      </UFieldGroup>
    </div>
    <p v-if="way === 'protocol'" class="mt-1.5 text-xs text-muted" data-run-as-line>{{ RUN_AS_CHAT_LINE }}</p>
  </UFormField>
</template>
