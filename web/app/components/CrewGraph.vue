<script lang="ts">
// One Vue Flow store per graph, counted over the app's life, so the editor's and another page's never share nodes (#449): a count
// inside <script setup> began at 0 for every graph, and every graph read the store crew-graph-1.
let seq = 0
</script>

<script setup lang="ts">
import { ConnectionMode, MarkerType, useVueFlow, VueFlow, type Connection, type Edge, type Node, type NodeMouseEvent } from '@vue-flow/core'
import { Background } from '@vue-flow/background'
import { Controls } from '@vue-flow/controls'
import type { CrewStart, RunInfo, RunTask } from '~/composables/useSessions'
import type { OfferRun } from '~/utils/runs'
import type { MemberStatus } from '~/utils/crews'
import { connectError, connectionAllowed, crewGraph, type GraphDirection, type GraphMember, type HandoffSeen } from '~/utils/crewGraph'
import { taskListRows } from '~/utils/taskGraph'
import { taskWords, type TaskRun } from '~/utils/tasks'
import { markLook, STATUS_KEYS, TASK_KEYS, type MarkKey, type MarkView } from '~/utils/status'
import type { MemberNodeData } from '~/components/CrewGraphNode.vue'
import type { LineStationInfo } from '~/components/RunLineDiagram.vue'
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
 *
 * A managed run's Tasks mode is drawn as lines (RunLineDiagram, UX1 PR-33): a
 * task a station lit by its state, Vue Flow mounted for the members alone;
 * under the drawing a caption for the selected task carries Open and Show in
 * Tasks. A phone lists the tasks by depth.
 */
const props = withDefaults(
  defineProps<{
    members: GraphMember[]
    /** Live member states in a run (memberStatus). */
    states?: Map<string, MemberStatus>
    /** Each member's mark in a run (memberStatusOf), by name; absent for a saved crew. */
    marks?: Map<string, MarkView>
    /** Each task's mark (taskStatus), by id. */
    taskMarks?: Map<string, MarkView>
    handoffs?: HandoffSeen[]
    editable?: boolean
    runId?: string
    stopped?: boolean
    /** The run, as what a member offers by hand (memberOffer) and its tasks' words (a continued run's, taskWords) read it. */
    run?: (OfferRun & Pick<RunInfo, 'resumedBy'>) | null
    selected?: string
    now?: number
    /** Fill the height the parent gives (a page body); else the canvas is a fixed 24 rem tall (a form). */
    fill?: boolean
    /** The agents' names by id, said on each node under the member's name. */
    agentNames?: Record<string, string>
    /** A managed run's tasks (#334): with any, the graph has a Tasks mode, the tasks drawn as lines. */
    tasks?: RunTask[]
    paused?: boolean
    /** The run's state and why it paused, for the tasks' words: they read as in the run page's task list (an interrupted run's held task). */
    runState?: string
    pausedWhy?: string
    /** The task selected in Tasks mode. */
    selectedTask?: string
  }>(),
  { states: undefined, marks: undefined, taskMarks: undefined, editable: false, runId: '', stopped: false, run: null, selected: '', now: 0, fill: false, agentNames: () => ({}), tasks: () => [], paused: false, runState: '', pausedWhy: '', selectedTask: '' },
)
const emit = defineEmits<{
  select: [name: string]
  open: [name: string]
  start: [name: string]
  /** Tasks mode: a task clicked, one opened (Enter, a double click, the caption's Open), and one shown in the Tasks view. */
  selectTask: [id: string]
  openTask: [id: string]
  showTask: [id: string]
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
// The switches are the app's screens' state (useUiState): kept by the server too, and taken up when its copy arrives.
const ui = useUiState()
watch(
  prefs,
  (p) => {
    try {
      localStorage.setItem(STORAGE_KEY, JSON.stringify(p))
    } catch {
      /* storage refused: the choice lasts the page */
    }
    ui.setLayout({ crewGraph: { ...p } })
  },
  { deep: true },
)
watch(
  () => ui.layout.value.crewGraph,
  (g) => {
    if (!g) return
    const next = { direction: g.direction ?? prefs.value.direction, handoffs: g.handoffs ?? prefs.value.handoffs, members: g.members ?? prefs.value.members }
    if (next.direction !== prefs.value.direction || next.handoffs !== prefs.value.handoffs || next.members !== prefs.value.members) prefs.value = next
  },
)

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
const taskRows = computed(() => taskListRows(props.tasks))
const taskRun = computed<TaskRun>(() => ({ state: props.runState, stoppedAt: props.stopped ? 'stopped' : undefined, paused: props.paused, pausedWhy: props.pausedWhy, resumedBy: props.run?.resumedBy }))
function taskWordsOf(t: RunTask) {
  return taskWords(t, taskRun.value)
}
const warnings = computed(() => new Map(taskRows.value.flatMap((n) => (n.warning ? [[n.id, n.warning] as const] : []))))
// The stations: each task with its needs, its state, its mark (taskMarks: taskStatus, its attempt's session's state while it runs) and its words.
const stations = computed<LineStationInfo[]>(() =>
  props.tasks.map((t) => ({
    id: t.id,
    needs: t.needs,
    state: t.state,
    mark: props.taskMarks?.get(t.id),
    words: warnings.value.get(t.id) ?? taskWordsOf(t).text,
  })),
)
const caption = computed(() => {
  const t = props.tasks.find((x) => x.id === props.selectedTask)
  if (!t) return undefined
  const words = warnings.value.get(t.id) ?? taskWordsOf(t).text
  return { t, words, attempt: t.attempts ? `attempt ${t.attempt?.n ?? t.attempts}` : '', session: t.attempt?.sessionId ?? '' }
})
const diagram = useTemplateRef<{ fit: () => void }>('diagram')
function fit() {
  if (tasksMode.value) diagram.value?.fit()
  else fitView({ padding: 0.2 })
}

const flowNodes = computed<Node<MemberNodeData>[]>(() =>
  tasksMode.value
    ? []
    : graph.value.nodes.map((n) => ({
        id: n.id,
        type: 'member',
        position: { x: n.x, y: n.y },
        data: { node: n, direction: prefs.value.direction, editable: props.editable, runId: props.runId, stopped: props.stopped, run: props.run, agentName: props.agentNames[n.member.agentId] ?? '', mark: props.marks?.get(n.id) },
        connectable: props.editable,
        draggable: false,
        selected: n.id === props.selected,
      })),
)
// The edges Vue Flow draws: the start rules and the run's handoffs (Tasks mode draws its lines itself).
const drawnEdges = computed(() => (tasksMode.value ? [] : graph.value.edges))
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
  nextTick(() => !tasksMode.value && fitView({ padding: 0.2 }))
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
  () => nextTick(() => !tasksMode.value && fitView({ padding: 0.2 })),
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
  emit('select', e.node.id)
}
function onNodeDoubleClick(e: NodeMouseEvent) {
  emit('open', e.node.id)
}

/**
 * The legend: one line a key, made from the status table (STATUS_LOOK and TASK_LOOK, in their order), listing only the keys the graph
 * draws now (the tasks' marks in Tasks mode, else the members'); in Tasks mode the three kinds of line after them.
 */
const legend = computed(() => {
  const drawn = new Set<MarkKey>([...((tasksMode.value ? props.taskMarks : props.marks)?.values() ?? [])].map((m) => m.key))
  return [...STATUS_KEYS, ...TASK_KEYS].filter((k) => drawn.has(k)).map((k) => ({ key: k, look: markLook(k) }))
})
const lineLegend = [
  { key: 'line', label: 'a line: tasks that follow one another' },
  { key: 'branch', label: 'a branch: a task that starts from a result another line goes on from' },
  { key: 'join', label: "a join: a task that needs more than one line's result" },
]
</script>

<template>
  <div class="flex min-h-0 flex-1 flex-col gap-2" :data-graph-mode="editable ? 'edit' : 'run'">
    <div class="flex flex-wrap items-center gap-1.5">
      <UButton v-if="wide" label="Fit" icon="i-lucide-scan" size="xs" color="neutral" variant="outline" data-graph-fit @click="fit" />
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
        <UButton label="Legend" icon="i-lucide-info" size="xs" color="neutral" variant="ghost" class="ml-auto" data-graph-legend-open />
        <template #content>
          <dl class="grid max-w-sm grid-cols-[auto_1fr] items-center gap-x-2 gap-y-1 p-3 text-xs" data-graph-legend-list>
            <template v-for="l in legend" :key="l.key">
              <dt class="flex items-center"><StatusMark :status="{ key: l.key, word: l.look.word, legend: l.look.legend }" /></dt>
              <dd :data-graph-legend="l.key">{{ l.look.legend }}</dd>
            </template>
            <template v-if="tasksMode">
              <template v-for="l in lineLegend" :key="l.key">
                <dt class="w-4" :data-graph-legend-line="l.key">
                  <svg viewBox="0 0 16 10" class="h-2.5 w-4" fill="none" stroke="var(--ui-text-muted)" stroke-width="1.5" aria-hidden="true">
                    <path v-if="l.key === 'line'" d="M1 5 H15" />
                    <path v-else-if="l.key === 'branch'" d="M1 2 H15 M5 2 Q8 2 8 5 Q8 8 11 8 H15" />
                    <path v-else d="M1 2 H15 M1 8 H5 Q8 8 8 5 Q8 2 11 2" />
                  </svg>
                </dt>
                <dd>{{ l.label }}</dd>
              </template>
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
            <StatusMark v-if="taskMarks?.get(n.id)" :status="taskMarks.get(n.id)!" word class="flex-none" />
            <span class="ml-auto truncate text-xs text-muted">{{ n.task.member }}</span>
          </div>
          <span class="truncate text-xs" :class="n.warning ? 'text-warning' : 'text-muted'">{{ n.warning ?? taskWordsOf(n.task).text }}</span>
        </div>
      </li>
    </ol>
    <CrewGraphList v-else-if="!wide" :graph="graph" :marks="marks" :editable="editable" :run="run" :run-id="runId" :selected="selected" @open="emit('open', $event)" @start="emit('start', $event)" @select="emit('select', $event)" />
    <!-- Tasks mode: the run as lines, fitted to the pane, which scrolls inside itself past half size; the selected task's caption under it. -->
    <template v-else-if="tasksMode">
      <div class="relative rounded-md border border-default bg-elevated [--line-station-bg:var(--ui-bg-elevated)]" :class="fill ? 'min-h-72 flex-1' : 'h-96'" data-graph-canvas data-graph-lines>
        <RunLineDiagram ref="diagram" :stations="stations" :direction="prefs.direction" :selected="selectedTask" :label="`The run's ${tasks.length} tasks as lines`" @select="emit('selectTask', $event)" @open="emit('openTask', $event)" />
      </div>
      <div class="flex min-h-8 min-w-0 flex-wrap items-center gap-x-3 gap-y-1 text-xs" :data-station-caption="caption?.t.id">
        <template v-if="caption">
          <span class="font-mono font-semibold text-highlighted">{{ caption.t.id }}</span>
          <span class="text-muted">{{ caption.t.member }}</span>
          <span class="min-w-0 truncate" :title="caption.words" data-station-words>{{ caption.words }}</span>
          <span v-if="caption.attempt" class="font-mono text-muted" data-station-attempt>{{ caption.attempt }}</span>
          <span class="ml-auto flex flex-none items-center gap-1">
            <UButton v-if="caption.session" label="Open" icon="i-lucide-terminal" size="xs" color="neutral" variant="outline" data-station-open @click="emit('openTask', caption.t.id)" />
            <UButton label="Show in Tasks" icon="i-lucide-list-checks" size="xs" color="neutral" variant="ghost" data-station-show @click="emit('showTask', caption.t.id)" />
          </span>
        </template>
        <span v-else class="text-muted" data-station-hint>Select a task to see what it does now; Enter or a double click opens it.</span>
      </div>
    </template>
    <div v-else class="relative overflow-hidden rounded-md border border-default bg-elevated/30" :class="fill ? 'min-h-72 flex-1' : 'h-96'" data-graph-canvas :data-graph-store="flowId">
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
.vue-flow__node-member {
  padding: 0;
  border: 0;
  background: transparent;
  width: auto;
}
</style>
