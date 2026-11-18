<script setup lang="ts">
import type { AgentInfo } from '~/composables/useSessions'
import { MEASURED_BY } from '~/utils/compare'
import { laneSelection, type SelectionMark } from '~/utils/compareSelect'
import { ticketPath } from '~/utils/dispatch'
import { shortCommit } from '~/utils/dispatchPlans'

/**
 * A ticket's compare view (Program item CP1, docs/plans/compare-crew.md, PR-4), the ticket page's `?view=compare&run=<any run of the
 * chain>`, drawn in place of its sections and its strip as the Review view is: the comparison's base commit, its objective and
 * criteria, one column per candidate (CompareCandidates), the checks with the server's verdicts (CompareChecks) and the measured rows
 * (CompareMeasured), every one measured by Conductor; below the measured part and apart from it, the supporting information
 * (CompareSupporting: what the agents, their reviews and Brakeman said); and last the selection (CompareSelect, PR-7: DR1's record of
 * origin `compare`, answered through DR1's own card), so Send is reached only past what it is answered from. Each column says
 * "selected" or "not selected" once the selection is applied, read from the selection alone. One column on a phone, in the same
 * order (PR-8b); Brakeman's table scrolls inside its own card, never the page. Read by
 * useComparison: again on the run's change, on Dispatch's events naming the ticket, on the comparison's Brakeman request and on a
 * reconnect. A run of no comparison, a read under way and a first read that failed are said as every page says them (PageEmpty).
 */
const props = defineProps<{
  ticketKey: string
  /** The ticket's id, which Dispatch's events name it by. */
  ticketId: string
  /** The run the route names, any of the chain's. */
  run: string
  agents?: AgentInfo[]
}>()

const c = useComparison({ run: () => props.run, ticket: () => props.ticketId })
const comparison = computed(() => c.comparison.value)
const state = computed(() => {
  if (!props.run) return 'unnamed'
  if (c.missing.value) return 'missing'
  if (comparison.value) return 'content'
  return c.error.value ? 'failed' : 'reading'
})
const back = { query: {} }
/** Each lane's label once the selection is applied: selected, not selected. */
const labels = computed<Record<string, SelectionMark>>(() => {
  const out: Record<string, SelectionMark> = {}
  for (const cand of comparison.value?.candidates ?? []) {
    const m = laneSelection(comparison.value, cand.task)
    if (m) out[cand.task] = m
  }
  return out
})
</script>

<template>
  <section class="flex flex-col gap-4" data-compare-view :data-compare-run="run" :data-compare-state="state" :data-compare-settled="comparison ? String(comparison.settled) : undefined">
    <div class="flex flex-wrap items-center gap-2">
      <h2 class="text-base font-semibold text-highlighted">Comparison</h2>
      <UButton label="Back to the ticket" icon="i-lucide-arrow-left" size="xs" color="neutral" variant="ghost" :to="back" data-compare-back />
    </div>

    <PageEmpty v-if="state === 'unnamed'" state="missing" words="No run is named: a comparison opens from a run of the ticket." :actions="[{ label: `Back to ${ticketKey}`, icon: 'i-lucide-arrow-left', to: ticketPath(ticketKey) }]" data-compare-unnamed />
    <PageEmpty v-else-if="state === 'missing'" state="missing" :words="`${c.missing.value}: ${run}`" :actions="[{ label: `Back to ${ticketKey}`, icon: 'i-lucide-arrow-left', to: ticketPath(ticketKey) }]" data-compare-missing />
    <PageEmpty v-else-if="state === 'failed'" state="failed" :error="c.error.value" data-compare-failed @again="c.read()" />
    <PageEmpty v-else-if="state === 'reading'" state="reading" thing="comparison" data-compare-reading />

    <template v-else-if="comparison">
      <UAlert v-if="c.error.value" color="neutral" variant="subtle" icon="i-lucide-cloud-alert" :title="`Not read again: ${c.error.value}`" description="What is shown is the last comparison read; it is read again by itself." data-compare-stale />
      <p v-if="c.changing.value" class="flex items-center gap-1.5 text-xs text-muted" data-compare-changing>
        <UIcon name="i-lucide-loader-circle" class="size-3.5 flex-none animate-spin" />The run is changing: the comparison is read again in a second.
      </p>

      <!-- What the candidates are compared on: the base, the objective, the criteria. -->
      <section class="flex flex-col gap-1.5 rounded-lg p-3 text-sm ring ring-default" data-compare-head>
        <p class="flex min-w-0 flex-wrap items-baseline gap-x-1.5 text-xs text-muted">
          <span>Every candidate starts at the base</span>
          <span class="font-mono text-highlighted" :title="comparison.base" data-mono="commit" data-compare-base>{{ shortCommit(comparison.base) }}</span>
          <span>·</span>
          <span data-compare-lanes-words>{{ comparison.settled ? 'every lane has ended' : 'a lane is still being worked' }}</span>
        </p>
        <p v-if="comparison.objective" class="whitespace-pre-wrap break-words text-default" data-compare-objective>{{ comparison.objective }}</p>
        <div v-if="comparison.criteria.length" class="flex flex-col gap-0.5">
          <span class="text-xs font-medium text-muted">Criteria</span>
          <ol class="flex list-decimal flex-col gap-0.5 pl-5" data-compare-criteria>
            <li v-for="k in comparison.criteria" :key="k.id" class="break-words" :data-compare-criterion="k.id">{{ k.text }}</li>
          </ol>
        </div>
      </section>

      <section class="flex flex-col gap-2" data-compare-columns>
        <div class="flex flex-wrap items-baseline gap-x-2">
          <h3 class="text-sm font-semibold text-highlighted">Candidates</h3>
          <span class="text-xs text-muted">{{ MEASURED_BY }}</span>
        </div>
        <CompareCandidates :candidates="comparison.candidates" :agents="agents" :labels="labels" />
      </section>

      <section class="flex flex-col gap-4" data-compare-measured>
        <CompareChecks :checks="comparison.checks" :candidates="comparison.candidates" />
        <CompareMeasured :candidates="comparison.candidates" :not-observed="comparison.independence?.notObserved" />
      </section>

      <!-- Supporting information, not evidence: its own panel, below the measured part, never colouring it. -->
      <CompareSupporting :supporting="comparison.supporting" :run="comparison.run" :state="comparison.state" @asked="c.read()" />

      <!-- The selection (PR-7): DR1's record, answered through DR1's own card. Last, on every width: after the measured part and the
           supporting panel, so the answer sits under what it is answered from (PR-8b's phone order, on a wide window too). -->
      <CompareSelect :comparison="comparison" />
    </template>
  </section>
</template>
