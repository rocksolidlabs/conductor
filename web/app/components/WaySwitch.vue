<script setup lang="ts">
import type { SessionWay } from '~/utils/protocol'
import { ENDS_TERMINAL_WORK, pressFor, switchState, type SwitchFacts } from '~/utils/switchState'

/**
 * The header's switch, Terminal | Chat (docs/plans/chat-or-terminal.md, design 11): the side the session runs is selected, the
 * other moves it (`move`, the page's call of POST /api/sessions/{id}/way), at once when the turn has ended, else at its end, with
 * one line saying which (`line` false: the page draws the line where it has room, from the same state). What a move to Chat at
 * rest ends is said on the Chat side itself, as its tooltip, before it is pressed. A link's viewer sees a label of the way, with no
 * action.
 */
const props = withDefaults(defineProps<{ facts: SwitchFacts; line?: boolean }>(), { line: true })
const emit = defineEmits<{ move: [to: SessionWay] }>()
const state = computed(() => switchState(props.facts))
const sides: Array<{ way: SessionWay; label: string }> = [
  { way: 'terminal', label: 'Terminal' },
  { way: 'protocol', label: 'Chat' },
]
/** The at-rest line goes with the Chat side, before a press; every other line is the switch's own. */
const atRest = computed(() => state.value.line === ENDS_TERMINAL_WORK)
function press(way: SessionWay) {
  const to = pressFor(state.value, way)
  if (to) emit('move', to)
}
</script>

<template>
  <div v-if="state.shown" class="flex min-w-0 items-center gap-2" data-way-switch :data-way="state.selected" :data-noted="state.noted || undefined">
    <UFieldGroup v-if="facts.canMove && !facts.crewChat" size="xs">
      <UTooltip v-for="side in sides" :key="side.way" :text="side.way === 'protocol' && atRest ? ENDS_TERMINAL_WORK : undefined" :disabled="!(side.way === 'protocol' && atRest)">
        <UButton
          :label="side.label"
          color="neutral"
          :variant="side.way === state.selected ? 'soft' : 'ghost'"
          :disabled="!pressFor(state, side.way) && side.way !== state.selected"
          :aria-pressed="side.way === state.selected"
          :aria-description="side.way === 'protocol' && atRest ? ENDS_TERMINAL_WORK : undefined"
          :data-way-side="side.way"
          @click="press(side.way)"
        />
      </UTooltip>
    </UFieldGroup>
    <UBadge v-else :label="state.selected === 'protocol' ? 'Chat' : 'Terminal'" color="neutral" variant="subtle" size="sm" data-way-label />
    <span v-if="line && state.line && !atRest" class="min-w-0 text-xs text-muted" role="status" data-way-line>{{ state.line }}</span>
  </div>
</template>
