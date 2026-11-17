<script setup lang="ts">
import type { AgentInfo } from '~/composables/useSessions'
import { attemptsWords, candidateCommitWords, candidateMark, type ComparedCandidate } from '~/utils/compare'
import { shortCommit } from '~/utils/dispatchPlans'
import { SELECTION_WORDS, type SelectionMark } from '~/utils/compareSelect'
import { attemptSession, taskStatus } from '~/utils/status'

/**
 * A comparison's columns (Program item CP1, docs/plans/compare-crew.md, PR-4): one per candidate, in the block's order, with its agent
 * and arguments, its approach, its commit (its standing result, or a failed lane's last submitted commit, marked "failed: not
 * selectable"), its attempts and its state in the status language: a running lane whose task the live store holds draws that task's own
 * mark (taskStatus), any other the comparison's word for it (candidateMark). Nothing here is coloured by what an agent said. Once the
 * selection is applied each column says "selected" or "not selected" (`labels`, PR-7), in neutral ink.
 */
const props = defineProps<{ candidates: ComparedCandidate[]; agents?: AgentInfo[]; labels?: Record<string, SelectionMark> }>()

const live = useAttention()
const clock = useNow()

function markOf(c: ComparedCandidate) {
  if (c.state === 'running') {
    const r = live.runOf(c.run)
    const t = r?.tasks?.find((x) => x.id === c.task)
    if (r && t && !r.record) return taskStatus(t, r, attemptSession(t, live.sessions.value), clock.value)
  }
  return candidateMark(c)
}
const agentName = (id: string) => props.agents?.find((a) => a.id === id)?.name ?? id
/** The columns side by side from a tablet up, one under another on a phone. */
const cols = computed(() => (props.candidates.length >= 4 ? 'sm:grid-cols-2 xl:grid-cols-4' : props.candidates.length === 3 ? 'sm:grid-cols-2 lg:grid-cols-3' : 'sm:grid-cols-2'))
</script>

<template>
  <ol class="grid grid-cols-1 gap-3" :class="cols" data-compare-candidates>
    <li
      v-for="c in candidates"
      :key="c.task"
      class="flex min-w-0 flex-col gap-1.5 rounded-lg p-3 text-sm ring ring-default"
      :data-compare-candidate="c.task"
      :data-state="c.state"
      :data-selectable="c.selectable ? 'true' : 'false'"
    >
      <div class="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
        <span class="min-w-0 break-all font-mono font-semibold text-highlighted" data-mono="id" data-compare-candidate-task>{{ c.task }}</span>
        <StatusMark :status="markOf(c)" word data-compare-candidate-state />
        <UBadge
          v-if="labels?.[c.task]"
          :label="SELECTION_WORDS[labels[c.task]!]"
          color="neutral"
          :variant="labels[c.task] === 'selected' ? 'outline' : 'subtle'"
          size="sm"
          :class="labels[c.task] === 'selected' ? 'text-default' : 'text-muted'"
          :data-compare-selection="labels[c.task]"
        />
      </div>
      <p class="flex min-w-0 flex-wrap items-baseline gap-x-1.5 text-xs text-muted" data-compare-candidate-agent>
        <span class="text-default">{{ agentName(c.agent) }}</span>
        <span v-if="c.member">as {{ c.member }}</span>
        <code v-if="c.args.length" class="min-w-0 break-all font-mono" data-mono="command" data-compare-candidate-args>{{ c.args.join(' ') }}</code>
      </p>
      <p v-if="c.approach" class="whitespace-pre-wrap break-words text-default" data-compare-candidate-approach>{{ c.approach }}</p>
      <p v-else class="text-xs text-muted" data-compare-candidate-approach>No approach given: an independent try.</p>
      <p class="flex min-w-0 flex-wrap items-baseline gap-x-1.5 text-xs" data-compare-candidate-commit :data-commit="c.commit">
        <span v-if="c.commit" class="font-mono text-highlighted" :title="c.commit" data-mono="commit">{{ shortCommit(c.commit) }}</span>
        <span class="text-muted">{{ candidateCommitWords(c) }}</span>
      </p>
      <p class="text-xs text-muted" data-compare-candidate-attempts>{{ attemptsWords(c) }}<template v-if="c.manifestChanged"> · its commit changes conductor.manifest.json</template></p>
      <p v-if="c.unavailable" class="flex items-start gap-1.5 text-xs text-muted" data-compare-candidate-unavailable>
        <UIcon name="i-lucide-git-branch" class="mt-0.5 size-3.5 flex-none" />
        <span class="min-w-0 break-words">Git could not be read for it: {{ c.unavailable }}</span>
      </p>
    </li>
  </ol>
</template>
