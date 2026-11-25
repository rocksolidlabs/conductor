<script setup lang="ts">
import type { SessionWay } from '~/utils/protocol'
import { pressFor, switchState, type SwitchFacts } from '~/utils/switchState'

/**
 * The header's switch, Chat | Terminal (docs/plans/chat-or-terminal.md, design 11): the side the session runs is selected, the
 * other moves it (`move`, the page's call of POST /api/sessions/{id}/way), at once when the turn has ended, else at its end, with
 * one line saying which. A link's viewer sees a label of the way, with no action. Not mounted on a page yet (row 7 mounts it).
 */
const props = defineProps<{ facts: SwitchFacts }>()
const emit = defineEmits<{ move: [to: SessionWay] }>()
const state = computed(() => switchState(props.facts))
const sides: Array<{ way: SessionWay; label: string }> = [
  { way: 'protocol', label: 'Chat' },
  { way: 'terminal', label: 'Terminal' },
]
function press(way: SessionWay) {
  const to = pressFor(state.value, way)
  if (to) emit('move', to)
}
</script>

<template>
  <div v-if="state.shown" class="flex min-w-0 items-center gap-2" data-way-switch :data-way="state.selected" :data-noted="state.noted || undefined">
    <UFieldGroup v-if="facts.canMove" size="xs">
      <UButton
        v-for="side in sides"
        :key="side.way"
        :label="side.label"
        color="neutral"
        :variant="side.way === state.selected ? 'soft' : 'ghost'"
        :disabled="!pressFor(state, side.way) && side.way !== state.selected"
        :aria-pressed="side.way === state.selected"
        :data-way-side="side.way"
        @click="press(side.way)"
      />
    </UFieldGroup>
    <UBadge v-else :label="state.selected === 'protocol' ? 'Chat' : 'Terminal'" color="neutral" variant="subtle" size="sm" data-way-label />
    <span v-if="state.line" class="min-w-0 text-xs text-muted" role="status" data-way-line>{{ state.line }}</span>
  </div>
</template>
