<script setup lang="ts">
import type { BriefItem, RefResolution } from '~/utils/brakeman'
import { parseRef, refLabel } from '~/utils/brakeman'
import { ANSWERER, commandLabel, decisionControls, isStale, revisionOf, staleWords } from '~/utils/brakemanCards'
import type { TaskCommand } from '~/utils/tasks'

/**
 * A decision card (docs/plans/brakeman.md, PR-7): its text is the brief's (what happened, why a person is needed, the question, the
 * alternatives and their tradeoffs, the recommendation and its uncertainty, what is blocked, what answering authorizes, the evidence
 * and the revision it was read at); its controls are Conductor's own, from the live record of what it is about (utils/brakemanCards.ts):
 * an open question's options and "Answer in the session"; a task's commands by the run page's rules, each with the run's revision the
 * turn read, so a run that moved since is refused by its route and the card says what changed; a ticket's page; for a plan revision,
 * DS2's own Approve from the revision's live record (BrakemanPlanControl, PR-11), which the operator alone presses.
 *
 * In the brief's Needs you, the card leads with the question, what happened and what waits on it, then the controls, then Brakeman's
 * recommendation as words (never a control's label or fill), with the card's whole text and its chips folded under Details, and a
 * footnote of who answers, what answering authorizes and the revision every command carries. It says whether its live controls still
 * offer a command, an open question or an approval (`offered`), which the region's mark follows.
 */
const props = defineProps<{ item: Extract<BriefItem, { kind: 'decision/1' }>; index: number; refs: ReadonlyMap<string, RefResolution> }>()
const emit = defineEmits<{ offered: [offered: boolean] }>()

const live = useAttention()
const about = computed(() => parseRef(props.item.about))
const runId = computed(() => (about.value?.kind === 'task' ? about.value.run! : ''))
const commands = useTaskCommands(() => runId.value)

// The run the card is about, read when the page does not hold it (a run the server keeps, not in the sidebar's list).
onMounted(() => {
  if (runId.value && !live.runOf(runId.value)) void live.refreshRun(runId.value).catch(() => null)
})

/** The revision of `about` the turn read: its resolution's `seen`, else the card's own, which the server held equal to it. */
const seen = computed(() => props.refs.get(props.item.about)?.seen.rev || props.item.subjectRevision)
const controls = computed(() =>
  decisionControls(props.item.about, seen.value, {
    run: (id) => live.runOf(id),
    session: (id) => live.sessions.value.find((s) => s.id === id),
  }),
)
const recommended = computed(() => (typeof props.item.recommendation === 'number' && props.item.recommendation >= 0 ? props.item.recommendation : -1))
const recommends = computed(() => props.item.alternatives[recommended.value]?.label ?? '')
const names = {
  session: (id: string) => live.sessions.value.find((s) => s.id === id)?.name,
  run: (id: string) => live.runNames.value[id],
}
const waiting = computed(() => (props.item.blocked ?? []).map((r) => refLabel(r, names)).join(', '))
/** Whether a plan revision's control offers its Approve now, as BrakemanPlanControl reads the revision. */
const approvable = ref(false)
/** Whether the live controls still offer something to answer: a command, the question still asked, an approval. A ticket's page is no answer here. */
const offered = computed(() => {
  const c = controls.value
  if (c.kind === 'task') return c.commands.length > 0
  if (c.kind === 'question') return c.open
  if (c.kind === 'plan') return approvable.value
  return false
})
watch(offered, (v) => emit('offered', v), { immediate: true })
/** Answered since Brakeman read it: a task the page holds that allows no command now, or a question its session no longer asks. */
const decided = computed(() => {
  const c = controls.value
  return (c.kind === 'task' && !c.commands.length && c.expectedRevision > 0) || (c.kind === 'question' && !c.open && !!live.sessions.value.find((s) => s.id === c.session))
})
const footRevision = computed(() => {
  if (!revisionOf(seen.value)) return ''
  const what = controls.value.kind === 'task' || about.value?.kind === 'task' ? 'the run' : 'it'
  return `Read at revision ${seen.value}: refused if ${what} has moved.`
})
/** Who answers, what answering authorizes, as Brakeman wrote it, and the revision every command carries. */
const footnote = computed(() => ['You answer, as the operator.', props.item.authorizes, footRevision.value].filter(Boolean).join(' '))
const details = ref(false)

/** What the last command's route answered, in the card's words, when it refused. */
const refusal = ref('')
const asking = ref<TaskCommand | null>(null)
const askOpen = computed({
  get: () => !!asking.value,
  set: (v: boolean) => {
    if (!v) asking.value = null
  },
})
const question = computed(() => {
  const c = controls.value
  if (!asking.value || asking.value === 'retry' || c.kind !== 'task') return { title: '', detail: '' }
  const t = live.runOf(c.run)?.tasks?.find((x) => x.id === c.task)
  return t ? commands.question(t, asking.value) : { title: '', detail: '' }
})

async function send(command: TaskCommand) {
  const c = controls.value
  if (c.kind !== 'task') return
  refusal.value = ''
  asking.value = null
  let failed: unknown = null
  await commands.send({ id: c.task }, command, { expectedRevision: c.expectedRevision, refused: (e) => (failed = e) })
  const run = await live.refreshRun(c.run).catch(() => null)
  if (!failed) return
  if (isStale(failed)) {
    const t = run?.tasks?.find((x) => x.id === c.task)
    refusal.value = staleWords('run', c.expectedRevision, run?.managed?.revision, t ? `${t.id} is ${t.state}` : undefined)
  } else refusal.value = (failed as Error).message
}
function press(command: TaskCommand) {
  if (command === 'retry') void send(command)
  else asking.value = command
}
</script>

<template>
  <div class="flex flex-col gap-2.5 rounded-md border border-default p-3.5" data-brief-decision :data-decision-about="item.about" :data-decision-offered="offered ? '' : undefined">
    <div class="flex flex-wrap items-baseline gap-x-2 gap-y-1">
      <p class="min-w-0 flex-1 text-[15px] font-semibold text-highlighted" data-decision-question>{{ item.question }}</p>
      <UBadge v-if="decided" label="decided since" color="neutral" variant="soft" size="sm" data-decision-decided />
    </div>
    <p class="text-sm" data-decision-lead>{{ item.happened }}<span v-if="waiting" data-decision-waiting>{{ ` Waiting on it: ${waiting}.` }}</span></p>

    <!-- Conductor's controls, from the live record of what the card is about. -->
    <div class="flex flex-col gap-1.5" data-decision-controls :data-controls-kind="controls.kind">
      <template v-if="controls.kind === 'question'">
        <ul v-if="controls.open && controls.options.length" class="flex flex-wrap gap-1.5" data-question-options>
          <li v-for="o in controls.options" :key="o.input"><UBadge :label="o.label" color="neutral" variant="soft" size="sm" /></li>
        </ul>
        <UButton :to="controls.to" label="Answer in the session" icon="i-lucide-message-square-reply" size="sm" color="neutral" variant="outline" class="self-start max-sm:min-h-11 max-sm:w-full max-sm:justify-center" data-decision-answer />
      </template>
      <div v-else-if="controls.kind === 'task'" class="flex flex-wrap items-center gap-2">
        <UButton
          v-for="c in controls.commands"
          :key="c"
          :label="commandLabel(c)"
          :icon="c === 'retry' ? 'i-lucide-rotate-ccw' : c === 'accept' ? 'i-lucide-check' : 'i-lucide-x'"
          size="sm"
          :color="c === 'retry' ? 'neutral' : c === 'accept' ? 'warning' : 'error'"
          variant="outline"
          class="max-sm:min-h-11 max-sm:w-full max-sm:justify-center"
          :loading="commands.busy.value === `${controls.task}:${c}`"
          :data-card-command="c"
          @click="press(c)"
        />
        <UButton :to="controls.to" label="The run page" icon="i-lucide-arrow-up-right" size="sm" color="neutral" variant="ghost" class="max-sm:min-h-11 max-sm:w-full max-sm:justify-center" data-decision-run />
      </div>
      <UButton v-else-if="controls.kind === 'ticket'" :to="controls.to" label="Open the ticket" icon="i-lucide-ticket" size="sm" color="neutral" variant="outline" class="self-start max-sm:min-h-11 max-sm:w-full max-sm:justify-center" data-decision-ticket />
      <BrakemanPlanControl v-else-if="controls.kind === 'plan'" :ticket-key="controls.ticket" :n="controls.n" @offered="approvable = $event" />
      <UButton v-else-if="controls.to" :to="controls.to" label="Open its page" icon="i-lucide-arrow-up-right" size="sm" color="neutral" variant="ghost" class="self-start max-sm:min-h-11" />
      <p v-if="recommends" class="text-xs text-muted" data-brief-recommends>Brakeman recommends: {{ recommends }}</p>
      <UAlert v-if="refusal" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="refusal" data-card-refused />
    </div>

    <!-- The card's whole text and its chips, folded and kept mounted. Not drawn on a phone. -->
    <UCollapsible v-model:open="details" :unmount-on-hide="false" class="max-sm:hidden" :ui="{ content: 'flex flex-col gap-2 pt-2' }">
      <UButton label="Details" :icon="details ? 'i-lucide-chevron-down' : 'i-lucide-chevron-right'" size="xs" color="neutral" variant="ghost" class="-ml-1.5 w-fit" data-brief-decision-details />
      <template #content>
        <BrakemanDecisionText :happened="item.happened" :why-human="item.whyHuman" :answerer="ANSWERER" :authorizes="item.authorizes" :alternatives="item.alternatives" :recommended="recommended" :uncertainty="item.uncertainty" />
        <div v-if="item.blocked?.length" class="flex flex-wrap items-center gap-1.5 text-xs text-muted">
          <span>Blocked until it is decided:</span>
          <BrakemanRefChip v-for="r in item.blocked" :key="r" :ref-name="r" :resolution="refs.get(r)" />
        </div>
        <div class="flex flex-wrap items-center gap-1.5 text-xs text-muted">
          <span>Evidence</span>
          <BrakemanRefChip v-for="r in item.evidence" :key="r" :ref-name="r" :resolution="refs.get(r)" />
          <span v-if="revisionOf(seen)">· read at revision {{ seen }}</span>
        </div>
        <div class="flex flex-wrap items-center gap-1.5 text-xs text-muted">
          <BrakemanRefChip :ref-name="item.about" :resolution="refs.get(item.about)" />
          <span data-controls-words>{{ controls.words }}</span>
        </div>
      </template>
    </UCollapsible>

    <p class="text-xs text-muted" data-decision-footnote>{{ footnote }}</p>

    <UModal v-model:open="askOpen" :title="question.title" :description="question.detail">
      <template #footer>
        <div class="flex w-full justify-end gap-2">
          <UButton label="Keep it" color="neutral" variant="ghost" @click="asking = null" />
          <UButton :label="asking === 'cancel' ? 'Cancel the task' : 'Accept the result'" :icon="asking === 'cancel' ? 'i-lucide-x' : 'i-lucide-check'" :color="asking === 'cancel' ? 'error' : 'warning'" :loading="!!commands.busy.value" data-card-confirm @click="asking && send(asking)" />
        </div>
      </template>
    </UModal>
  </div>
</template>
