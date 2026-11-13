<script setup lang="ts">
import type { DecideCard } from '~/composables/useDecide'
import { decideOutcomeWords } from '~/utils/decisions'

/**
 * One decision on the decide page (docs/plans/decide-grant.md, "web/app"), drawn from its decide view alone: the question, the context,
 * the options as the view gives them (an assignment's Done and Not done are the server's) with their tradeoffs and the recommended one,
 * the uncertainty and what an answer authorizes; the answer as its type asks; and, each above the card, what became of it. The draft
 * stays as typed through every refusal; while an answer's outcome is not known the form is held, and Send again sends that answer.
 */
const props = defineProps<{ card: DecideCard; answerable: boolean; closable: boolean; ended: boolean }>()
defineEmits<{ send: []; close: []; dismiss: [] }>()

const v = computed(() => props.card.view)
const options = computed(() => (v.value?.options ?? []).map((o, i) => ({ value: i, label: o.label, description: o.tradeoffs })))
const recommended = computed(() => v.value?.options?.[v.value.recommendation ?? -1]?.label ?? '')
const ready = computed(() => props.card.draft.choice !== undefined || (v.value?.type === 'clarification' && !!props.card.draft.text.trim()))
const locked = computed(() => !props.answerable || !!props.card.sent)
const outcome = computed(() => (v.value ? decideOutcomeWords(v.value) : ''))
const asked = computed(() => {
  const t = Date.parse(v.value?.createdAt ?? '')
  return Number.isFinite(t) ? new Date(t).toLocaleString() : ''
})
/** The type, drawn only when it is one the protocol names. */
const type = computed(() => (['clarification', 'approval', 'assignment'].includes(v.value?.type ?? '') ? v.value!.type : ''))
</script>

<template>
  <article class="flex min-w-0 flex-col gap-3 rounded-lg border border-default bg-default p-4 text-sm" data-decide-card :data-decision-id="card.id">
    <header class="flex min-w-0 flex-wrap items-center gap-2">
      <span class="font-mono font-semibold text-highlighted">{{ card.id }}</span>
      <UBadge v-if="type" :label="type" color="neutral" variant="subtle" size="sm" />
      <span class="flex-1" />
      <UButton v-if="closable" label="Close" color="neutral" variant="ghost" size="sm" class="min-h-11" data-decide-close @click="$emit('close')" />
      <UButton v-if="card.end" label="Dismiss" color="neutral" variant="ghost" size="sm" class="min-h-11" data-decide-dismiss @click="$emit('dismiss')" />
    </header>

    <UAlert v-if="card.end === 'gone'" color="warning" variant="subtle" icon="i-lucide-circle-slash" :title="`${card.id} is no longer open to you.`" data-decide-gone />
    <UAlert v-else-if="card.end === 'refused'" color="warning" variant="subtle" icon="i-lucide-circle-slash" :title="`${card.id} cannot be read on this server.`" data-decide-read-refused />
    <UAlert v-else-if="card.suspended && v?.state === 'open' && !ended && !card.sent" color="neutral" variant="subtle" icon="i-lucide-loader-circle" :title="`Checking that ${card.id} is still yours to answer.`" data-decide-suspended />
    <UAlert v-if="card.unknown" color="warning" variant="subtle" icon="i-lucide-circle-help" :title="card.unknown" data-decide-unknown />
    <UAlert v-if="card.refused" color="error" variant="subtle" icon="i-lucide-circle-x" :title="card.refused" data-decide-refused />
    <UAlert v-if="outcome" :color="v?.outcome === 'applied' || v?.outcome === 'waiting' ? 'success' : 'neutral'" variant="subtle" icon="i-lucide-info" :title="outcome" data-decide-outcome />
    <UAlert v-if="!v && !card.end && !ended && card.failed" color="warning" variant="subtle" icon="i-lucide-loader-circle" :title="`${card.id} could not be read. Trying again.`" data-decide-read-failed />
    <p v-else-if="!v && !card.end && !ended" class="flex items-center gap-2 text-muted"><UIcon name="i-lucide-loader-circle" class="size-4 animate-spin" /> Reading {{ card.id }}…</p>

    <template v-if="v?.question">
      <p class="break-words text-base font-medium text-highlighted" data-decide-question>{{ v.question }}</p>
      <p v-if="asked" class="text-xs text-muted">Asked {{ asked }}</p>
      <p v-if="v.context" class="whitespace-pre-wrap break-words" data-decide-context>{{ v.context }}</p>
      <dl class="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-xs">
        <template v-if="recommended">
          <dt class="text-muted">Recommended</dt>
          <dd class="min-w-0 [overflow-wrap:anywhere]" data-decide-recommended>{{ recommended }}</dd>
        </template>
        <template v-if="v.uncertainty">
          <dt class="text-muted">Uncertainty</dt>
          <dd class="min-w-0 [overflow-wrap:anywhere]">{{ v.uncertainty }}</dd>
        </template>
        <template v-if="v.authorizes">
          <dt class="text-muted">An answer authorizes</dt>
          <dd class="min-w-0 [overflow-wrap:anywhere]">{{ v.authorizes }}</dd>
        </template>
      </dl>
    </template>

    <!-- An answer held whose outcome is not known keeps its form, whatever a read has shown of the decision since; Send again is offered as useDecide's answerable says. -->
    <form v-if="v?.state === 'open' || card.sent" class="flex min-w-0 flex-col gap-3" data-decide-form @submit.prevent="$emit('send')">
      <URadioGroup v-if="options.length" v-model="card.draft.choice" :items="options" :disabled="locked" legend="Your choice" :ui="{ root: 'min-w-0', fieldset: 'min-w-0', item: 'min-w-0', wrapper: 'min-w-0', label: '[overflow-wrap:anywhere]', description: '[overflow-wrap:anywhere]' }" data-decide-options />
      <UFormField v-if="v?.type === 'clarification'" label="Your answer">
        <UTextarea v-model="card.draft.text" :disabled="locked" :maxlength="4096" autoresize class="w-full" data-decide-text />
      </UFormField>
      <UFormField label="Why" hint="optional">
        <UTextarea v-model="card.draft.rationale" :disabled="locked" :maxlength="2000" autoresize class="w-full" data-decide-rationale />
      </UFormField>
      <UButton type="submit" :label="card.sending ? 'Sending' : card.unknown ? 'Send again' : 'Send'" :loading="card.sending" :disabled="!answerable || (!card.sent && !ready)" class="min-h-11 self-start" data-decide-send />
    </form>
  </article>
</template>
