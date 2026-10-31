<script setup lang="ts">
import type { RunAttempt, RunInfo, RunStep, RunTask } from '~/composables/useSessions'
import { relativeTime } from '~/utils/sessions'
import { latestReads, ownedBy, type Owned } from '~/utils/latestReads'
import { prerequisitesRefused, stepManifest, taskActions, taskDot, taskWords } from '~/utils/tasks'

/**
 * The tasks of a managed run (#334, row 7): one row per task in the crew's
 * order with its state, member, attempt, what it does in words, its result
 * commit, and what can be done with it (Open the attempt's session, Open
 * preview while the attempt's worktree answers behind its project
 * manifest's preview, Retry, Accept anyway, Cancel, Copy branch). The
 * selected task (`?task=`) opens its result and its attempts under the rows,
 * each attempt with what it submitted and its receipts (RunAttemptSteps).
 * A run with a project manifest shows its prerequisites' receipts above the
 * rows. Cancel and Accept anyway ask first; every command carries a receipt
 * (useTaskCommands). The preview opens in a new tab: it is on the server's
 * loopback and not proxied, and an iframe of it would be mixed content
 * under TLS.
 */
const props = defineProps<{ run: RunInfo; selected: string; now: number }>()
const emit = defineEmits<{ select: [id: string]; open: [sessionId: string] }>()

const copy = useCopy()
const commands = useTaskCommands(() => props.run.id)
const api = useSessions()

const tasks = computed(() => props.run.tasks ?? [])
const selectedTask = computed(() => tasks.value.find((t) => t.id === props.selected))
const live = computed(() => ({ stoppedAt: props.run.stoppedAt, paused: props.run.paused, pausedWhy: props.run.pausedWhy, record: props.run.record, resumedBy: props.run.resumedBy }))
function agentOf(member: string): string {
  return props.run.members.find((m) => m.name === member)?.agentId ?? ''
}
const short = (c?: string) => (c ? c.slice(0, 12) : '')
const words = (t: RunTask) => taskWords(t, live.value)
const acts = (t: RunTask) => taskActions(t, live.value)
const tone = { warning: 'text-warning', error: 'text-error', muted: 'text-muted', default: 'text-default', success: 'text-success' }
function stateColor(state: RunTask['state']): 'success' | 'warning' | 'error' | 'neutral' | 'info' | 'primary' {
  switch (state) {
    case 'running':
      return 'success'
    case 'verifying':
      return 'info'
    case 'failed':
      return 'error'
    case 'succeeded':
      return 'primary'
  }
  return 'neutral'
}
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
const attempts = computed(() => ownedBy(attemptsHeld.value, taskOwner(props.run.id, props.selected), []))
const loadError = computed(() => ownedBy(attemptsError.value, taskOwner(props.run.id, props.selected), ''))
const attemptReads = latestReads()
watch(
  () => [props.run.id, props.selected, props.run.managed?.revision] as const,
  ([run, id]) => {
    if (!id) {
      attemptReads.drop()
      attemptsHeld.value = null
      attemptsError.value = null
      return
    }
    const owner = taskOwner(run, id)
    void attemptReads.read(
      () => api.runTask(run, id),
      (r) => {
        attemptsHeld.value = { owner, value: r.attempts }
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
    <ol v-else class="flex flex-col gap-1.5" data-task-rows>
      <li v-for="t in tasks" :key="t.id" :data-task-row="t.id" :data-task-state="t.state">
        <div
          class="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-md border px-3 py-2 text-sm"
          :class="selected === t.id ? 'border-primary bg-primary/5' : 'border-default bg-elevated/30'"
          role="button"
          tabindex="0"
          @click="emit('select', selected === t.id ? '' : t.id)"
          @keydown.enter.prevent="emit('select', t.id)"
        >
          <span class="size-2 flex-none rounded-full" :class="taskDot(t.state)" aria-hidden="true" />
          <UIcon :name="t.kind === 'review' ? 'i-lucide-search-check' : 'i-lucide-hammer'" class="size-4 flex-none text-muted" :title="t.kind" />
          <span class="font-mono font-semibold text-highlighted" data-task-id>{{ t.id }}</span>
          <UBadge :label="t.state" :color="stateColor(t.state)" variant="subtle" size="sm" class="flex-none" data-task-state-badge />
          <span class="flex flex-none items-center gap-1.5 text-muted"><SessionAvatar :agent-id="agentOf(t.member)" /><span>{{ t.member }}</span></span>
          <span v-if="t.attempts" class="flex-none font-mono text-xs text-muted" data-task-attempt>attempt {{ t.attempt?.n ?? t.attempts }}</span>
          <span class="min-w-[12rem] flex-1 truncate" :class="tone[words(t).tone]" :title="words(t).text" data-task-words>{{ words(t).text }}</span>
          <span v-if="t.result?.commit" class="flex-none font-mono text-xs text-muted" :title="t.result.commit" data-task-commit>{{ short(t.result.commit) }}</span>
          <div class="ml-auto flex flex-none items-center gap-1" @click.stop>
            <UButton v-if="acts(t).open" label="Open" icon="i-lucide-terminal" size="xs" color="neutral" variant="ghost" :data-task-open="t.id" @click="emit('open', t.attempt!.sessionId!)" />
            <UButton v-if="acts(t).preview" label="Open preview" icon="i-lucide-external-link" size="xs" color="neutral" variant="ghost" :to="acts(t).preview" target="_blank" external :title="acts(t).preview" :data-task-preview="t.id" />
            <UButton v-if="acts(t).retry" label="Retry" icon="i-lucide-rotate-ccw" size="xs" color="neutral" variant="outline" :loading="commands.busy.value === `${t.id}:retry`" :data-task-retry="t.id" @click="commands.send(t, 'retry')" />
            <UButton v-if="acts(t).accept" label="Accept anyway…" icon="i-lucide-check" size="xs" color="warning" variant="outline" :data-task-accept="t.id" @click="asking = { task: t, command: 'accept' }" />
            <UButton v-if="acts(t).cancel" icon="i-lucide-x" size="xs" color="error" variant="ghost" aria-label="Cancel the task" :data-task-cancel="t.id" @click="asking = { task: t, command: 'cancel' }" />
            <UButton v-if="acts(t).copyBranch" icon="i-lucide-git-branch" size="xs" color="neutral" variant="ghost" aria-label="Copy branch" :data-task-copy-branch="t.id" @click="copy(t.attempt!.branch!, 'Branch copied')" />
          </div>
        </div>
      </li>
    </ol>

    <!-- The selected task: its result, then its attempts with what each submitted. -->
    <section v-if="selectedTask" class="flex flex-col gap-2 rounded-md border border-default p-3 text-sm" data-task-detail :data-task-detail-id="selectedTask.id">
      <div class="flex items-center gap-2">
        <span class="font-mono font-semibold text-highlighted">{{ selectedTask.id }}</span>
        <span class="truncate text-muted">{{ selectedTask.kind }} · {{ selectedTask.member }}{{ selectedTask.needs?.length ? ` · needs ${selectedTask.needs.join(', ')}` : '' }}</span>
        <UButton icon="i-lucide-x" size="xs" variant="ghost" color="neutral" class="ml-auto" aria-label="Close" data-task-detail-close @click="emit('select', '')" />
      </div>
      <p v-if="selectedTask.blocker" class="rounded-md bg-warning/10 p-2 text-warning" data-task-blocker>
        Blocked by {{ selectedTask.blocker.by }}: {{ selectedTask.blocker.reason }}<template v-if="selectedTask.blocker.message"> — {{ selectedTask.blocker.message }}</template>
      </p>
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
      <ol class="flex flex-col gap-1.5" data-task-attempts>
        <li v-for="a in attempts" :key="a.n" class="flex flex-col gap-1 rounded-md border border-default px-2.5 py-1.5" :data-task-attempt-row="a.n" :data-attempt-state="a.state">
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
          <RunAttemptSteps v-if="a.steps?.length" :steps="a.steps" :now="now" :attempt="a" :log="logOf(selectedTask.id, a.n)" :save-as="saveName(selectedTask.id, a.n)" />
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
