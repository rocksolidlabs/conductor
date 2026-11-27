<script setup lang="ts">
import type { PlanBody } from '~/composables/useDispatch'
import type { AgentInfo } from '~/composables/useSessions'
import { argvLine, checksEachWords, comparisonChecks, planComparison } from '~/utils/dispatchPlans'

/**
 * What a comparison's plan revision fixes (CP1, docs/plans/compare-crew.md), as its body and its approval dialog show it: the criteria
 * every candidate is held to, each candidate with its task, its member's agent and its approach, and the checks on each submitted
 * candidate attempt, counted as the launch's statement counts them (comparisonChecks: one candidate's task's checks and the manifest's
 * verify steps, never every lane's added together). `compact` for the approval dialog: the approaches folded to a few lines each and the
 * checks said here (the body says them under its tasks, data-plan-expected).
 */
const props = defineProps<{
  body: PlanBody
  agents?: readonly AgentInfo[]
  compact?: boolean
}>()

const cmp = computed(() => planComparison(props.body, props.agents ?? []))
const checks = computed(() => comparisonChecks(props.body) ?? [])
</script>

<template>
  <section v-if="cmp" class="flex min-w-0 flex-col gap-2" data-plan-compare :data-plan-compare-candidates="cmp.candidates.length">
    <h4 class="text-xs font-semibold text-muted">Agent trial: {{ cmp.candidates.length }} candidates from one base, checked alike</h4>
    <div class="flex flex-col gap-0.5">
      <span class="text-xs text-muted">Every candidate is held to</span>
      <ol class="flex list-decimal flex-col gap-0.5 pl-5" data-plan-compare-criteria>
        <li v-for="k in cmp.criteria" :key="k.id" class="break-words" :data-plan-compare-criterion="k.id">{{ k.text }}</li>
      </ol>
    </div>
    <ul class="grid grid-cols-1 gap-2" :class="compact ? '' : 'sm:grid-cols-2'">
      <li v-for="c in cmp.candidates" :key="c.task" class="flex min-w-0 flex-col gap-0.5 rounded-md p-2 ring ring-default" :data-plan-compare-candidate="c.task">
        <span class="flex min-w-0 flex-wrap items-baseline gap-x-2">
          <span class="break-all font-mono font-medium text-highlighted" data-mono="id">{{ c.task }}</span>
          <span class="text-xs text-muted">{{ c.member }}<template v-if="c.agent"> · {{ c.agent }}</template></span>
        </span>
        <p v-if="c.approach" class="whitespace-pre-wrap break-words" :class="compact ? 'line-clamp-4' : ''" :title="compact ? c.approach : undefined" data-plan-compare-approach>{{ c.approach }}</p>
        <p v-else class="text-xs text-muted" data-plan-compare-approach>No approach given: an independent try.</p>
      </li>
    </ul>
    <p v-if="compact" class="text-xs text-muted" data-plan-compare-checks :data-count="checks.length">
      {{ checksEachWords(checks.length) }}<template v-if="checks.length">: <template v-for="(c, i) in checks" :key="`${c.phase}/${c.id}`"><template v-if="i">, </template><span class="font-mono" data-mono="id">{{ c.id }}</span> <code class="break-all font-mono text-dimmed" data-mono="command">{{ argvLine(c.argv) }}</code></template></template>.
    </p>
  </section>
</template>
