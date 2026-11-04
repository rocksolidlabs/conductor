<script setup lang="ts">
import type { PlanDecision } from '~/composables/useDispatch'
import type { DecisionRow } from '~/utils/dispatch'
import { relativeTime } from '~/utils/sessions'

/**
 * A ticket's Decisions (Program item DS2, docs/plans/dispatch-evidence.md, "web/app", PR-12): what people decided about it, newest first,
 * each with who, when and its note: the approvals of its plan revisions (the one each revision holds now; one made again is in the
 * history beside the one before), the owner's acceptance on record, the results a person accepted anyway in its runs, and the decision
 * records a merge brought (their link kind reserved for durable decisions, Program item DR1). Below them, the decisions the approved
 * revision says a person will make: recorded and shown, not held, which the section says beside them. Read from what the page reads (the
 * revisions, the ticket, the runs' candidates), never a copy of its own. One column on a phone.
 */
defineProps<{
  rows: readonly DecisionRow[]
  /** The approved revision's required decisions, and its tag (r2), when its body is read. */
  required?: readonly PlanDecision[]
  revision?: string
  now: number
}>()

const ICONS: Record<DecisionRow['kind'], string> = {
  approval: 'i-lucide-badge-check',
  acceptance: 'i-lucide-circle-check-big',
  'accepted-anyway': 'i-lucide-shield-alert',
  record: 'i-lucide-scale',
}
const tones = { warning: 'text-warning', error: 'text-error', muted: 'text-muted', default: 'text-default', success: 'text-success' }
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
    <div v-if="required?.length" class="flex flex-col gap-1 rounded-md px-3 py-2 ring ring-default" data-decisions-required>
      <span class="text-xs font-medium text-muted">To be decided, as {{ revision || 'the approved revision' }} says</span>
      <ul class="flex flex-col gap-0.5 text-sm">
        <li v-for="d in required" :key="d.id" class="min-w-0 break-words" :data-decision-required="d.id">
          <span class="font-mono text-xs text-muted">{{ d.id }}</span> {{ d.question }}
          <span class="text-xs text-dimmed">({{ d.kind }}<template v-if="d.before.length">, before {{ d.before.join(', ') }}</template>)</span>
        </li>
      </ul>
      <p class="text-xs text-dimmed">Recorded and shown, not held: the run does not wait on them yet. Durable decisions, still to come, will hold the tasks before them.</p>
    </div>
  </section>
</template>
