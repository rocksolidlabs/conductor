<script setup lang="ts">
import type { ChatMessage } from '~/utils/protocol'
import type { TileAsk } from '~/utils/tileAsk'

/**
 * The bar of a run's tile whose member asks (tileAskOf): who asks and the question whole, wrapping, never cut, then what answers it:
 * its choices, a press answered through the page's `answer` (the run chat's own path); else where it is answered, a line or the
 * button that goes there (the decision's card, the member's own view).
 */
const props = defineProps<{ ask: TileAsk; answer: (m: ChatMessage, index: number) => Promise<void> }>()
const emit = defineEmits<{ open: []; decision: [id: string] }>()
</script>

<template>
  <div class="flex flex-col gap-1.5" data-run-tile-ask :data-ask-kind="props.ask.kind">
    <p class="text-[13px] leading-snug text-default break-words" data-run-tile-question>
      <b class="font-semibold text-highlighted">{{ props.ask.who }}</b>
      {{ props.ask.question }}
    </p>
    <RunAskChoices v-if="props.ask.kind === 'choices'" :line="props.ask.line" :answer="props.answer" />
    <UButton
      v-else-if="props.ask.kind === 'decision'"
      :label="props.ask.words"
      size="xs"
      color="neutral"
      variant="outline"
      class="self-start max-sm:min-h-11"
      data-run-tile-answer="decision"
      @click="emit('decision', props.ask.decision)"
    />
    <UButton
      v-else-if="props.ask.kind === 'open'"
      :label="props.ask.words"
      size="xs"
      color="neutral"
      variant="outline"
      class="self-start max-sm:min-h-11"
      data-run-tile-answer="open"
      @click="emit('open')"
    />
    <p v-else class="text-xs text-muted" data-run-tile-answer="terminal">{{ props.ask.words }}</p>
  </div>
</template>
