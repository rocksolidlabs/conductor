<script setup lang="ts">
import type { PlanDecision } from '~/composables/useDispatch'
import type { DecisionRow } from '~/utils/dispatch'
import type { Decision } from '~/utils/decisions'
import { decisionWords, requiredRecord } from '~/utils/decisions'
import { relativeTime } from '~/utils/sessions'

/**
 * A ticket's Decisions (Program item DS2, docs/plans/dispatch-evidence.md, "web/app", PR-12): what people decided about it, newest first,
 * each with who, when and its note: the approvals of its plan revisions (the one each revision holds now; one made again is in the
 * history beside the one before), the owner's acceptance on record, the results a person accepted anyway in its runs, and the decision
 * records a merge brought (their link kind reserved for durable decisions, Program item DR1). Then the durable decision records of the
 * ticket's runs (Program item DR1: a gate of the approved revision, an agent's request), each with its state and its receipts in words,
 * and Answer (or Show) opening its card. Below them, the decisions the approved revision says a person will make, each with its latest
 * record's state: Conductor holds the tasks they name until the operator answers. Read from what the page reads (the revisions, the
 * ticket, the runs' candidates, the ticket's decisions), never a copy of its own. One column on a phone.
 */
const props = defineProps<{
  rows: readonly DecisionRow[]
  /** The durable decisions of the ticket's runs, newest first; null before the first read answered. */
  records?: readonly Decision[] | null
  /** The approved revision's required decisions, its tag (r2) and its id, when its body is read. */
  required?: readonly PlanDecision[]
  revision?: string
  planId?: string
  now: number
}>()

const ICONS: Record<DecisionRow['kind'], string> = {
  approval: 'i-lucide-badge-check',
  acceptance: 'i-lucide-circle-check-big',
  'accepted-anyway': 'i-lucide-shield-alert',
  record: 'i-lucide-scale',
}
const tones = { warning: 'text-warning', error: 'text-error', muted: 'text-muted', default: 'text-default', success: 'text-success' }
const decisionModal = useDecisionModal()
/** A required decision's latest record: the newest gate of that plan decision opened under the approved revision, none of another's. */
const recordOf = (gate: string) => requiredRecord(props.records, gate, props.planId)
</script>

<template>
  <section class="flex flex-col gap-2" data-ticket-decisions :data-count="rows.length">
    <h3 class="text-xs font-semibold uppercase tracking-wide text-muted">Decisions</h3>
    <p v-if="!rows.length" class="text-sm text-dimmed" data-decisions-empty>No approval, acceptance or Accept anyway yet.</p>
    <ul v-else class="flex flex-col gap-1.5">
      <li v-for="d in rows" :key="d.key" class="flex min-w-0 items-start gap-2 rounded-md px-3 py-2 text-sm ring ring-default" :data-decision="d.key" :data-decision-kind="d.kind">
        <UIcon :name="ICONS[d.kind]" class="mt-0.5 size-4 flex-none" :class="tones[d.tone]" />
        <div class="flex min-w-0 flex-1 flex-col gap-0.5">
          <div class="flex min-w-0 flex-wrap items-baseline gap-x-2">
            <span class="min-w-0 break-words font-medium text-highlighted" data-decision-title>{{ d.title }}</span>
            <span class="text-xs" :class="tones[d.tone]" data-decision-state>{{ d.state }}</span>
          </div>
          <p v-if="d.note" class="whitespace-pre-wrap break-words text-xs text-default" data-decision-note>{{ d.note }}</p>
          <p class="text-xs text-dimmed">
            by <span data-decision-by>{{ d.by }}</span> {{ relativeTime(d.at, now, { suffix: true }) }}
            <template v-if="d.run">
              · <NuxtLink :to="`/runs/${encodeURIComponent(d.run)}`" class="hover:underline" data-decision-run>open the run</NuxtLink>
            </template>
            <template v-if="d.url">
              · <a :href="d.url" target="_blank" rel="noopener noreferrer" class="hover:underline">open the record</a>
            </template>
          </p>
        </div>
      </li>
    </ul>
    <ul v-if="records?.length" class="flex flex-col gap-1.5" data-ticket-decision-records>
      <li v-for="r in records" :key="r.id" class="flex min-w-0 items-start gap-2 rounded-md px-3 py-2 text-sm ring ring-default" :data-ticket-decision-record="r.id" :data-decision-state="r.state">
        <UIcon name="i-lucide-scale" class="mt-0.5 size-4 flex-none" :class="tones[decisionWords(r).tone]" />
        <div class="flex min-w-0 flex-1 flex-col gap-0.5">
          <div class="flex min-w-0 flex-wrap items-baseline gap-x-2">
            <span class="font-mono text-xs font-semibold">{{ r.id }}</span>
            <span class="min-w-0 break-words font-medium text-highlighted">{{ r.request.question }}</span>
          </div>
          <p class="break-words text-xs" :class="tones[decisionWords(r).tone]" data-ticket-decision-state>{{ decisionWords(r).state }}</p>
          <p v-if="r.answer" class="break-words text-xs text-muted">answered: {{ r.answer }}</p>
          <p class="text-xs text-dimmed">
            {{ r.origin === 'plan' ? `the plan's gate ${r.gate ?? ''}` : `asked by ${r.asker?.task ?? 'an agent'}` }} {{ relativeTime(r.createdAt, now, { suffix: true }) }} ·
            <NuxtLink :to="`/runs/${encodeURIComponent(r.run)}?view=tasks`" class="hover:underline">open the run</NuxtLink>
          </p>
        </div>
        <UButton :label="r.state === 'open' ? 'Answer' : 'Show'" size="xs" :color="r.state === 'open' ? 'warning' : 'neutral'" variant="soft" class="flex-none" :data-ticket-decision-open="r.id" @click="decisionModal.open(r.id)" />
      </li>
    </ul>
    <div v-if="required?.length" class="flex flex-col gap-1 rounded-md px-3 py-2 ring ring-default" data-decisions-required>
      <span class="text-xs font-medium text-muted">To be decided, as {{ revision || 'the approved revision' }} says</span>
      <ul class="flex flex-col gap-0.5 text-sm">
        <li v-for="d in required" :key="d.id" class="min-w-0 break-words" :data-decision-required="d.id">
          <span class="font-mono text-xs text-muted">{{ d.id }}</span> {{ d.question }}
          <span class="text-xs text-dimmed">({{ d.kind }}<template v-if="d.before.length">, before {{ d.before.join(', ') }}</template>)</span>
          <span v-if="recordOf(d.id)" class="text-xs" :data-decision-required-record="recordOf(d.id)!.id"> · {{ recordOf(d.id)!.id }} {{ recordOf(d.id)!.state }}</span>
        </li>
      </ul>
      <p class="text-xs text-dimmed" data-decisions-held>Conductor holds the tasks they name until the operator answers: each run launched from it opens one decision per row.</p>
    </div>
  </section>
</template>
