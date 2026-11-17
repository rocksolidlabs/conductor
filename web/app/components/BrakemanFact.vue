<script setup lang="ts">
import type { RefResolution } from '~/utils/brakeman'

/**
 * A brief's fact: its number among the facts, its text with the evidence it rests on as chips (BrakemanRefChip) after it, and at its
 * right what the default slot holds (Keep as a note).
 */
defineProps<{ number: number; text: string; evidence: readonly string[]; refs: ReadonlyMap<string, RefResolution> }>()
</script>

<template>
  <div class="flex items-start gap-2" data-brief-fact>
    <span class="w-4 flex-none pt-px text-sm font-semibold text-muted" data-fact-number>{{ number }}</span>
    <div class="flex min-w-0 flex-1 flex-col gap-1.5">
      <p class="break-words text-sm">{{ text }}</p>
      <div v-if="evidence.length" class="flex flex-wrap gap-1.5" data-fact-evidence>
        <BrakemanRefChip v-for="r in evidence" :key="r" :ref-name="r" :resolution="refs.get(r)" />
      </div>
    </div>
    <slot />
  </div>
</template>
