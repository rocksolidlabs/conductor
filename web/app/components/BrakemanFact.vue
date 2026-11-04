<script setup lang="ts">
import type { RefResolution } from '~/utils/brakeman'

/** A brief's fact: its number among the facts, its text, and the evidence it rests on as chips (BrakemanRefChip). */
defineProps<{ number: number; text: string; evidence: readonly string[]; refs: ReadonlyMap<string, RefResolution> }>()
</script>

<template>
  <div class="flex flex-col gap-1.5" data-brief-fact>
    <p class="text-sm"><span class="mr-1 font-semibold text-muted">{{ number }}.</span>{{ text }}</p>
    <div class="flex flex-wrap gap-1.5" data-fact-evidence>
      <BrakemanRefChip v-for="r in evidence" :key="r" :ref-name="r" :resolution="refs.get(r)" />
    </div>
  </div>
</template>
