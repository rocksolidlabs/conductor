<script setup lang="ts">
import { listRows, type CrewGraphData, type GraphNode } from '~/utils/crewGraph'
import { startWords } from '~/utils/crewWords'
import { memberOffer, type OfferRun } from '~/utils/runs'
import type { MarkView } from '~/utils/status'

/**
 * The graph on a phone: the members as a list, parents before children,
 * each indented by its depth, with the same status mark, badges and actions
 * as a node. Handoffs are listed under the member that sent them.
 */
const props = defineProps<{ graph: CrewGraphData; marks?: Map<string, MarkView>; editable?: boolean; run?: OfferRun | null; runId?: string; selected?: string }>()
const emit = defineEmits<{ open: [name: string]; start: [name: string]; select: [name: string] }>()

const rows = computed(() => listRows(props.graph))

function handoffsFrom(n: GraphNode) {
  return props.graph.edges.filter((e) => e.kind === 'handoff' && e.from === n.id)
}

/** What a row offers by hand (memberOffer). */
const offerOf = (n: GraphNode) => memberOffer(n.state, props.run)

function startLabel(n: GraphNode): string {
  return n.warning ?? startWords(n.member.start).text
}
</script>

<template>
  <ol class="flex flex-col gap-1.5" data-graph-list>
    <li v-for="n in rows" :key="n.id" :style="{ paddingLeft: `${n.depth * 16}px` }" :data-graph-list-row="n.id" :data-depth="n.depth">
      <div
        class="flex flex-col gap-1 rounded-md border px-2.5 py-2"
        :class="selected === n.id ? 'border-primary bg-primary/5' : 'border-default bg-elevated/30'"
        role="button"
        tabindex="0"
        @click="emit('select', n.id)"
        @keydown.enter.prevent="emit('open', n.id)"
      >
        <div class="flex items-center gap-2">
          <SessionAvatar :agent-id="n.member.agentId" :solid="n.state === 'needs_input'" :dashed="n.state === 'ended' || n.state === 'pending'" />
          <span class="truncate text-sm font-semibold">{{ n.id }}</span>
          <StatusMark v-if="marks?.get(n.id)" :status="marks.get(n.id)!" word class="flex-none" />
          <span class="ml-auto truncate text-[11px] text-muted" :class="n.warning && 'text-warning'">{{ startLabel(n) }}</span>
        </div>
        <div v-if="n.member.branch || n.member.diff || n.member.error" class="flex items-center gap-2 font-mono text-[11px] text-muted">
          <span v-if="n.member.branch" class="truncate">{{ n.member.branch }}</span>
          <span v-if="n.member.diff" class="ml-auto flex-none">+{{ n.member.diff.added }} −{{ n.member.diff.removed }}</span>
          <span v-if="n.member.error" class="truncate text-error">{{ n.member.error }}</span>
        </div>
        <ul v-if="handoffsFrom(n).length" class="flex flex-wrap gap-1">
          <li v-for="e in handoffsFrom(n)" :key="e.id">
            <UBadge :label="`→ ${e.to} ×${e.count}`" color="info" variant="subtle" size="sm" :title="e.last" />
          </li>
        </ul>
        <div v-if="!editable && (n.state === 'pending' || n.state === 'ended')" class="flex gap-1.5" @click.stop>
          <UButton v-if="offerOf(n) === 'start'" label="Start now" icon="i-lucide-play" size="xs" color="neutral" variant="outline" @click="emit('start', n.id)" />
          <ResumeButton v-else-if="offerOf(n) === 'resume' && runId" :run-id="runId" :member="n.id" stay size="xs" />
        </div>
      </div>
    </li>
  </ol>
</template>
