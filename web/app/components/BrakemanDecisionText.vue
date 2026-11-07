<script setup lang="ts">
/**
 * The text of a Brakeman decision card (docs/plans/brakeman.md, PR-7): what happened, why a person is needed, who answers, what
 * answering authorizes, the alternatives considered with Brakeman's recommendation, and its uncertainty. Text only: the controls are
 * Conductor's, from the live record, wherever the card is shown. The Brakeman page's card (BrakemanDecision) and the decision page's
 * item (InboxItem, docs/plans/decision-inbox.md) both draw it, so the two show the same card.
 */
const props = defineProps<{
  happened: string
  whyHuman?: string
  answerer: string
  authorizes?: string
  alternatives: ReadonlyArray<{ label: string; tradeoffs: string }>
  /** The index of the alternative Brakeman recommends; -1 (or none) for none. */
  recommended?: number
  uncertainty?: string
}>()
const rec = computed(() => (typeof props.recommended === 'number' && props.recommended >= 0 ? props.recommended : -1))
</script>

<template>
  <dl class="grid gap-x-3 gap-y-1 text-sm sm:grid-cols-[max-content_1fr]">
    <dt class="text-muted">What happened</dt>
    <dd data-decision-happened>{{ happened }}</dd>
    <dt class="text-muted">Why a person</dt>
    <dd>{{ whyHuman }}</dd>
    <dt class="text-muted">Who answers</dt>
    <dd data-decision-answerer>{{ answerer }}</dd>
    <dt class="text-muted">Answering authorizes</dt>
    <dd data-decision-authorizes>{{ authorizes }}</dd>
  </dl>
  <ol class="flex flex-col gap-1.5" data-decision-alternatives>
    <li v-for="(a, i) in alternatives" :key="i" class="rounded-md bg-elevated/40 px-2 py-1.5 text-sm" :data-alternative="i">
      <span class="font-medium">{{ a.label }}</span>
      <UBadge v-if="rec === i" label="Brakeman recommends" color="primary" variant="subtle" size="sm" class="ml-2" data-recommended />
      <p class="text-xs text-muted">{{ a.tradeoffs }}</p>
    </li>
  </ol>
  <p v-if="rec < 0" class="text-xs text-muted">Brakeman recommends none of them.</p>
  <p v-if="uncertainty" class="text-xs text-muted">Uncertain: {{ uncertainty }}</p>
</template>
