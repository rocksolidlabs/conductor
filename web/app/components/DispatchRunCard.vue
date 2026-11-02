<script setup lang="ts">
import type { RunSummary } from '~/composables/useDispatch'
import type { RunInfo } from '~/composables/useSessions'
import { runTitle, shortRunId } from '~/utils/crewWords'
import { runQuestions, summaryBadges, summaryState } from '~/utils/dispatchEvidence'
import { revisionTag } from '~/utils/dispatchPlans'
import { runBadge, runState } from '~/utils/runs'
import { relativeTime } from '~/utils/sessions'
import { taskBadges } from '~/utils/tasks'

/**
 * One run of a ticket's Runs (Program item DS2, docs/plans/dispatch-evidence.md, "web/app", PR-11): its name, linked to its page, its
 * state and its tasks counted, its blockers, and the questions its members wait on now, each with Open on the member's session, counted
 * from the live sessions joined to the members (utils/dispatchEvidence.ts, runQuestions): a question needs no run event and no reload.
 * Then the run page's own task rows (RunTasks.vue, embedded, not a copy) for the run the live store holds or its record, and its
 * candidates with their receipts and repairs (DispatchCandidates). The header's own actions (a hand link's Unlink) are the slot's.
 */
const props = defineProps<{
  runId: string
  /** The run as a ticket's runs summarize it; absent for a hand link the runs read has not answered for. */
  summary?: RunSummary
  /** The run as the live store holds it, else its record; undefined while it is read, null when the server knows nothing of it. */
  run: RunInfo | null | undefined
  now: number
}>()

const live = useAttention()
const router = useRouter()

/** The run as the live store holds it (a record is none: nothing of it is live). */
const liveRun = computed(() => live.runOf(props.runId))
const title = computed(() => (props.run ? runTitle(props.run) : props.summary?.label || props.runId))
const questions = computed(() => runQuestions(props.summary ?? { record: !!props.run?.record, members: [], questions: props.run?.needsInput ?? 0 }, liveRun.value, live.sessions.value))
const state = computed(() => {
  const r = liveRun.value
  if (r) {
    const s = runState(r, live.sessions.value)
    return runBadge(s.state, questions.value.count)
  }
  if (props.summary) return summaryState(props.summary, questions.value.count)
  return null
})
const badges = computed(() => (props.run?.managed ? taskBadges(props.run) : props.summary ? summaryBadges(props.summary) : []))
const blockers = computed(() => props.summary?.blockers ?? [])
const managed = computed(() => !!props.run?.managed || Object.keys(props.summary?.tasks ?? {}).length > 0)
const ticket = computed(() => props.summary?.ticket ?? props.run?.ticket)

/** The task selected in the run's rows, its result and attempts opened under them. */
const selected = ref('')
</script>

<template>
  <div class="flex flex-col gap-2" :data-run-card="runId" :data-run-questions-count="questions.count">
    <div class="flex min-w-0 flex-wrap items-center gap-2">
      <UIcon name="i-lucide-workflow" class="size-4 flex-none text-muted" />
      <NuxtLink v-if="run || summary" :to="`/runs/${encodeURIComponent(runId)}?view=tasks`" class="min-w-0 truncate text-sm font-medium text-highlighted hover:underline" data-ticket-run-open>{{ title }}</NuxtLink>
      <span v-else-if="run === null" class="min-w-0 truncate text-sm text-muted" data-ticket-run-missing>
        <span class="font-mono">{{ runId }}</span>: the server holds no run and no record of it.
      </span>
      <span v-else class="min-w-0 truncate font-mono text-sm text-muted">Reading {{ runId }}…</span>
      <span class="flex-none font-mono text-[11px] text-dimmed" :title="runId">{{ shortRunId(runId) }}</span>
      <UBadge v-if="ticket" :label="revisionTag(ticket.planN)" color="neutral" variant="outline" size="sm" class="flex-none font-mono" :title="`Launched from ${ticket.key} at its revision ${ticket.rev}, plan revision ${ticket.planN}`" data-run-card-plan />
      <UBadge v-if="run?.record || (!run && summary?.record)" label="record" color="neutral" variant="subtle" size="sm" class="flex-none" title="A run the server no longer holds, as it was when it ended: read-only" data-ticket-run-record />
      <UBadge v-else-if="state" :label="state.label" :color="state.color" variant="subtle" size="sm" class="flex-none" data-ticket-run-state />
      <UBadge v-for="b in badges" :key="b.label" :label="b.label" :color="b.color" variant="subtle" size="sm" class="flex-none" data-ticket-run-badge />
      <span v-if="summary?.startedAt" class="text-xs text-dimmed">started {{ relativeTime(summary.startedAt, now, { suffix: true }) }}</span>
      <div class="flex-1" />
      <slot name="actions" />
    </div>

    <div v-if="questions.waiting.length || questions.count" class="flex flex-col gap-1 rounded-md bg-warning/5 p-2 ring ring-warning/30" data-run-questions :data-count="questions.count">
      <h5 class="text-xs font-medium text-warning">{{ questions.count === 1 ? 'A question waits on a person' : `${questions.count} questions wait on a person` }}</h5>
      <ul v-if="questions.waiting.length" class="flex flex-col gap-1">
        <li v-for="q in questions.waiting" :key="q.sessionId" class="flex min-w-0 flex-wrap items-center gap-2 text-xs" :data-run-question="q.member">
          <span class="flex-none font-medium">{{ q.member }}</span>
          <span class="min-w-0 flex-1 break-words text-default" data-run-question-message>{{ q.message || 'waits on a prompt' }}</span>
          <UButton label="Open" icon="i-lucide-square-arrow-out-up-right" size="xs" color="warning" variant="soft" :to="`/sessions/${encodeURIComponent(q.sessionId)}`" data-run-question-open />
        </li>
      </ul>
      <p v-else class="text-xs text-muted">As recorded: the server holds none of its members' sessions now.</p>
    </div>

    <ul v-if="blockers.length" class="flex flex-col gap-1" data-run-blockers>
      <li v-for="b in blockers" :key="b.task" class="flex min-w-0 items-start gap-1.5 text-xs" :data-run-blocker="b.task">
        <UIcon name="i-lucide-octagon-pause" class="mt-0.5 size-3.5 flex-none text-warning" />
        <span class="min-w-0 break-words"><span class="font-mono font-medium">{{ b.task }}</span> is blocked: {{ b.reason }} <span class="text-dimmed">({{ b.by }}, {{ relativeTime(b.at, now, { suffix: true }) }})</span></span>
      </li>
    </ul>

    <RunTasks v-if="run" :run="run" :selected="selected" :now="now" @select="(id: string) => (selected = id)" @open="(id: string) => router.push(`/sessions/${id}`)" />
    <DispatchCandidates v-if="managed" :run-id="runId" :revision="liveRun?.managed?.revision" :live-tasks="liveRun?.tasks" />
  </div>
</template>
