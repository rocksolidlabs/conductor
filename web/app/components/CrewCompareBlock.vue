<script setup lang="ts">
import type { AgentInfo, CrewMember } from '~/composables/useSessions'
import type { CrewCompare } from '~/utils/protocol'
import type { DraftTask } from '~/utils/crews'
import { joinArgv } from '~/utils/argv'

/**
 * An agent trial's block in the crew editor (CP1, where it is called a comparison), read only: the criteria, then each candidate's lane (its task, its member's agent and
 * arguments, its approach, and the review that comes after it), and whether Brakeman drafts the comparison. The template made it and a
 * save sends it back as it was read; an edit of the lanes' tasks is held to the comparison's rules by the server, which says what it
 * refuses.
 */
const props = defineProps<{
  compare: CrewCompare
  tasks: readonly DraftTask[]
  members: readonly CrewMember[]
  agents: readonly AgentInfo[]
}>()

/** Each candidate's lane: its task, its member and agent, and the tasks that need it (its review), in the block's order. */
const lanes = computed(() =>
  props.compare.candidates.map((c, i) => {
    const task = props.tasks.find((t) => t.id === c.task)
    const member = task ? props.members.find((m) => m.name === task.member) : undefined
    const after = task ? props.tasks.filter((t) => t.needKeys.includes(task.key)).map((t) => t.id) : []
    return {
      task: c.task,
      letter: String.fromCharCode(65 + i),
      approach: c.approach?.trim() ?? '',
      member: member?.name ?? task?.member ?? '',
      agent: member ? (props.agents.find((a) => a.id === member.agentId)?.name ?? member.agentId) : '',
      args: member?.args ?? [],
      after,
      missing: !task,
    }
  }),
)
</script>

<template>
  <section class="flex flex-col gap-3 rounded-md border border-default p-3 text-sm" data-crew-compare-block :data-compare-candidates="compare.candidates.length">
    <div class="flex flex-wrap items-baseline gap-x-2.5 gap-y-1">
      <span class="font-semibold text-highlighted">Agent trial</span>
      <span class="min-w-0 text-[12.5px] text-muted">{{ compare.candidates.length }} agents, each working alone from the same commit, checked alike. Read only here.</span>
    </div>
    <div class="flex flex-col gap-1">
      <span class="text-xs font-medium text-muted">What a good result does</span>
      <ul class="flex list-disc flex-col gap-0.5 pl-5" data-crew-compare-criteria>
        <li v-for="c in compare.criteria" :key="c.id" :data-crew-compare-criterion="c.id">{{ c.text }}</li>
      </ul>
    </div>
    <ul class="grid gap-2 sm:grid-cols-2">
      <li v-for="l in lanes" :key="l.task" class="flex min-w-0 flex-col gap-1 rounded-md bg-elevated/40 p-2" :data-crew-compare-lane="l.task">
        <div class="flex min-w-0 flex-wrap items-baseline gap-x-2">
          <span class="font-medium text-highlighted">Agent {{ l.letter }}</span>
          <code class="text-xs text-muted" data-mono="id">{{ l.task }}</code>
        </div>
        <span v-if="l.missing" class="text-xs text-error">No task of the crew has this id: the server refuses the crew as it stands.</span>
        <span v-else class="min-w-0 text-xs text-muted [overflow-wrap:anywhere]">
          {{ l.member }} · {{ l.agent }}<template v-if="l.args.length"> · <code data-mono="command">{{ joinArgv(l.args) }}</code></template>
        </span>
        <span class="min-w-0 text-xs [overflow-wrap:anywhere]" :class="l.approach ? 'text-default' : 'text-muted'" data-crew-compare-approach>{{ l.approach || 'No approach given: an independent try' }}</span>
        <span v-if="l.after.length" class="text-xs text-muted">Then: <code v-for="t in l.after" :key="t" class="mr-1" data-mono="id">{{ t }}</code></span>
      </li>
    </ul>
    <span class="text-xs text-muted" data-crew-compare-brakeman>{{ compare.brakeman ? 'Brakeman sums up the results when all are done.' : 'No summary by Brakeman for this trial.' }}</span>
  </section>
</template>
