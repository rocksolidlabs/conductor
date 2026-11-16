<script setup lang="ts">
import { BaseEdge, EdgeLabelRenderer, getBezierPath, Position, type EdgeProps } from '@vue-flow/core'
import { handoffPath, type GraphEdge } from '~/utils/crewGraph'
import { relativeTime } from '~/utils/sessions'

/**
 * One edge of the crew graph. A solid edge is a start rule ("after X is done"),
 * with a small label and, in the editor, a button that deletes it (the
 * member then starts immediately). A dashed, animated edge is the handoffs
 * from one member to another in this run: a count badge, and the last
 * message and its time on hover. A dotted edge in the brand colour is a
 * task's need of the result of the task it points from (the tasks graph of
 * a managed run).
 */
export interface CrewEdgeData {
  edge: GraphEdge
  editable: boolean
  now: number
}

const props = defineProps<EdgeProps<CrewEdgeData>>()
const emit = defineEmits<{ disconnect: [to: string] }>()

// A handoff takes an arc of its own, so it never shares the path (and the label's place) of the "after" edge between the same two.
const path = computed(() =>
  props.data.edge.kind === 'handoff'
    ? handoffPath(props.sourceX, props.sourceY, props.targetX, props.targetY, props.sourcePosition === Position.Right ? 'LR' : 'TB')
    : getBezierPath({
    sourceX: props.sourceX,
    sourceY: props.sourceY,
    sourcePosition: props.sourcePosition,
    targetX: props.targetX,
    targetY: props.targetY,
    targetPosition: props.targetPosition,
  }),
)
const e = computed(() => props.data.edge)
const handoff = computed(() => e.value.kind === 'handoff')
const needs = computed(() => e.value.kind === 'needs')
const stroke = computed(() => (handoff.value ? 'var(--ui-info)' : needs.value ? 'var(--ui-primary)' : 'var(--ui-text-muted)'))
</script>

<template>
  <BaseEdge
    :id="id"
    :path="path[0]"
    :marker-end="markerEnd"
    :style="{ stroke, strokeWidth: handoff ? 1.5 : 1.25, strokeDasharray: handoff ? '6 4' : needs ? '2 3' : undefined }"
    :class="handoff && 'conductor-handoff-edge'"
    :data-graph-edge="`${e.from}-${e.to}`"
    :data-graph-edge-kind="e.kind"
  />
  <EdgeLabelRenderer>
    <div class="nodrag nopan absolute" :style="{ transform: `translate(-50%, -50%) translate(${path[1]}px, ${path[2]}px)`, pointerEvents: 'all' }" :data-graph-edge-label="`${e.from}-${e.to}`" :data-graph-label-kind="e.kind">
      <UPopover v-if="handoff" mode="hover" :open-delay="150">
        <UBadge :label="`${e.count ?? 0}`" color="info" variant="solid" size="sm" class="cursor-default font-mono" :aria-label="`${e.count} handoffs from ${e.from} to ${e.to}`" data-graph-handoff-count />
        <template #content>
          <div class="flex max-w-72 flex-col gap-1 p-3 text-xs">
            <span class="font-semibold">{{ e.from }} → {{ e.to }} · {{ e.count }} {{ e.count === 1 ? 'handoff' : 'handoffs' }}</span>
            <span v-if="e.last" class="text-default">{{ e.last }}</span>
            <span v-if="e.lastAt" class="text-muted">{{ relativeTime(e.lastAt, data.now, { suffix: true }) }}</span>
          </div>
        </template>
      </UPopover>
      <span v-else class="flex items-center gap-0.5 rounded bg-default/90 px-1 text-[10px] text-muted">
        {{ needs ? 'needs' : 'when done' }}
        <IconButton v-if="data.editable && !needs" icon="i-lucide-x" size="xs" color="neutral" variant="ghost" class="-mr-1 size-4 p-0" :label="`${e.to} no longer starts after ${e.from}`" data-graph-edge-delete @click="emit('disconnect', e.to)" />
      </span>
    </div>
  </EdgeLabelRenderer>
</template>

<style>
.conductor-handoff-edge {
  animation: conductor-dash 1s linear infinite;
}
@keyframes conductor-dash {
  to {
    stroke-dashoffset: -10;
  }
}
</style>
