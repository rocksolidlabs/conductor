<script setup lang="ts">
import { ConnectionMode, MarkerType, useVueFlow, VueFlow, type Connection, type Edge, type Node, type NodeMouseEvent } from '@vue-flow/core'
import { Background } from '@vue-flow/background'
import { Controls } from '@vue-flow/controls'
import type { CrewStart, RunTask } from '~/composables/useSessions'
import type { MemberStatus } from '~/utils/crews'
import { connectError, connectionAllowed, crewGraph, type GraphDirection, type GraphMember, type HandoffSeen } from '~/utils/crewGraph'
import { taskLayout, taskListRows } from '~/utils/taskGraph'
import { taskDot, taskWords } from '~/utils/tasks'
import type { MemberNodeData } from '~/components/CrewGraphNode.vue'
import type { TaskNodeData } from '~/components/CrewGraphTaskNode.vue'
import type { CrewEdgeData } from '~/components/CrewGraphEdge.vue'

/**
 * The crew graph: members as nodes laid out by their start rules (roots on
 * the left), solid edges for "after X is done", dashed edges for the handoffs
 * of a run. On a run page it shows the members' live states and offers
 * Start now, Resume and Open; in the crew editor (`editable`) dragging from
 * one member's right handle to another's left sets "starts after", the
 * edge's × deletes the rule, and a node's menu sets the rule by hand. The
 * server's rules hold here too: one parent, no self edge, no cycle. On a
 * phone (under md) the graph is a list with the same badges.
 */
const props = withDefaults(
  defineProps<{
    members: GraphMember[]
    /** Live member states in a run (memberStatus). */
    states?: Map<string, MemberStatus>
    handoffs?: HandoffSeen[]
    editable?: boolean
    runId?: string
    stopped?: boolean
    selected?: string
    now?: number
    /** Fill the height the parent gives (a page body); else the canvas is a fixed 24 rem tall (a form). */
    fill?: boolean
    /** The agents' names by id, said on each node under the member's name. */
    agentNames?: Record<string, string>
    /** A managed run's tasks (#334): with any, the graph has a Tasks mode, one node per task and an edge for each need. */
    tasks?: RunTask[]
    paused?: boolean
    /** The task selected in Tasks mode. */
    selectedTask?: string
  }>(),
  { editable: false, runId: '', stopped: false, selected: '', now: 0, fill: false, agentNames: () => ({}), tasks: () => [], paused: false, selectedTask: '' },
)
const emit = defineEmits<{
  select: [name: string]
  open: [name: string]
  start: [name: string]
  /** Tasks mode: a task clicked, and one opened (Enter, a double click, Open). */
  selectTask: [id: string]
  openTask: [id: string]
  /** `to` now starts after `from`, or on its own again (start given). */
  setStart: [name: string, start: CrewStart]
  remove: [name: string]
  /** A rule the editor refused, in words for a toast. */
  refused: [message: string]
}>()

const STORAGE_KEY = 'conductor.crewGraph'
function readPrefs(): { direction: GraphDirection; handoffs: boolean; members: boolean } {
  try {
    const raw = JSON.parse(localStorage.getItem(STORAGE_KEY) ?? '{}') as { direction?: unknown; handoffs?: unknown; members?: unknown }
    return { direction: raw.direction === 'TB' ? 'TB' : 'LR', handoffs: raw.handoffs !== false, members: raw.members === true }
  } catch {
    return { direction: 'LR', handoffs: true, members: false }
  }
}
const prefs = ref(readPrefs())
watch(
  prefs,
  (p) => {
    try {
      localStorage.setItem(STORAGE_KEY, JSON.stringify(p))
    } catch {
      /* storage refused: the choice lasts the page */
    }
  },
  { deep: true },
)

// One Vue Flow store per graph, so the editor's and another page's never share nodes.
let seq = 0
const flowId = `crew-graph-${++seq}`
const { setNodes, setEdges, fitView } = useVueFlow({ id: flowId })

const graph = computed(() => crewGraph(props.members, { direction: prefs.value.direction, states: props.states, handoffs: props.editable || !prefs.value.handoffs ? [] : props.handoffs }))

// A managed run's graph shows its tasks unless the person switched to the members; a run without tasks has members alone.
const tasksMode = computed(() => props.tasks.length > 0 && !prefs.value.members)
const modeItems = [
  { label: 'Tasks', value: 'tasks', icon: 'i-lucide-list-checks' },
  { label: 'Members', value: 'members', icon: 'i-lucide-users' },
]
const mode = computed({
  get: () => (tasksMode.value ? 'tasks' : 'members'),
  set: (v: string) => (prefs.value.members = v === 'members'),
})
const taskGraph = computed(() => taskLayout(props.tasks, prefs.value.direction))
const taskRows = computed(() => taskListRows(taskGraph.value))
function agentOf(member: string): string {
  return props.members.find((m) => m.name === member)?.agentId ?? ''
}
function taskWordsOf(t: RunTask) {
  return taskWords(t, { stoppedAt: props.stopped ? 'stopped' : undefined, paused: props.paused })
}

const flowNodes = computed<Node<MemberNodeData | TaskNodeData>[]>(() =>
  tasksMode.value
    ? taskGraph.value.nodes.map((n) => ({
        id: n.id,
        type: 'task',
        position: { x: n.x, y: n.y },
        data: { node: n, direction: prefs.value.direction, stopped: props.stopped, paused: props.paused, agentId: agentOf(n.task.member) },
        connectable: false,
        draggable: false,
        selected: n.id === props.selectedTask,
      }))
    : graph.value.nodes.map((n) => ({
        id: n.id,
        type: 'member',
        position: { x: n.x, y: n.y },
        data: { node: n, direction: prefs.value.direction, editable: props.editable, runId: props.runId, stopped: props.stopped, agentName: props.agentNames[n.member.agentId] ?? '' },
        connectable: props.editable,
        draggable: false,
        selected: n.id === props.selected,
      })),
)
// The edges this graph draws: the tasks' needs in Tasks mode, else the start rules and the run's handoffs.
const drawnEdges = computed(() => (tasksMode.value ? taskGraph.value.edges : graph.value.edges))
const flowEdges = computed<Edge<CrewEdgeData>[]>(() =>
  drawnEdges.value.map((e) => ({
    id: e.id,
    type: 'crew',
    source: e.from,
    target: e.to,
    data: { edge: e, editable: props.editable, now: props.now || Date.now() },
    markerEnd: MarkerType.ArrowClosed,
    animated: e.kind === 'handoff',
    selectable: false,
    focusable: false,
  })),
)

// The store takes the nodes and edges as they change; the view refits when the members or the direction change.
const wide = ref(true)
let media: MediaQueryList | undefined
function onMedia(e: MediaQueryListEvent | MediaQueryList) {
  wide.value = e.matches
}
onMounted(() => {
  media = window.matchMedia('(min-width: 768px)')
  onMedia(media)
  media.addEventListener('change', onMedia)
  // The canvas has its size now: fit once more than init did.
  nextTick(() => fitView({ padding: 0.2 }))
})
onBeforeUnmount(() => media?.removeEventListener('change', onMedia))

watch(
  [flowNodes, flowEdges],
  ([nodes, edges]) => {
    setNodes(nodes)
    setEdges(edges)
  },
  { immediate: true },
)
watch(
  () => [props.members.length, props.tasks.length, tasksMode.value, prefs.value.direction, wide.value] as const,
  () => nextTick(() => fitView({ padding: 0.2 })),
  { flush: 'post' },
)

// Vue Flow runs this on every edge the graph hands it, not only on a connection the person draws: the graph's own edges stay (#281).
function isValidConnection(c: Connection): boolean {
  return connectionAllowed(props.members, drawnEdges.value, props.editable, c.source, c.target)
}

function onConnect(c: Connection) {
  const why = connectError(props.members, c.source, c.target)
  if (why) {
    emit('refused', why)
    return
  }
  emit('setStart', c.target, { when: 'after', member: c.source })
}

/** A connection dropped on a node that cannot take it: say why (Vue Flow refuses it silently). */
function onConnectEnd(ev?: MouseEvent | TouchEvent) {
  const target = (ev?.target as HTMLElement | null)?.closest?.('[data-graph-node]')?.getAttribute('data-graph-node')
  const from = connecting.value
  connecting.value = ''
  if (!from || !target || target === from) return
  const why = connectError(props.members, from, target)
  if (why) emit('refused', why)
}
const connecting = ref('')
function onConnectStart(p: { nodeId?: string | null }) {
  connecting.value = p.nodeId ?? ''
}

function onNodeClick(e: NodeMouseEvent) {
  if (tasksMode.value) emit('selectTask', e.node.id)
  else emit('select', e.node.id)
}
function onNodeDoubleClick(e: NodeMouseEvent) {
  if (tasksMode.value) emit('openTask', e.node.id)
  else emit('open', e.node.id)
}

const memberLegend = [
  { dot: 'bg-success', label: 'running' },
  { dot: 'bg-warning', label: 'needs input' },
  { dot: 'bg-info', label: 'starting' },
  { dot: 'bg-neutral-400', label: 'ended' },
  { dot: 'bg-transparent ring-1 ring-neutral-400', label: 'pending' },
]
const taskLegend = [
  { dot: taskDot('running'), label: 'an attempt runs' },
  { dot: taskDot('verifying'), label: 'checking a result' },
  { dot: taskDot('succeeded'), label: 'done' },
  { dot: taskDot('failed'), label: 'failed' },
  { dot: taskDot('ready'), label: 'ready, or cancelled' },
  { dot: taskDot('blocked'), label: 'waiting for a result' },
]
const legend = computed(() => (tasksMode.value ? taskLegend : memberLegend))
</script>

<template>
  <div class="flex min-h-0 flex-1 flex-col gap-2" :data-graph-mode="editable ? 'edit' : 'run'">
    <div class="flex flex-wrap items-center gap-1.5">
      <UButton v-if="wide" label="Fit" icon="i-lucide-scan" size="xs" color="neutral" variant="outline" data-graph-fit @click="fitView({ padding: 0.2 })" />
      <UButton
        v-if="wide"
        :label="prefs.direction === 'LR' ? 'Left to right' : 'Top to bottom'"
        :icon="prefs.direction === 'LR' ? 'i-lucide-arrow-right' : 'i-lucide-arrow-down'"
        size="xs"
        color="neutral"
        variant="outline"
        data-graph-direction
        @click="prefs.direction = prefs.direction === 'LR' ? 'TB' : 'LR'"
      />
      <UTabs v-if="tasks.length" v-model="mode" :items="modeItems" :content="false" size="xs" color="neutral" data-graph-mode-switch />
      <USwitch v-if="!editable && !tasksMode" v-model="prefs.handoffs" label="Handoffs" size="xs" data-graph-handoffs />
      <span v-if="editable && wide" class="ml-1.5 text-xs text-muted" data-graph-hint>Drag from a member's right edge onto another to make it wait. One "after" each; loops can't be drawn.</span>
      <UPopover v-if="!editable" :content="{ align: 'end' }">
        <UButton label="Legend" icon="i-lucide-info" size="xs" color="neutral" variant="ghost" class="ml-auto" />
        <template #content>
          <dl class="grid grid-cols-[auto_1fr] items-center gap-x-2 gap-y-1 p-3 text-xs">
            <template v-for="l in legend" :key="l.label">
              <dt class="size-2 rounded-full" :class="l.dot" />
              <dd>{{ l.label }}</dd>
            </template>
            <template v-if="tasksMode">
              <dt class="h-px w-4 border-t border-dotted border-(--ui-primary)" />
              <dd>needs the result of the task it points from</dd>
            </template>
            <template v-else>
              <dt class="h-px w-4 bg-(--ui-text-muted)" />
              <dd>starts when the member it points from is done</dd>
              <dt class="h-px w-4 border-t border-dashed border-(--ui-info)" />
              <dd>handoffs in this run, with their count</dd>
            </template>
          </dl>
        </template>
      </UPopover>
    </div>

    <p v-if="!members.length" class="rounded-md border border-dashed border-accented p-6 text-center text-sm text-muted" data-graph-empty>No members yet.</p>
    <p v-else-if="members.length === 1 && !editable" class="text-xs text-muted" data-graph-solo>One member: nothing waits on anything.</p>

    <!-- A phone in Tasks mode: the tasks as a list, one after the tasks it needs. -->
    <ol v-if="!wide && tasksMode" class="flex flex-col gap-1.5" data-graph-task-list>
      <li v-for="n in taskRows" :key="n.id" :style="{ paddingLeft: `${n.depth * 16}px` }" :data-graph-task-row="n.id" :data-depth="n.depth">
        <div
          class="flex flex-col gap-1 rounded-md border px-2.5 py-2"
          :class="selectedTask === n.id ? 'border-primary bg-primary/5' : 'border-default bg-elevated/30'"
          role="button"
          tabindex="0"
          @click="emit('selectTask', n.id)"
          @keydown.enter.prevent="emit('openTask', n.id)"
        >
          <div class="flex items-center gap-2">
            <UIcon :name="n.task.kind === 'review' ? 'i-lucide-search-check' : 'i-lucide-hammer'" class="size-4 flex-none text-muted" />
            <span class="truncate font-mono text-sm font-semibold">{{ n.id }}</span>
            <span class="size-2 flex-none rounded-full" :class="taskDot(n.task.state)" aria-hidden="true" />
            <span class="ml-auto truncate text-xs text-muted">{{ n.task.member }}</span>
          </div>
          <span class="truncate text-xs" :class="n.warning ? 'text-warning' : 'text-muted'">{{ n.warning ?? taskWordsOf(n.task).text }}</span>
        </div>
      </li>
    </ol>
    <CrewGraphList v-else-if="!wide" :graph="graph" :editable="editable" :stopped="stopped" :run-id="runId" :selected="selected" @open="emit('open', $event)" @start="emit('start', $event)" @select="emit('select', $event)" />
    <div v-else class="relative overflow-hidden rounded-md border border-default bg-elevated/30" :class="fill ? 'min-h-72 flex-1' : 'h-96'" data-graph-canvas>
      <VueFlow
        :id="flowId"
        :connection-mode="ConnectionMode.Strict"
        :is-valid-connection="isValidConnection"
        :nodes-draggable="false"
        :nodes-connectable="editable"
        :edges-updatable="false"
        :elements-selectable="true"
        :zoom-on-double-click="false"
        :min-zoom="0.4"
        :max-zoom="1.5"
        :delete-key-code="null"
        fit-view-on-init
        class="h-full w-full"
        @connect="onConnect"
        @connect-start="onConnectStart"
        @connect-end="onConnectEnd"
        @node-click="onNodeClick"
        @node-double-click="onNodeDoubleClick"
      >
        <Background :gap="16" :size="1" pattern-color="var(--ui-border)" />
        <Controls :show-interactive="false" position="bottom-right" />
        <template #node-member="p">
          <CrewGraphNode v-bind="p" @open="emit('open', $event)" @start="emit('start', $event)" @set-start="(name, when) => emit('setStart', name, { when })" @remove="emit('remove', $event)" />
        </template>
        <template #node-task="p">
          <CrewGraphTaskNode v-bind="p" @open="emit('openTask', $event)" />
        </template>
        <template #edge-crew="p">
          <CrewGraphEdge v-bind="p" @disconnect="emit('setStart', $event, { when: 'immediately' })" />
        </template>
      </VueFlow>
    </div>
  </div>
</template>

<style>
/* The canvas follows the app's theme: Vue Flow's own greys give way to the brand tokens. */
.vue-flow__controls {
  box-shadow: none;
  border: 1px solid var(--ui-border);
  border-radius: 6px;
  overflow: hidden;
}
.vue-flow__controls-button {
  background: var(--ui-bg);
  border-bottom: 1px solid var(--ui-border);
  fill: var(--ui-text);
}
.vue-flow__controls-button:hover {
  background: var(--ui-bg-elevated);
}
.vue-flow__edge.selected .vue-flow__edge-path,
.vue-flow__edge:focus .vue-flow__edge-path {
  stroke: var(--ui-primary);
}
.vue-flow__node-member,
.vue-flow__node-task {
  padding: 0;
  border: 0;
  background: transparent;
  width: auto;
}
</style>
