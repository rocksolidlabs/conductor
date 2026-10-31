<script setup lang="ts">
import type { DispatchLabel, TicketStatus, TicketSummary } from '~/composables/useDispatch'
import type { AgentInfo } from '~/composables/useSessions'
import type { CardColumn } from '~/components/DispatchCards.vue'
import { boardColumns, boardStep, type WordColor } from '~/utils/dispatch'

/**
 * A project's tickets as a board (docs/plans/dispatch.md, "web/app"): a column per status in its order (Backlog, Ready, In progress, In
 * review, Done, Cancelled), each card in the list's order within its column. A card moves by drag and drop or by ← → while it has the
 * focus (DispatchCards.vue); the page makes the edit, and a move to Done the close policy refuses says the policy's words and the card
 * snaps back. The board scrolls sideways when its columns do not fit.
 */
const props = defineProps<{
  tickets: readonly TicketSummary[]
  labels: ReadonlyMap<string, DispatchLabel>
  agents: readonly AgentInfo[]
  locked?: boolean
}>()
const emit = defineEmits<{ move: [key: string, status: TicketStatus] }>()

const TONES: Record<WordColor, string> = { neutral: 'text-muted', primary: 'text-primary', success: 'text-success', info: 'text-info', warning: 'text-warning', error: 'text-error' }

const columns = computed<CardColumn[]>(() => boardColumns(props.tickets).map((c) => ({ id: c.status, label: c.word.label, icon: c.word.icon, tone: TONES[c.word.color], tickets: c.tickets })))

function step(t: TicketSummary, delta: 1 | -1): string | null {
  return boardStep(t.status, delta)
}

const cards = useTemplateRef<{ focusCard: (key: string) => Promise<boolean> }>('cards')
/** Gives a card the focus (DispatchCards.focusCard). */
function focusCard(key: string): Promise<boolean> {
  return cards.value?.focusCard(key) ?? Promise.resolve(false)
}
defineExpose({ focusCard })
</script>

<template>
  <DispatchCards ref="cards" :columns="columns" :labels="labels" :agents="agents" :locked="locked" :step="step" data-board @move="(key: string, id: string) => emit('move', key, id as TicketStatus)" />
</template>
