<script setup lang="ts">
import { Handle, Position, type NodeProps } from '@vue-flow/core'
import type { GraphDirection, GraphNode } from '~/utils/crewGraph'
import { startWords } from '~/utils/crewWords'
import type { MarkView } from '~/utils/status'
import { memberOffer, type OfferRun } from '~/utils/runs'

/**
 * One member on the crew graph: its agent, name and status mark (memberStatusOf), its first prompt in the editor (its branch and diff in a run), a
 * warning when its start rule could not be drawn, and its last line: when
 * it starts (the editor), or what it does now and what can be done with it
 * (Start now, Resume, Open). A member started by hand is drawn dashed. The
 * target handle takes the one "after" edge, the source handle starts them.
 * Enter opens the member; the graph handles clicks.
 */
export interface MemberNodeData {
  node: GraphNode
  direction: GraphDirection
  editable: boolean
  /** The run the member is in; empty for a saved crew. */
  runId: string
  stopped: boolean
  /** The run, as what a member offers by hand reads it (memberOffer); absent for a saved crew. */
  run?: OfferRun | null
  /** The agent's name from the catalog; its id when unknown. */
  agentName: string
  /** Its state in the status language (memberStatusOf); absent for a saved crew. */
  mark?: MarkView
}

const props = defineProps<NodeProps<MemberNodeData>>()
const emit = defineEmits<{ open: [name: string]; start: [name: string]; setStart: [name: string, when: 'immediately' | 'manual']; remove: [name: string] }>()

const n = computed(() => props.data.node)
const state = computed(() => n.value.state)
const targetPos = computed(() => (props.data.direction === 'LR' ? Position.Left : Position.Top))
const sourcePos = computed(() => (props.data.direction === 'LR' ? Position.Right : Position.Bottom))
const manual = computed(() => n.value.member.start.when === 'manual')
const start = computed(() => startWords(n.value.member.start))
/**
 * The node's last line in a run (its state's word is the mark's, the Crews pages' one vocabulary): whom a member not started waits for,
 * why one ended; the start rule's words in the editor and for a crew not running.
 */
const words = computed<{ text: string; tone: 'warning' | 'error' | 'muted' | 'default' } | null>(() => {
  if (!state.value) return null
  const m = n.value.member
  const mark = props.data.mark
  if (state.value === 'pending') return { text: props.data.stopped ? 'never started' : m.start.when === 'after' ? `after ${m.start.member}` : m.start.when === 'manual' ? 'by hand' : 'at launch', tone: 'muted' }
  if (state.value === 'ended') return { text: mark ? mark.legend.replace(/^[^:]+: /, '') : '', tone: mark?.key === 'failed' ? 'error' : 'muted' }
  return { text: mark?.word ?? '', tone: 'default' }
})
/** What the node offers by hand: Start now, Resume or Relaunch, or nothing (memberOffer). */
const offer = computed(() => memberOffer(state.value, props.data.run))
const tone = { warning: 'text-warning', error: 'text-error', muted: 'text-muted', default: 'text-default' }

const menu = computed(() => [
  [
    { label: 'Starts at launch', icon: 'i-lucide-zap', onSelect: () => emit('setStart', n.value.id, 'immediately') },
    { label: 'Starts when you press Start', icon: 'i-lucide-hand', onSelect: () => emit('setStart', n.value.id, 'manual') },
  ],
  [{ label: 'Remove member', icon: 'i-lucide-trash-2', color: 'error' as const, onSelect: () => emit('remove', n.value.id) }],
])
</script>

<template>
  <div
    class="flex h-24 w-60 flex-col gap-1 rounded-lg border bg-default p-2.5 text-left shadow-xs outline-none transition-shadow focus-visible:ring-2 focus-visible:ring-primary"
    :class="[
      selected ? 'border-primary ring-1 ring-primary' : state === 'needs_input' ? 'border-warning' : state === 'pending' || (!state && manual) ? 'border-dashed border-accented' : 'border-default',
      state === 'ended' && 'opacity-70',
    ]"
    tabindex="0"
    :data-graph-node="n.id"
    :data-status="state ?? ''"
    :aria-label="`${n.id}: ${words?.text ?? start.text}`"
    @keydown.enter.prevent="emit('open', n.id)"
  >
    <Handle type="target" :position="targetPos" :connectable="data.editable" :connectable-start="false" class="!size-2.5 !border-2 !border-default !bg-primary" :class="!data.editable && n.member.start.when !== 'after' && '!opacity-0'" />
    <div class="flex min-w-0 items-center gap-2">
      <SessionAvatar :agent-id="n.member.agentId" :solid="state === 'needs_input'" :dashed="state === 'ended' || state === 'pending'" />
      <span class="max-w-[8.5rem] flex-none truncate font-mono text-[13px] font-semibold text-highlighted">{{ n.id }}</span>
      <span class="min-w-0 truncate text-[11px] text-muted">{{ data.agentName || n.member.agentId }}</span>
      <StatusMark v-if="data.mark" :status="data.mark" word class="ml-auto" />
      <UDropdownMenu v-else-if="data.editable" :items="menu" :content="{ align: 'end' }" class="ml-auto">
        <IconButton icon="i-lucide-ellipsis" size="xs" color="neutral" variant="ghost" :label="`${n.id}: start rule`" class="nodrag" />
      </UDropdownMenu>
    </div>
    <div class="flex min-w-0 items-center gap-2 text-[11.5px] text-muted" :class="!n.warning && !n.member.branch && !n.member.diff && 'font-normal'">
      <span v-if="n.warning" class="truncate text-warning" :title="n.warning">{{ n.warning }}</span>
      <span v-else-if="n.member.branch" class="truncate font-mono text-[11px]" :title="n.member.branch">{{ n.member.branch }}</span>
      <span v-else-if="n.member.prompt" class="truncate text-default" :title="n.member.prompt">“{{ n.member.prompt }}”</span>
      <span v-else class="truncate italic">no first prompt</span>
      <span v-if="n.member.diff" class="ml-auto flex-none font-mono text-[11px]">+{{ n.member.diff.added }} −{{ n.member.diff.removed }}</span>
    </div>
    <div class="mt-auto flex min-w-0 items-center gap-1.5 text-[11px]">
      <template v-if="!words">
        <UIcon :name="start.icon" class="size-3.5 flex-none" :class="start.muted ? 'text-muted' : 'text-default'" />
        <span class="truncate" :class="start.muted ? 'text-muted' : 'text-default'">{{ start.text }}</span>
      </template>
      <template v-else-if="state === 'pending'">
        <span class="truncate" :class="tone[words.tone]">{{ words.text }}</span>
        <UButton v-if="offer === 'start'" label="Start now" icon="i-lucide-play" size="xs" color="neutral" variant="outline" class="nodrag ml-auto" @click.stop="emit('start', n.id)" />
      </template>
      <template v-else-if="state === 'ended'">
        <span class="truncate" :class="tone[words.tone]" :title="words.text">{{ words.text }}</span>
        <ResumeButton v-if="data.runId && offer === 'resume'" :run-id="data.runId" :member="n.id" stay size="xs" class="nodrag ml-auto" @click.stop />
      </template>
      <template v-else>
        <span class="truncate" :class="tone[words.tone]">{{ words.text }}</span>
        <UButton label="Open" icon="i-lucide-terminal" size="xs" color="neutral" variant="ghost" class="nodrag ml-auto" @click.stop="emit('open', n.id)" />
      </template>
    </div>
    <Handle type="source" :position="sourcePos" :connectable="data.editable" :connectable-end="false" class="!size-2.5 !border-2 !border-default !bg-primary" :class="!data.editable && '!opacity-0'" />
  </div>
</template>
