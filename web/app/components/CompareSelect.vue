<script setup lang="ts">
import type { Comparison } from '~/utils/compare'
import { acceptedAnswer, decisionWords } from '~/utils/decisions'
import { selectionAnswerable, selectionFor, selectionPanelWords, selectionReview } from '~/utils/compareSelect'

/**
 * The selection of a comparison (Program item CP1, docs/plans/compare-crew.md, PR-7): DR1's record of origin `compare` that the
 * comparison names (`selection.decision`), read whole and answered through DR1's own DecisionRespond, so the panel, the card, the
 * ticket's Decisions, Needs you and a phone are one act: the candidates and None of these, nothing preselected, the rationale
 * required, the confirm saying final and naming the candidate. Its state is DR1's words ("Open: waits for …", "Recorded: …",
 * "Applied: approach-b selected at 4b825dc"); a record closed because a lane changed says so, and is never answered here: a
 * new one opens when every lane has ended. Before there is a record the panel says when one opens. Once applied with a candidate, Review
 * the selected candidate opens the ticket's Review view on it (PR-8b).
 */
const props = defineProps<{ comparison: Comparison | null }>()

const sel = computed(() => props.comparison?.selection ?? null)
const record = useDecisionRecord(() => sel.value?.decision ?? '')
const d = computed(() => (record.decision.value && record.decision.value.id === sel.value?.decision ? record.decision.value : null))
const runHeld = useDecisionRun(() => d.value?.run ?? '')
const words = computed(() => (d.value ? decisionWords(d.value, { runHeld: runHeld.value, controls: true }) : null))
const panel = computed(() => selectionPanelWords(props.comparison, d.value))
const answerable = computed(() => selectionAnswerable(sel.value, d.value))
const answer = computed(() => (d.value ? acceptedAnswer(d.value) : null))
const forWho = computed(() => selectionFor(sel.value))
const review = computed(() => selectionReview(sel.value))
const tones = { warning: 'text-warning', muted: 'text-muted', error: 'text-error', default: 'text-default' }
</script>

<template>
  <section class="flex min-w-0 flex-col gap-3 rounded-lg border border-default p-3" data-compare-select :data-compare-selection="sel?.decision ?? ''" :data-selection-state="d?.state ?? sel?.state ?? ''">
    <header class="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-0.5">
      <h3 class="text-sm font-semibold text-highlighted">Select a candidate</h3>
      <span v-if="forWho" class="text-xs text-muted" data-compare-select-for>{{ forWho }}</span>
      <span v-if="words" class="min-w-0 break-words text-xs" :class="tones[words.tone]" data-compare-select-state>{{ words.state }}</span>
    </header>

    <p v-if="panel" class="break-words text-sm text-default" data-compare-select-words>{{ panel }}</p>
    <UAlert v-if="record.error.value && sel" color="neutral" variant="subtle" icon="i-lucide-cloud-alert" :title="record.error.value" data-compare-select-error>
      <template #actions><UButton label="Read again" size="xs" color="neutral" variant="outline" @click="record.read()" /></template>
    </UAlert>
    <p v-else-if="sel && !d" class="text-sm text-muted">Reading {{ sel.decision }}…</p>

    <template v-if="d">
      <p class="break-words text-sm font-medium text-highlighted" data-compare-select-question>{{ d.request.question }}</p>
      <dl v-if="answer && (answer.choice || answer.rationale)" class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 rounded-md bg-elevated/40 p-2 text-xs" data-compare-select-answer>
        <template v-if="answer.choice">
          <dt class="text-muted">Selected</dt>
          <dd class="font-medium wrap-anywhere" data-compare-select-choice>{{ answer.choice }}</dd>
        </template>
        <template v-if="answer.rationale">
          <dt class="text-muted">Why</dt>
          <dd class="whitespace-pre-wrap break-words" data-compare-select-rationale>{{ answer.rationale }}</dd>
        </template>
        <template v-if="answer.by">
          <dt class="text-muted">By</dt>
          <dd class="break-words">{{ answer.by }}</dd>
        </template>
      </dl>
      <DecisionRespond v-if="answerable" :decision="d" :begin="record.begin" @read="record.read()" />
      <UButton v-if="review" label="Review the selected candidate" icon="i-lucide-scan-eye" size="sm" color="neutral" variant="outline" class="self-start max-lg:min-h-11" :to="review" data-compare-review-selected />
    </template>
  </section>
</template>
