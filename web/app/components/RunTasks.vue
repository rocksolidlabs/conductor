<script setup lang="ts">
import type { RouteLocationRaw } from 'vue-router'
import type { RunAttempt, RunInfo, RunStep, RunTask } from '~/composables/useSessions'
import type { BrakemanRequest, BrakemanRow } from '~/utils/brakeman'
import { candidateRef, ticketPath } from '~/utils/dispatch'
import { taskReviewTarget } from '~/utils/review'
import { relativeTime } from '~/utils/sessions'
import { latestReads, ownedBy, type Owned } from '~/utils/latestReads'
import { prerequisitesRefused, stepManifest, taskActions, taskAnswer, taskRow } from '~/utils/tasks'
import { attemptSession, taskStatus } from '~/utils/status'
import type { DecisionSummary } from '~/utils/decisions'
import { ownerWords } from '~/utils/crews'
import { personPath } from '~/utils/people'
import { previewPath, TREE_SECRET_NAME } from '~/utils/capture'
import { lanesOf, tasksByLane, type RunLane } from '~/utils/compare'
import { SELECTION_WORDS, laneSelection } from '~/utils/compareSelect'

/**
 * The tasks of a managed run (#334, row 7), as a table (the UX review's
 * P7): one row per task in the crew's order, its state said once (the
 * status language's mark and word, taskStatus), the task, who (its member,
 * the attempt when it is past the first and its words do not say it, and
 * the owner only when the task names one: the crew's acceptance owner is
 * said in the header's people and the task's detail), what it does in
 * words with its result commit, and what can be done with it: Answer,
 * Retry, Accept anyway, Open and Open preview in the row, Open in Conductor
 * (the preview page), Cancel, Copy branch and Ask Brakeman why behind its
 * menu (taskRow). Ask Brakeman why asks Brakeman to explain the task at once and selects its row; the page that holds Brakeman's
 * answers (`answers`, the run page's) shows the newest about the task under the selected row, and a page that holds none (a ticket's
 * run card) says the request's id in a toast with a link to it. The
 * selected task (`?task=`) opens its result and its attempts under the rows,
 * each attempt with what it submitted and its receipts (RunAttemptSteps).
 * A run with a project manifest shows its prerequisites' receipts above the
 * rows. Cancel and Accept anyway ask first; every command carries a receipt
 * (useTaskCommands). A task a durable decision holds (Program item DR1) says
 * what it waits for, and Answer opens the decision's card in the app's
 * modal (useDecisionModal); the selected task lists its decisions. The
 * preview opens in a new tab: it is on the server's
 * loopback and not proxied, and an iframe of it would be mixed content
 * under TLS. For a run a ticket holds (RV1), the selected work task offers
 * Review, the ticket's Review view at its candidate, and a failed receipt
 * Prepare a repair there; a run whose ticket the page did not find (no
 * launch, no run link to a run of its chain) says so and to link one. A comparison's tasks (Program item CP1) are grouped by lane: each
 * lane's row names its candidate and its approach, over its candidate's row and its review's, in the one grid, and once the
 * comparison's selection is applied, "selected" or "not selected" (read from the comparison, PR-7).
 */
const props = defineProps<{
  run: RunInfo
  selected: string
  now: number
  /** The attempt a link named (`&attempt=`, a history row's: Program item HS1, PR-6), shown marked, its `step` opened with its log. */
  attempt?: number
  /** The ticket whose chain holds the run (its key), '' for none: a task's Review and Prepare a repair go to its Review view (RV1). */
  ticket?: string
  /** The receipt a link named on that attempt, `<phase>/<id>/<k>`. */
  step?: string
  /**
   * Brakeman's answer about each task, by task id (issue 993, F8): the run page's reads of the run. The selected row shows its own
   * under it. Absent where the page reads none (a ticket's run cards): an ask from a row then says its request in a toast.
   */
  answers?: Record<string, BrakemanRow>
}>()
const emit = defineEmits<{ select: [id: string]; open: [sessionId: string]; asked: [request: BrakemanRequest] }>()

const copy = useCopy()
const commands = useTaskCommands(() => props.run.id)
const api = useSessions()

const tasks = computed(() => props.run.tasks ?? [])
/**
 * The rows in their order: a comparison's tasks by lane (Program item CP1), each lane's header row, then its candidate and its review,
 * then the tasks of no lane; any other run's tasks in the crew's order.
 */
type Row = { key: string; lane: RunLane; task?: undefined } | { key: string; task: RunTask; lane?: undefined }
const rows = computed<Row[]>(() => {
  const lanes = lanesOf(props.run)
  if (!lanes.length) return tasks.value.map((t) => ({ key: `task:${t.id}`, task: t }))
  const { groups, rest } = tasksByLane(tasks.value, lanes)
  return [...groups.flatMap((g) => [{ key: `lane:${g.lane.task}`, lane: g.lane } as Row, ...g.tasks.map((t) => ({ key: `task:${t.id}`, task: t }))]), ...rest.map((t) => ({ key: `task:${t.id}`, task: t }))]
})
const selectedTask = computed(() => tasks.value.find((t) => t.id === props.selected))
/** A comparison run's comparison, read only for one: its applied selection labels the lanes. */
const comparison = useComparison({ run: () => (lanesOf(props.run).length ? props.run.id : '') }).comparison
const laneLabel = (task: string) => laneSelection(comparison.value, task)
/** The ticket's Review view at a candidate (RV1) of `run` (this run's, or the run a continuation's result came from), preparing a repair from a receipt when one is named. */
function reviewAt(run: string, task: string, attempt: number, commit: string | undefined, repair?: string): RouteLocationRaw | undefined {
  if (!props.ticket || !commit) return undefined
  return { path: ticketPath(props.ticket), query: { view: 'review', candidate: candidateRef({ run, task, attempt, commit }), ...(repair ? { repair } : {}) } }
}
/**
 * The selected work task's candidate to review: its result, in the run that made it (a result a continuation imported is its first
 * run's attempt, taskReviewTarget), else its latest attempt that submitted one.
 */
const reviewTarget = computed(() => (selectedTask.value ? taskReviewTarget(props.run.id, selectedTask.value, attempts.value) : null))
// The run's state too: a task of an interrupted run says what its continuation takes up (taskWords), a held one among them (DR1).
const live = computed(() => ({ state: props.run.state, stoppedAt: props.run.stoppedAt, paused: props.run.paused, pausedWhy: props.run.pausedWhy, record: props.run.record, resumedBy: props.run.resumedBy }))
function agentOf(member: string): string {
  return props.run.members.find((m) => m.name === member)?.agentId ?? ''
}
const short = (c?: string) => (c ? c.slice(0, 12) : '')
const acts = (t: RunTask) => taskActions(t, live.value)
/** A task's row in the table: its words without the attempt, the attempt and the owner said once, its actions in the row and in its menu. */
const rowOf = (t: RunTask) => taskRow(t, live.value, { brakeman: brakeman.available.value })
/** Whether the row draws an action (Answer, Retry, Accept, Open, Open preview or its menu): narrow, one that draws none has no line for them. */
function hasActs(t: RunTask): boolean {
  const a = acts(t)
  return !!(taskAnswer(t) || a.retry || a.accept || a.open || a.preview || rowOf(t).menu.length)
}
// Ask Brakeman why (shown where the server has Brakeman): asks it to explain the task, at once, under the person's name, and selects
// the row, under which the answer stands while it is selected. A refusal is said in a toast, in the server's words.
const brakeman = useBrakeman()
const decisionModal = useDecisionModal()
const asker = useAskBrakeman()
async function askWhy(t: RunTask) {
  if (props.selected !== t.id) emit('select', t.id)
  const body = { kind: 'explain', subjects: [`task:${props.run.id}/${t.id}`] }
  // Where the page shows the answer under the row, the row says it; elsewhere a toast names the request (or the refusal).
  const r = props.answers ? await asker.send(body).catch((e: Error) => (toast.add({ title: 'Not asked', description: e.message, icon: 'i-lucide-triangle-alert', color: 'error' }), null)) : await asker.tell(body)
  if (r) emit('asked', r)
}
const toast = useToast()
/** The answer under the task's row: the selected row's alone, when the page holds one. */
const answerOf = (t: RunTask) => (props.selected === t.id ? props.answers?.[t.id] : undefined)
/** The attempt's preview page (Open in Conductor): it frames the preview and captures it. */
const inConductor = (t: RunTask) => previewPath({ run: props.run.id, task: t.id, attempt: t.attempt!.n })
/** A preview that names its tree's secret opens at the label's admin URL, read as it is clicked; any other is a plain link. */
const opener = usePreviewOpen()
const secretPreview = (t: RunTask) => acts(t).preview.includes(TREE_SECRET_NAME)
/**
 * A candidate's preview (PV1 PR-8): Preview this candidate makes its checkout and opens the preview page at its `seq`; while it is open
 * the menu offers Open and Close. A refusal is said under the table.
 */
const candidates = useCandidatePreview(() => props.run.id)
const router = useRouter()
const candidatePath = (t: RunTask) => previewPath({ run: props.run.id, task: t.id, attempt: t.attempt!.n, seq: acts(t).previewing?.seq })
async function previewCandidate(t: RunTask) {
  const n = t.attempt!.n
  const opened = await candidates.open(t.id, n)
  if (opened) await router.push(previewPath({ run: props.run.id, task: t.id, attempt: n, seq: opened.seq }))
}
async function closeCandidate(t: RunTask) {
  const p = acts(t).previewing
  if (p) await candidates.close(t.id, t.attempt!.n, p.seq)
}
const candidateRefusals = computed(() => Object.entries(candidates.refused.value).filter(([, w]) => w))
/** The row's menu: Open in Conductor, Cancel… (red inside the menu alone), Copy branch, Ask Brakeman why, each with its hook on its element. */
function menuOf(t: RunTask) {
  return rowOf(t).menu.map((k) => {
    if (k === 'inConductor') return { label: 'Open in Conductor', icon: 'i-lucide-scan', to: inConductor(t), 'data-task-preview-open': t.id }
    if (k === 'previewCandidate') return { label: 'Preview this candidate', icon: 'i-lucide-git-commit-horizontal', 'data-task-candidate-preview': t.id, onSelect: () => void previewCandidate(t) }
    if (k === 'candidateOpen') return { label: "Open the candidate's preview", icon: 'i-lucide-scan', to: candidatePath(t), 'data-task-candidate-open': t.id }
    if (k === 'candidateClose') return { label: "Close the candidate's preview", icon: 'i-lucide-circle-stop', 'data-task-candidate-close': t.id, onSelect: () => void closeCandidate(t) }
    if (k === 'cancel') return { label: 'Cancel…', icon: 'i-lucide-x', color: 'error' as const, 'data-task-cancel': t.id, onSelect: () => (asking.value = { task: t, command: 'cancel' }) }
    if (k === 'copyBranch') return { label: 'Copy branch', icon: 'i-lucide-git-branch', 'data-task-copy-branch': t.id, onSelect: () => copy(t.attempt!.branch!, 'Branch copied') }
    return { label: 'Ask Brakeman why', icon: 'i-lucide-message-circle-question', 'data-task-ask-brakeman': t.id, onSelect: () => void askWhy(t) }
  })
}
// Each task's state in the status language (utils/status.ts, taskStatus): its attempt's session read from the live store.
const attention = useAttention()
const clock = useNow()
const statusOf = (t: RunTask) => taskStatus(t, props.run, attemptSession(t, attention.sessions.value), clock.value)
function attemptColor(state: RunAttempt['state']): 'success' | 'warning' | 'error' | 'neutral' | 'info' {
  switch (state) {
    case 'running':
    case 'succeeded':
      return 'success'
    case 'submitting':
    case 'verifying':
      return 'info'
    case 'failed':
      return 'error'
    case 'uncertain':
    case 'interrupted':
      return 'warning'
  }
  return 'neutral'
}
function severityColor(s: string): 'error' | 'warning' | 'neutral' | 'info' {
  return s === 'blocker' ? 'error' : s === 'major' ? 'warning' : s === 'minor' ? 'info' : 'neutral'
}

// The selected task's attempts, read when the selection or the run's revision changes (a run event brings the revision). Reads at
// two revisions can overlap and reply in any order: only the last one started is shown (latestReads), so an older reply that
// arrives last never puts back a receipt's earlier state on a run that has nothing more to say. What a read brings is held with
// the run and task it is of and shown only while they are the ones selected (ownedBy): another task selected before its own read
// replied, or whose read failed, shows none of the last task's attempts, so no receipt's Log asks one task's route for another's.
const taskOwner = (run: string, task: string) => JSON.stringify([run, task])
const attemptsHeld = ref<Owned<RunAttempt[]> | null>(null)
const attemptsError = ref<Owned<string> | null>(null)
const decisionsHeld = ref<Owned<DecisionSummary[]> | null>(null)
/** The selected task's decisions, as its read lists them (those it asked), with the ones that hold it and it did not ask (a gate's). */
const taskDecisions = computed(() => {
  const asked = ownedBy(decisionsHeld.value, taskOwner(props.run.id, props.selected), [])
  const held = (selectedTask.value?.holds ?? []).filter((h) => !asked.some((d) => d.id === h.id))
  return [...held.map((h) => ({ id: h.id, type: h.type, state: h.state, question: h.question, answer: undefined as string | undefined, why: undefined as string | undefined })), ...asked]
})
const attempts = computed(() => ownedBy(attemptsHeld.value, taskOwner(props.run.id, props.selected), []))
/** A link's attempt or step the selected task's read does not hold (an attempt past its count, a receipt never made): said, never guessed. */
const wantedMissing = computed(() => {
  const n = props.attempt
  if (!n || attemptsHeld.value?.owner !== taskOwner(props.run.id, props.selected)) return ''
  const a = attempts.value.find((x) => x.n === n)
  if (!a) return `${props.selected} has no attempt ${n}.`
  if (props.step && !(a.steps ?? []).some((s) => stepKey(s) === props.step)) return `Attempt ${n} has no receipt ${props.step}.`
  return ''
})
const loadError = computed(() => ownedBy(attemptsError.value, taskOwner(props.run.id, props.selected), ''))
const attemptReads = latestReads()
watch(
  () => [props.run.id, props.selected, props.run.managed?.revision] as const,
  ([run, id]) => {
    if (!id) {
      attemptReads.drop()
      attemptsHeld.value = null
      attemptsError.value = null
      decisionsHeld.value = null
      return
    }
    const owner = taskOwner(run, id)
    void attemptReads.read(
      () => api.runTask(run, id),
      (r) => {
        attemptsHeld.value = { owner, value: r.attempts }
        decisionsHeld.value = { owner, value: r.decisions ?? [] }
        attemptsError.value = null
      },
      (e) => (attemptsError.value = { owner, value: (e as Error).message }),
    )
  },
  { immediate: true },
)

// The run's prerequisites' receipts (EN1), read while the run has a project manifest, again when its revision moves; the last
// read started is the one shown, and only under the run it is of, as for the attempts.
const prereqHeld = ref<Owned<RunStep[]> | null>(null)
const prereqHeldError = ref<Owned<string> | null>(null)
const prerequisites = computed(() => ownedBy(prereqHeld.value, props.run.id, []))
const prereqError = computed(() => ownedBy(prereqHeldError.value, props.run.id, ''))
const prereqReads = latestReads()
watch(
  () => [props.run.id, !!props.run.manifest, props.run.managed?.revision] as const,
  ([id, has]) => {
    if (!has) {
      prereqReads.drop()
      prereqHeld.value = null
      prereqHeldError.value = null
      return
    }
    void prereqReads.read(
      () => api.runPrerequisites(id),
      (steps) => {
        prereqHeld.value = { owner: id, value: steps }
        prereqHeldError.value = null
      },
      (e) => (prereqHeldError.value = { owner: id, value: (e as Error).message }),
    )
  },
  { immediate: true },
)
// The prerequisites failed before any of their commands ran (a manifest env name the server does not allow or the host does
// not have): no receipt says so, the run's pause words do.
const prereqRefused = computed(() => prerequisitesRefused(props.run))
const manifestName = computed(() => {
  const m = props.run.manifest
  if (!m) return ''
  return m.source === 'file' ? `conductor.manifest.json, ${stepManifest(`file:${m.digest ?? ''}`)}` : `the crew's, crew:${m.revision ?? 0}`
})
/** A receipt as a link names it: `check/unit/1`. */
const stepKey = (s: RunStep) => `${s.phase}/${s.id}/${s.k}`
const logOf = (task: string, n: number) => (s: RunStep) => api.stepLog(props.run.id, task, n, s.id, s.k)
const prereqLog = (s: RunStep) => api.prerequisiteLog(props.run.id, s.id, s.k)
const saveName = (task: string, n: number) => (s: RunStep) => `${props.run.id}-${task}-${n}-${s.phase}-${s.id}-${s.k}`
const prereqName = (s: RunStep) => `${props.run.id}-prerequisite-${s.id}-${s.k}`

// Cancel and Accept anyway ask first.
const asking = ref<{ task: RunTask; command: 'cancel' | 'accept' } | null>(null)
const askOpen = computed({
  get: () => !!asking.value,
  set: (v: boolean) => {
    if (!v) asking.value = null
  },
})
const question = computed(() => (asking.value ? commands.question(asking.value.task, asking.value.command) : { title: '', detail: '' }))
async function confirm() {
  const a = asking.value
  if (!a) return
  await commands.send(a.task, a.command)
  asking.value = null
}
</script>

<template>
  <div class="flex min-h-0 flex-col gap-3" data-run-tasks>
    <!-- The run's project manifest and its prerequisites, run once before the first attempt (and again at an unpause). -->
    <section v-if="run.manifest" class="flex flex-col gap-1.5 rounded-md border border-default p-3 text-sm" data-run-manifest>
      <div class="flex flex-wrap items-baseline gap-x-2">
        <span class="font-medium">Project manifest</span>
        <span class="text-xs text-muted" :title="run.manifest.digest || undefined">{{ manifestName }}</span>
      </div>
      <UAlert v-if="prereqError" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="prereqError" />
      <p v-if="prereqRefused" class="whitespace-pre-wrap break-words text-xs text-error" data-run-prereq-refused>{{ prereqRefused }}</p>
      <RunAttemptSteps v-if="prerequisites.length" :steps="prerequisites" :now="now" :log="prereqLog" :save-as="prereqName" title="Prerequisites" />
      <template v-else-if="!prereqRefused">
        <p v-if="run.manifest.prerequisites?.length" class="text-xs text-muted">The prerequisites have not run yet.</p>
        <p v-else class="text-xs text-muted">No prerequisites.</p>
      </template>
    </section>
    <p v-if="!tasks.length" class="text-sm text-muted">No tasks.</p>
    <!-- The tasks as a table: state, task, who, what and actions, each read straight down (one grid template for every row, every cell
         drawn even when empty); narrower than 48 rem a row folds into state and task, then who and what, then its actions, if any. -->
    <div v-else class="task-table flex flex-col gap-1.5" data-task-table>
      <div class="task-grid task-head px-3 text-[11px] font-medium uppercase tracking-wide text-muted" aria-hidden="true" data-task-head>
        <span class="[grid-area:state]">State</span>
        <span class="[grid-area:task]">Task</span>
        <span class="[grid-area:who]">Who</span>
        <span class="[grid-area:what]">What</span>
      </div>
      <ol class="flex flex-col gap-1.5" data-task-rows>
        <template v-for="row in rows" :key="row.key">
        <!-- A comparison's lane: its candidate's task and its approach, over the lane's rows. -->
        <li v-if="row.lane" class="flex min-w-0 flex-wrap items-baseline gap-x-2 px-3 pt-1.5 text-xs" :data-task-lane="row.lane.task" :data-lane-selection="laneLabel(row.lane.task)">
          <span class="flex-none font-medium text-muted">Lane</span>
          <span class="flex-none font-mono font-semibold text-highlighted" data-mono="id">{{ row.lane.task }}</span>
          <span class="min-w-0 break-words text-muted" data-task-lane-approach>{{ row.lane.approach || 'a candidate of the comparison' }}{{ row.lane.review ? `, reviewed by ${row.lane.review}` : '' }}</span>
          <span v-if="laneLabel(row.lane.task)" class="flex-none font-medium" :class="laneLabel(row.lane.task) === 'selected' ? 'text-default' : 'text-muted'" data-task-lane-selection>{{ SELECTION_WORDS[laneLabel(row.lane.task)!] }}</span>
        </li>
        <template v-else>
        <!-- With Brakeman's answer under it, the row and its answer are one box: the border and the background move to the item. -->
        <li v-for="t in [row.task!]" :key="t.id" :class="answerOf(t) && 'rounded-md border border-primary bg-primary/5'" :data-task-row="t.id" :data-task-state="t.state">
          <!-- Enter selects the row only when the row has the focus: a control inside it (Answer, Retry, the menu) keeps its own keys. -->
          <div
            class="task-grid rounded-md px-3 py-2 text-sm"
            :class="[answerOf(t) ? 'border-0' : selected === t.id ? 'border border-primary bg-primary/5' : 'border border-default bg-elevated/30', { 'task-bare': !hasActs(t) }]"
            role="button"
            tabindex="0"
            @click="emit('select', selected === t.id ? '' : t.id)"
            @keydown.enter.self.prevent="emit('select', t.id)"
          >
            <span class="flex min-w-0 items-center [grid-area:state]" data-task-col="state"><StatusMark :status="statusOf(t)" word data-task-status /></span>
            <span class="flex min-w-0 items-center gap-2 [grid-area:task]" data-task-col="task">
              <UIcon :name="t.kind === 'review' ? 'i-lucide-search-check' : 'i-lucide-hammer'" class="size-4 flex-none text-muted" :title="t.kind" />
              <span class="min-w-0 truncate font-mono font-semibold text-highlighted" :title="t.id" data-mono="id" data-task-id>{{ t.id }}</span>
            </span>
            <span class="flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-0.5 text-muted [grid-area:who]" data-task-col="who">
              <SessionAvatar :agent-id="agentOf(t.member)" />
              <span class="truncate">{{ t.member }}</span>
              <span class="flex-none font-mono text-xs" data-task-attempt>{{ rowOf(t).attempt }}</span>
              <span v-if="rowOf(t).owner" class="flex flex-none items-center gap-1 text-xs" :data-task-owner="t.owner" :data-task-owner-from="t.ownerFrom"><UIcon name="i-lucide-user" class="size-3.5" />{{ rowOf(t).owner }}</span>
            </span>
            <span class="flex min-w-0 items-center gap-2 [grid-area:what]" data-task-col="what">
              <span class="min-w-0 flex-1 truncate text-default" :title="rowOf(t).words" data-task-words>{{ rowOf(t).words }}</span>
              <span v-if="t.result?.commit" class="flex-none font-mono text-xs text-muted" :title="t.result.commit" data-task-commit>{{ short(t.result.commit) }}</span>
            </span>
            <div class="flex min-w-0 flex-wrap items-center justify-end gap-1 [grid-area:actions]" data-task-col="actions" @click.stop>
              <UButton v-if="taskAnswer(t)" :label="`Answer ${taskAnswer(t)}`" icon="i-lucide-scale" size="xs" color="warning" variant="soft" :data-task-answer="t.id" :data-decision="taskAnswer(t)" @click="decisionModal.open(taskAnswer(t)!)" />
              <UButton v-if="acts(t).retry" label="Retry" icon="i-lucide-rotate-ccw" size="xs" color="neutral" variant="outline" :loading="commands.busy.value === `${t.id}:retry`" :data-task-retry="t.id" @click="commands.send(t, 'retry')" />
              <UButton v-if="acts(t).accept" label="Accept anyway…" icon="i-lucide-check" size="xs" color="warning" variant="outline" :data-task-accept="t.id" @click="asking = { task: t, command: 'accept' }" />
              <UButton v-if="acts(t).open" label="Open" icon="i-lucide-terminal" size="xs" color="neutral" variant="ghost" :data-task-open="t.id" @click="emit('open', t.attempt!.sessionId!)" />
              <UButton v-if="acts(t).preview && secretPreview(t)" label="Open preview" icon="i-lucide-external-link" size="xs" color="neutral" variant="ghost" :title="acts(t).preview" :data-task-preview="t.id" @pointerenter="opener.prime({ run: run.id, task: t.id, attempt: t.attempt!.n }, acts(t).preview)" @focus="opener.prime({ run: run.id, task: t.id, attempt: t.attempt!.n }, acts(t).preview)" @click="opener.newTab({ run: run.id, task: t.id, attempt: t.attempt!.n }, acts(t).preview)" />
              <UButton v-else-if="acts(t).preview" label="Open preview" icon="i-lucide-external-link" size="xs" color="neutral" variant="ghost" :to="acts(t).preview" target="_blank" rel="noopener noreferrer" external :title="acts(t).preview" :data-task-preview="t.id" />
              <UDropdownMenu v-if="rowOf(t).menu.length" :items="menuOf(t)" :content="{ align: 'end' }">
                <IconButton icon="i-lucide-ellipsis" size="xs" color="neutral" variant="ghost" :label="`More for ${t.id}`" :data-task-menu="t.id" />
              </UDropdownMenu>
            </div>
          </div>
          <BrakemanRowAnswer v-if="answerOf(t)" :request="answerOf(t)!" :run-id="run.id" :task="t" :now="clock" @asked="emit('asked', $event)" />
        </li>
        </template>
        </template>
      </ol>
    </div>

    <UAlert v-for="[key, w] in candidateRefusals" :key="key" color="neutral" variant="subtle" icon="i-lucide-circle-alert" :title="`${key.replace(':', ' attempt ')}: ${w}`" :data-task-candidate-refused="key" />

    <!-- The selected task: its result, then its attempts with what each submitted. -->
    <section v-if="selectedTask" class="flex flex-col gap-2 rounded-md border border-default p-3 text-sm" data-task-detail :data-task-detail-id="selectedTask.id">
      <div class="flex items-center gap-2">
        <span class="font-mono font-semibold text-highlighted">{{ selectedTask.id }}</span>
        <span class="truncate text-muted">{{ selectedTask.kind }} · {{ selectedTask.member }}<span v-if="selectedTask.owner" data-task-detail-owner> · <NuxtLink :to="personPath(selectedTask.owner)" class="hover:underline" data-task-detail-owner-link>{{ ownerWords(selectedTask) }}</NuxtLink></span>{{ selectedTask.needs?.length ? ` · needs ${selectedTask.needs.join(', ')}` : '' }}</span>
        <IconButton icon="i-lucide-x" size="xs" variant="ghost" color="neutral" class="ml-auto" label="Close" data-task-detail-close @click="emit('select', '')" />
      </div>
      <!-- Its candidate reviewed on the ticket that holds the run (RV1), or why not. -->
      <div v-if="selectedTask.kind === 'work' && reviewTarget" class="flex flex-wrap items-center gap-2" data-task-review-entry>
        <UButton v-if="ticket" :label="`Review on ${ticket}`" icon="i-lucide-scan-eye" size="xs" color="neutral" variant="outline" :to="reviewAt(reviewTarget.run, selectedTask.id, reviewTarget.attempt, reviewTarget.commit)" data-task-review />
        <span v-else class="text-xs text-muted" data-task-review-unlinked>
          No ticket holds this run as far as the page read: no launch, and no run link to it or to a run of its chain. Link a run of its chain to a ticket to review it: a ticket's Runs section links a run by its id (<NuxtLink to="/dispatch" class="underline">Dispatch</NuxtLink>).
        </span>
      </div>
      <p v-if="selectedTask.blocker" class="rounded-md bg-warning/10 p-2 text-warning" data-task-blocker>
        Blocked by {{ selectedTask.blocker.by }}: {{ selectedTask.blocker.reason }}<template v-if="selectedTask.blocker.message"> — {{ selectedTask.blocker.message }}</template>
      </p>
      <!-- Its durable decisions: those that hold it and those it asked, each with its state and, open, Answer. -->
      <ul v-if="taskDecisions.length" class="flex flex-col gap-1" data-task-decisions>
        <li v-for="d in taskDecisions" :key="d.id" class="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5 rounded-md px-2.5 py-1.5 ring ring-default" :data-task-decision="d.id" :data-decision-state="d.state">
          <span class="font-mono text-xs font-semibold">{{ d.id }}</span>
          <!-- Amber while it waits on a person; every other state, an applied one among them, in neutral ink (green is a live agent's). -->
          <UBadge :label="d.state" :color="d.state === 'open' ? 'warning' : 'neutral'" variant="subtle" size="sm" :data-task-decision-badge="d.state" />
          <span class="min-w-0 flex-1 break-words">{{ d.question }}</span>
          <span v-if="d.answer" class="text-xs text-muted">answered: {{ d.answer }}</span>
          <span v-else-if="d.why" class="text-xs text-muted">{{ d.why }}</span>
          <UButton :label="d.state === 'open' ? 'Answer' : 'Show'" size="xs" :color="d.state === 'open' ? 'warning' : 'neutral'" variant="ghost" :data-task-decision-open="d.id" @click="decisionModal.open(d.id)" />
        </li>
      </ul>
      <div v-if="selectedTask.result" class="flex flex-col gap-1 rounded-md bg-elevated/40 p-2" data-task-result>
        <div class="font-medium">
          Result
          <span class="font-normal text-muted">
            · attempt {{ selectedTask.result.attempt }}
            <template v-if="selectedTask.result.commit"> · <span class="font-mono" :title="selectedTask.result.commit">{{ short(selectedTask.result.commit) }}</span></template>
            <template v-if="selectedTask.result.verdict"> · {{ selectedTask.result.verdict }}</template>
          </span>
        </div>
        <p v-if="selectedTask.result.summary" class="whitespace-pre-wrap">{{ selectedTask.result.summary }}</p>
        <ul v-if="selectedTask.result.issues?.length" class="list-disc pl-5 text-muted">
          <li v-for="(i, k) in selectedTask.result.issues" :key="k">{{ i }}</li>
        </ul>
        <ul v-if="selectedTask.result.findings?.length" class="flex flex-col gap-0.5" data-result-findings>
          <li v-for="(f, k) in selectedTask.result.findings" :key="k" class="flex flex-wrap items-center gap-2">
            <UBadge :label="f.severity" size="sm" variant="subtle" :color="severityColor(f.severity)" />
            <span v-if="f.path" class="font-mono text-xs">{{ f.path }}{{ f.line ? `:${f.line}` : '' }}</span>
            <span>{{ f.message }}</span>
          </li>
        </ul>
      </div>
      <UAlert v-if="loadError" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="loadError" />
      <UAlert v-if="wantedMissing" color="neutral" variant="subtle" icon="i-lucide-search-x" :title="wantedMissing" data-task-wanted-missing />
      <ol class="flex flex-col gap-1.5" data-task-attempts>
        <li v-for="a in attempts" :key="a.n" class="flex flex-col gap-1 rounded-md border px-2.5 py-1.5" :class="attempt === a.n ? 'border-primary bg-primary/5' : 'border-default'" :data-task-attempt-row="a.n" :data-attempt-state="a.state" :data-attempt-wanted="attempt === a.n ? '' : undefined">
          <div class="flex flex-wrap items-center gap-x-2 gap-y-0.5">
            <span class="font-medium">attempt {{ a.n }}</span>
            <UBadge :label="a.state" :color="attemptColor(a.state)" variant="subtle" size="sm" />
            <span v-if="a.stage" class="text-muted">at {{ a.stage }}</span>
            <span v-if="a.start" class="font-mono text-xs text-muted" :title="`from ${a.start}`">from {{ short(a.start) }}</span>
            <span v-if="a.result" class="font-mono text-xs text-muted" :title="a.result">→ {{ short(a.result) }}</span>
            <span v-if="a.startedAt" class="ml-auto text-xs text-muted">{{ relativeTime(a.startedAt, now, { suffix: true }) }}</span>
            <UButton v-if="a.sessionId" label="Open" icon="i-lucide-terminal" size="xs" color="neutral" variant="ghost" @click="emit('open', a.sessionId)" />
          </div>
          <p v-if="a.error" class="text-error">{{ a.error }}</p>
          <p v-if="a.checkpoint" class="text-muted" data-attempt-checkpoint>
            Checkpoint at <span class="font-mono text-xs" :title="a.checkpoint.commit">{{ short(a.checkpoint.commit) }}</span> · done: {{ a.checkpoint.done }} · remaining: {{ a.checkpoint.remaining }}<template v-if="a.checkpoint.notes"> · {{ a.checkpoint.notes }}</template>
          </p>
          <template v-if="a.submitted">
            <p v-if="a.submitted.summary" class="whitespace-pre-wrap">{{ a.submitted.summary }}</p>
            <ul v-if="a.submitted.issues?.length" class="list-disc pl-5 text-muted">
              <li v-for="(i, k) in a.submitted.issues" :key="k">{{ i }}</li>
            </ul>
            <ul v-if="a.submitted.findings?.length" class="flex flex-col gap-0.5" data-attempt-findings>
              <li v-for="(f, k) in a.submitted.findings" :key="k" class="flex flex-wrap items-center gap-2">
                <UBadge :label="f.severity" size="sm" variant="subtle" :color="severityColor(f.severity)" />
                <span v-if="f.path" class="font-mono text-xs">{{ f.path }}{{ f.line ? `:${f.line}` : '' }}</span>
                <span>{{ f.message }}</span>
              </li>
            </ul>
          </template>
          <RunAttemptSteps
            v-if="a.steps?.length"
            :steps="a.steps"
            :now="now"
            :attempt="a"
            :log="logOf(selectedTask.id, a.n)"
            :save-as="saveName(selectedTask.id, a.n)"
            :open-step="attempt === a.n ? step : undefined"
            :prepare="ticket && a.result ? (s: RunStep) => reviewAt(run.id, selectedTask!.id, a.n, a.result, `${s.id}/${s.k}`) : undefined"
          />
        </li>
      </ol>
    </section>

    <UModal v-model:open="askOpen" :title="question.title" :description="question.detail">
      <template #footer>
        <div class="flex w-full justify-end gap-2">
          <UButton label="Keep it" color="neutral" variant="ghost" @click="asking = null" />
          <UButton :label="asking?.command === 'cancel' ? 'Cancel the task' : 'Accept the result'" :icon="asking?.command === 'cancel' ? 'i-lucide-x' : 'i-lucide-check'" :color="asking?.command === 'cancel' ? 'error' : 'warning'" :loading="!!commands.busy.value" data-task-confirm @click="confirm" />
        </div>
      </template>
    </UModal>
  </div>
</template>

<style scoped>
/* The task table (the UX review's P7): one template for the header and every row, so each column reads straight down. */
.task-table {
  container-type: inline-size;
}
.task-grid {
  display: grid;
  /* Narrow, a row is its state and task, then who and what, and its actions on a line of their own at the end, wrapping there when
     they must: sharing the first line, they had only what the id and who left them and were drawn over the id. A row with no action
     draws no third line. The id starts after the state, whatever who's width below it, and wraps rather than being cut. */
  grid-template-columns: auto auto minmax(0, 1fr);
  grid-template-areas:
    'state task task'
    'who who what'
    'actions actions actions';
  column-gap: 0.75rem;
  row-gap: 0.25rem;
  align-items: center;
}
.task-grid.task-bare {
  grid-template-areas:
    'state task task'
    'who who what';
}
[data-task-id] {
  white-space: normal;
  overflow-wrap: anywhere;
}
.task-grid.task-bare > [data-task-col='actions'] {
  display: none;
}
/* A button wider than its line starts at the line's start rather than running out past it. */
[data-task-col='actions'] {
  justify-content: safe flex-end;
}
.task-head {
  display: none;
}
@container (min-width: 48rem) {
  .task-grid,
  .task-grid.task-bare {
    grid-template-columns: 8.5rem minmax(0, 1.1fr) minmax(0, 1.2fr) minmax(0, 2fr) 17rem;
    grid-template-areas: 'state task who what actions';
  }
  .task-grid.task-bare > [data-task-col='actions'] {
    display: flex;
  }
  [data-task-id] {
    white-space: nowrap;
  }
  .task-head {
    display: grid;
  }
}
</style>
