<script setup lang="ts">
import { layout, type GraphMember } from '~/utils/crewGraph'
import type { MemberMark } from '~/utils/status'
import { agentInitials } from '~/utils/sessions'

/**
 * A crew's shape at a glance: its members as small chips in the columns of
 * the graph's layout (roots on the left), joined by their "after" edges.
 * `chip` (a run card) shows each member's initials, name and status mark;
 * `mini` (the saved crews table) only the initials, dashed for a member
 * started by hand. Wider than `maxWidth`, it is scaled down. Nothing to click.
 */
const props = withDefaults(defineProps<{ members: GraphMember[]; size?: 'chip' | 'mini'; states?: Map<string, MemberMark>; maxWidth?: number }>(), {
  size: 'mini',
  states: undefined,
  maxWidth: 0,
})

const dims = computed(() => (props.size === 'chip' ? { w: 96, h: 24, cs: 124, rs: 30 } : { w: 32, h: 14, cs: 46, rs: 18 }))
const g = computed(() => layout(props.members))
const nodes = computed(() =>
  g.value.nodes.map((n) => ({
    n,
    x: n.depth * dims.value.cs,
    y: n.row * dims.value.rs,
    state: props.states?.get(n.id)?.status,
    mark: props.states?.get(n.id)?.mark,
    manual: n.member.start.when === 'manual',
  })),
)
const box = computed(() => {
  const cols = Math.max(1, ...g.value.nodes.map((n) => n.depth + 1))
  const rows = Math.max(1, ...g.value.nodes.map((n) => n.row + 1))
  return { w: (cols - 1) * dims.value.cs + dims.value.w, h: (rows - 1) * dims.value.rs + dims.value.h }
})
const scale = computed(() => (props.maxWidth && box.value.w > props.maxWidth ? props.maxWidth / box.value.w : 1))
const pos = (id: string) => nodes.value.find((x) => x.n.id === id)
/** An elbow from the parent's right edge to the child's left edge, at each one's middle. */
const edges = computed(() =>
  g.value.edges
    .filter((e) => e.kind === 'after')
    .map((e) => {
      const a = pos(e.from)
      const b = pos(e.to)
      if (!a || !b) return null
      const ax = a.x + dims.value.w
      const ay = a.y + dims.value.h / 2
      const bx = b.x
      const by = b.y + dims.value.h / 2
      const mid = (ax + bx) / 2
      return { id: e.id, d: ay === by ? `M${ax} ${ay}H${bx - 4}` : `M${ax} ${ay}H${mid}V${by}H${bx - 4}` }
    })
    .filter((e) => e !== null),
)
const marker = `dag-arrow-${useId()}`

function chipClass(x: (typeof nodes.value)[number]): string {
  if (props.size === 'mini') return x.manual ? 'border border-dashed border-accented bg-elevated text-default' : 'bg-elevated text-default'
  if (x.state === 'needs_input') return 'border border-warning bg-warning/10 text-highlighted'
  if (x.state === 'pending' || (!x.state && x.manual)) return 'border border-dashed border-accented text-muted'
  if (x.state === 'ended') return 'border border-default text-muted'
  return 'border border-default text-default'
}
</script>

<template>
  <div class="relative flex-none" :style="{ width: `${box.w * scale}px`, height: `${box.h * scale}px` }" aria-hidden="true" data-crew-mini-dag>
    <div class="absolute left-0 top-0 origin-top-left" :style="{ width: `${box.w}px`, height: `${box.h}px`, transform: scale === 1 ? undefined : `scale(${scale})` }">
      <svg class="absolute inset-0 overflow-visible text-accented" :width="box.w" :height="box.h">
        <defs>
          <marker :id="marker" viewBox="0 0 6 6" refX="5" refY="3" markerWidth="5" markerHeight="5" orient="auto">
            <path d="M0 0L6 3L0 6z" fill="currentColor" />
          </marker>
        </defs>
        <path v-for="e in edges" :key="e.id" :d="e.d" fill="none" stroke="currentColor" stroke-width="1.5" :marker-end="`url(#${marker})`" />
      </svg>
      <span
        v-for="x in nodes"
        :key="x.n.id"
        class="absolute box-border flex items-center rounded-md"
        :class="[chipClass(x), size === 'chip' ? 'gap-1.5 pl-1 pr-2 text-[12.5px]' : 'justify-center font-mono text-[8px] font-medium']"
        :style="{ left: `${x.x}px`, top: `${x.y}px`, width: `${dims.w}px`, height: `${dims.h}px` }"
        :title="`${x.n.id} · ${x.n.member.agentId}`"
      >
        <template v-if="size === 'chip'">
          <span class="grid size-4 flex-none place-items-center rounded bg-elevated font-mono text-[8px] font-semibold text-primary">{{ agentInitials(x.n.member.agentId) }}</span>
          <span class="truncate">{{ x.n.id }}</span>
          <StatusMark v-if="x.mark" :status="x.mark" class="ml-auto" :data-run-member-mark="x.n.id" />
        </template>
        <template v-else>{{ agentInitials(x.n.member.agentId) }}</template>
      </span>
    </div>
  </div>
</template>
