<script setup lang="ts">
import { candidateRef } from '~/utils/dispatch'
import { lineageWords, type Lineage } from '~/utils/reviewLineage'

/**
 * How the candidates of the task reviewed came to be (Program item RV1, PR-7), oldest first: which attempt repaired which review or
 * failed check, which checked a commit again, and in which continuation (utils/reviewLineage.ts). The one shown is marked; each opens
 * the view at it. It stops at the attempt: it never says who wrote a line.
 */
defineProps<{ lineage: readonly Lineage[]; candidate: string }>()
const emit = defineEmits<{ candidate: [ref: string] }>()
</script>

<template>
  <ol v-if="lineage.length" class="flex flex-col gap-0.5 text-xs" data-review-lineage :data-review-lineage-count="lineage.length">
    <li v-for="l in lineage" :key="`${l.run}/${l.attempt}`" class="flex min-w-0 items-start gap-1.5" data-review-lineage-step :data-lineage-cause="l.cause.kind" :data-lineage-attempt="l.attempt" :data-lineage-run="l.run">
      <UIcon :name="l.cause.kind === 'recheck' ? 'i-lucide-refresh-cw' : l.cause.kind === 'first' ? 'i-lucide-git-commit-horizontal' : 'i-lucide-wrench'" class="mt-0.5 size-3.5 flex-none text-muted" />
      <button
        type="button"
        class="min-w-0 break-words text-left hover:underline"
        :class="candidateRef(l) === candidate ? 'font-medium text-highlighted' : 'text-muted'"
        :aria-current="candidateRef(l) === candidate ? 'true' : undefined"
        data-lineage-words
        @click="emit('candidate', candidateRef(l))"
      >
        {{ lineageWords(l) }}
      </button>
    </li>
  </ol>
</template>
