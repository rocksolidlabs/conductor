<script setup lang="ts">
import { Handle, Position, type NodeProps } from '@vue-flow/core'
import type { GraphDirection } from '~/utils/crewGraph'
import type { TaskNode } from '~/utils/taskGraph'
import { taskDot, taskWords, type TaskRun } from '~/utils/tasks'

/**
 * One task on a managed run's graph (#334, row 7): its kind, id and state
 * dot, its member, which attempt runs, and what it does now in words; Open
 * goes to the attempt's session. The target handle takes the edges from
 * the tasks it needs. Enter opens it; the graph handles clicks.
 */
export interface TaskNodeData {
  node: TaskNode
  direction: GraphDirection
  /** Its run as its words read it: the same as the run page's task list (RunTasks), an interrupted run's held task among them. */
  run: TaskRun
  /** The member's agent, for its avatar. */
  agentId: string
}

const props = defineProps<NodeProps<TaskNodeData>>()
const emit = defineEmits<{ open: [id: string] }>()

const n = computed(() => props.data.node)
const t = computed(() => n.value.task)
const targetPos = computed(() => (props.data.direction === 'LR' ? Position.Left : Position.Top))
const sourcePos = computed(() => (props.data.direction === 'LR' ? Position.Right : Position.Bottom))
const words = computed(() => taskWords(t.value, props.data.run))
const tone = { warning: 'text-warning', error: 'text-error', muted: 'text-muted', default: 'text-default', success: 'text-success' }
</script>

<template>
  <div
    class="flex h-24 w-60 flex-col gap-1 rounded-lg border bg-default p-2.5 text-left shadow-xs outline-none transition-shadow focus-visible:ring-2 focus-visible:ring-primary"
    :class="[selected ? 'border-primary ring-1 ring-primary' : t.state === 'failed' ? 'border-error' : t.state === 'blocked' || t.state === 'ready' ? 'border-dashed border-accented' : 'border-default', t.state === 'cancelled' && 'opacity-70']"
    tabindex="0"
    :data-graph-task="t.id"
    :data-task-state="t.state"
    :aria-label="`${t.id}: ${words.text}`"
    @keydown.enter.prevent="emit('open', t.id)"
  >
    <Handle type="target" :position="targetPos" :connectable="false" class="!size-2.5 !border-2 !border-default !bg-primary" :class="!t.needs?.length && '!opacity-0'" />
    <div class="flex min-w-0 items-center gap-2">
      <UIcon :name="t.kind === 'review' ? 'i-lucide-search-check' : 'i-lucide-hammer'" class="size-4 flex-none text-muted" :title="t.kind" />
      <span class="max-w-[8.5rem] flex-none truncate font-mono text-[13px] font-semibold text-highlighted">{{ t.id }}</span>
      <span class="size-2 flex-none rounded-full" :class="taskDot(t.state)" :title="words.text" aria-hidden="true" />
      <UBadge v-if="t.state === 'failed'" label="failed" color="error" variant="subtle" size="sm" class="ml-auto flex-none" />
      <UBadge v-else-if="t.state === 'succeeded'" label="done" color="success" variant="subtle" size="sm" class="ml-auto flex-none" />
    </div>
    <div class="flex min-w-0 items-center gap-2 text-[11.5px] text-muted">
      <SessionAvatar :agent-id="data.agentId" />
      <span class="truncate">{{ t.member }}</span>
      <span v-if="t.attempts" class="ml-auto flex-none font-mono text-[11px]">attempt {{ t.attempt?.n ?? t.attempts }}</span>
    </div>
    <div class="mt-auto flex min-w-0 items-center gap-1.5 text-[11px]">
      <span v-if="n.warning" class="truncate text-warning" :title="n.warning">{{ n.warning }}</span>
      <span v-else class="truncate" :class="tone[words.tone]" :title="words.text">{{ words.text }}</span>
      <UButton v-if="t.attempt?.sessionId" label="Open" icon="i-lucide-terminal" size="xs" color="neutral" variant="ghost" class="nodrag ml-auto" @click.stop="emit('open', t.id)" />
    </div>
    <Handle type="source" :position="sourcePos" :connectable="false" class="!size-2.5 !border-2 !border-default !bg-primary !opacity-0" />
  </div>
</template>
