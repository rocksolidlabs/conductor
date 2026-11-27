<script setup lang="ts">
import type { ChatMessage } from '~/utils/protocol'
import type { TileAsk } from '~/utils/tileAsk'

/**
 * The bar of a run's tile whose member asks (tileAskOf): who asks and the question whole, wrapping, never cut, then its choices, a
 * press answered through the page's `answer` (the run chat's own path).
 */
const props = defineProps<{ ask: TileAsk; answer: (m: ChatMessage, index: number) => Promise<void> }>()
</script>

<template>
  <div class="flex flex-col gap-1.5" data-run-tile-ask>
    <p class="text-[13px] leading-snug text-default break-words" data-run-tile-question>
      <b class="font-semibold text-highlighted">{{ props.ask.who }}</b>
      {{ props.ask.question }}
    </p>
    <RunAskChoices :line="props.ask.line" :answer="props.answer" />
  </div>
</template>
