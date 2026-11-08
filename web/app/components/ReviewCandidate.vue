<script setup lang="ts">
import { shortCommit } from '~/utils/dispatchPlans'
import type { BaseChoice, ReviewCandidateOption } from '~/utils/review'

/**
 * The candidate reviewed and the base its diff is read against (Program item RV1, PR-6): a picker of the ticket's candidates, newest
 * first per task, each with its commit and whether it stands as its task's result; and a picker of the bases the server takes (the
 * run's base, where the attempt started, another result of the task). The base is the draft's: it is locked while the draft holds a
 * note on the base side, and says why. How each candidate came to be (its lineage) is shown by a later part of this view.
 */
defineProps<{
  options: ReviewCandidateOption[]
  candidate: string
  bases: BaseChoice[] | null
  base: string
  /** Why the base cannot move now, in words: '' when it can. */
  locked: string
}>()
const emit = defineEmits<{ candidate: [ref: string]; base: [commit: string] }>()
</script>

<template>
  <div class="flex flex-col gap-2 sm:flex-row sm:flex-wrap sm:items-end" data-review-candidate-picker>
    <UFormField label="Candidate" size="sm" class="min-w-0 sm:w-96">
      <USelect
        :model-value="candidate"
        :items="options.map((o) => ({ label: `${o.label}${o.standing ? ' · result' : ''}`, value: o.value }))"
        size="sm"
        class="w-full"
        data-review-candidate
        @update:model-value="(v: unknown) => emit('candidate', String(v))"
      />
    </UFormField>
    <UFormField label="Compared with" size="sm" class="min-w-0 sm:w-96">
      <USelect
        v-if="bases"
        :model-value="base"
        :items="bases.map((b) => ({ label: `${shortCommit(b.commit)} · ${b.label}`, value: b.commit }))"
        :disabled="!!locked"
        size="sm"
        class="w-full"
        data-review-base
        :data-review-base-commit="base"
        @update:model-value="(v: unknown) => emit('base', String(v))"
      />
      <span v-else class="text-xs text-dimmed">Reading the bases…</span>
    </UFormField>
    <p v-if="locked" class="flex items-center gap-1.5 text-xs text-muted sm:basis-full" data-review-base-locked>
      <UIcon name="i-lucide-lock" class="size-3.5 flex-none" />{{ locked }}
    </p>
  </div>
</template>
