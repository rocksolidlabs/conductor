<script setup lang="ts">
import { MODE_CHOICE, choiceKey } from '~/utils/attention'
import type { ChatMessage } from '~/utils/protocol'

/**
 * A member's question's choices as buttons where a run is shown (C6: the Crews home's card, the run page's tile), drawn as the run's
 * chat draws them. A press is handed to the page, which answers through the run chat's own path (useRunChatAnswer: the choice's keys
 * into the member's running session, naming the question, which the session checks). A page that may not answer (`answerable`
 * false: a view link's holder, a stopped run) is never given this component.
 */
const props = defineProps<{ line: ChatMessage; answer: (m: ChatMessage, index: number) => Promise<void> }>()

const sending = ref(-1)
async function press(i: number) {
  if (sending.value >= 0) return
  sending.value = i
  try {
    await props.answer(props.line, i)
  } finally {
    sending.value = -1
  }
}
</script>

<template>
  <div class="flex flex-wrap gap-1.5" data-run-ask-choices :data-question="line.id">
    <UButton
      v-for="(o, i) in line.options"
      :key="i"
      :label="o.label"
      size="xs"
      :color="o.mode ? 'warning' : 'neutral'"
      variant="outline"
      class="max-sm:min-h-11"
      :title="o.mode ? MODE_CHOICE : undefined"
      :loading="sending === i"
      :disabled="sending >= 0 && sending !== i"
      :data-run-ask-choice="i + 1"
      :data-choice-mode="o.mode || undefined"
      @click="press(i)"
    >
      <template v-if="choiceKey(o, i)" #leading><UKbd :value="String(choiceKey(o, i))" size="sm" class="opacity-70" /></template>
    </UButton>
  </div>
</template>
