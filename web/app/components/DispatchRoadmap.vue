<script setup lang="ts">
import type { DispatchLabel, DispatchMilestone, Horizon, TicketSummary } from '~/composables/useDispatch'
import type { AgentInfo } from '~/composables/useSessions'
import type { CardColumn } from '~/components/DispatchCards.vue'
import { HORIZONS, horizonWord, milestoneProgress, roadmapColumns, roadmapStep } from '~/utils/dispatch'

/**
 * A project's tickets as a roadmap (docs/plans/dispatch.md, "web/app"): Now, Next, Later and a folded Not planned, each card in the list's
 * order within its column, moved as a board's are (DispatchCards.vue: drag and drop, or ← → on a focused card), the page setting the
 * ticket's horizon. Beside the columns, the project's milestones, each with how many of the tickets shown are in it and how many of
 * those are done (the cancelled counted in neither). The columns scroll sideways when they do not fit; on a phone the milestones come
 * under them.
 */
const props = defineProps<{
  tickets: readonly TicketSummary[]
  labels: ReadonlyMap<string, DispatchLabel>
  milestones: readonly DispatchMilestone[]
  agents: readonly AgentInfo[]
  locked?: boolean
}>()
const emit = defineEmits<{ move: [key: string, horizon: Horizon] }>()

const columns = computed<CardColumn[]>(() =>
  roadmapColumns(props.tickets).map((c) => ({ id: c.word.name, label: c.word.label, icon: c.word.icon, tone: 'text-muted', tickets: c.tickets, folded: c.folded })),
)
const progress = computed(() => milestoneProgress(props.milestones, props.tickets))

/** A column's id is its horizon's name (now, next, later, none for not planned). */
function step(t: TicketSummary, delta: 1 | -1): string | null {
  const h = roadmapStep(t.horizon ?? '', delta)
  return h === null ? null : horizonWord(h).name
}
function moved(key: string, id: string) {
  const h = HORIZONS.find((x) => x.name === id)
  if (h) emit('move', key, h.value)
}

const cards = useTemplateRef<{ focusCard: (key: string) => Promise<boolean> }>('cards')
/** Gives a card the focus, Not planned unfolded first when the card is in it (DispatchCards.focusCard). */
function focusCard(key: string): Promise<boolean> {
  return cards.value?.focusCard(key) ?? Promise.resolve(false)
}
defineExpose({ focusCard })
</script>

<template>
  <div class="flex min-h-0 min-w-0 flex-1 flex-col overflow-y-auto lg:flex-row lg:overflow-hidden" data-roadmap>
    <DispatchCards ref="cards" :columns="columns" :labels="labels" :agents="agents" :locked="locked" :step="step" class="lg:min-h-0" @move="moved" />
    <aside class="flex-none border-t border-default p-4 lg:w-64 lg:overflow-y-auto lg:border-t-0 lg:border-l" data-roadmap-milestones>
      <h3 class="mb-2 text-xs font-semibold tracking-wide text-muted uppercase">Milestones</h3>
      <p v-if="!progress.length" class="text-xs text-dimmed">No milestones in this project yet.</p>
      <ul class="flex flex-col gap-3">
        <li v-for="p in progress" :key="p.milestone.id" class="flex flex-col gap-1" :data-milestone="p.milestone.id" :data-done="p.done" :data-total="p.total">
          <span class="flex items-baseline gap-2 text-sm">
            <UIcon name="i-lucide-milestone" class="size-3.5 flex-none self-center text-muted" />
            <span class="min-w-0 flex-1 truncate" :class="p.milestone.state === 'closed' ? 'text-muted line-through' : 'text-highlighted'">{{ p.milestone.name }}</span>
            <span class="flex-none text-xs text-muted tabular-nums" :title="`${p.done} of the ${p.total} tickets shown in it are done (the cancelled ones are not counted)`" data-milestone-count>{{ p.done }}/{{ p.total }}</span>
          </span>
          <UProgress :model-value="p.total ? Math.round((100 * p.done) / p.total) : 0" size="xs" :color="p.total && p.done === p.total ? 'success' : 'primary'" />
          <span v-if="p.milestone.target" class="text-xs text-dimmed">Due {{ p.milestone.target }}</span>
        </li>
      </ul>
    </aside>
  </div>
</template>
