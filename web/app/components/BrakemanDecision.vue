<script setup lang="ts">
import type { BriefItem, RefResolution } from '~/utils/brakeman'
import { parseRef } from '~/utils/brakeman'
import { ANSWERER, commandLabel, decisionControls, isStale, revisionOf, staleWords } from '~/utils/brakemanCards'
import type { TaskCommand } from '~/utils/tasks'

/**
 * A decision card (docs/plans/brakeman.md, PR-7): its text is the brief's (what happened, why a person is needed, the question, the
 * alternatives and their tradeoffs, the recommendation and its uncertainty, what is blocked, what answering authorizes, the evidence
 * and the revision it was read at); its controls are Conductor's own, from the live record of what it is about (utils/brakemanCards.ts):
 * an open question's options and "Answer in the session"; a task's commands by the run page's rules, each with the run's revision the
 * turn read, so a run that moved since is refused by its route and the card says what changed; a ticket's page; for a plan revision,
 * DS2's own Approve from the revision's live record (BrakemanPlanControl, PR-11), which the operator alone presses.
 */
const props = defineProps<{ item: Extract<BriefItem, { kind: 'decision/1' }>; index: number; refs: ReadonlyMap<string, RefResolution> }>()

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
  <div class="flex flex-col gap-2 rounded-md border border-default p-3" data-brief-decision :data-decision-about="item.about">
    <p class="text-sm font-medium text-highlighted" data-decision-question>{{ item.question }}</p>
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

    <!-- Conductor's controls, from the live record of what the card is about. -->
    <div class="flex flex-col gap-1.5 border-t border-default pt-2" data-decision-controls :data-controls-kind="controls.kind">
      <BrakemanRefChip :ref-name="item.about" :resolution="refs.get(item.about)" />
      <p class="text-xs text-muted" data-controls-words>{{ controls.words }}</p>
      <template v-if="controls.kind === 'question'">
        <ul v-if="controls.open && controls.options.length" class="flex flex-wrap gap-1.5" data-question-options>
          <li v-for="o in controls.options" :key="o.input"><UBadge :label="o.label" color="neutral" variant="soft" size="sm" /></li>
        </ul>
        <UButton :to="controls.to" label="Answer in the session" icon="i-lucide-message-square-reply" size="sm" color="neutral" variant="outline" class="self-start" data-decision-answer />
      </template>
      <div v-else-if="controls.kind === 'task'" class="flex flex-wrap items-center gap-1.5">
        <UButton
          v-for="c in controls.commands"
          :key="c"
          :label="commandLabel(c)"
          :icon="c === 'retry' ? 'i-lucide-rotate-ccw' : c === 'accept' ? 'i-lucide-check' : 'i-lucide-x'"
          size="sm"
          :color="c === 'retry' ? 'neutral' : c === 'accept' ? 'warning' : 'error'"
          variant="outline"
          :loading="commands.busy.value === `${controls.task}:${c}`"
          :data-card-command="c"
          @click="press(c)"
        />
        <UButton :to="controls.to" label="The run page" icon="i-lucide-arrow-up-right" size="sm" color="neutral" variant="ghost" data-decision-run />
      </div>
      <UButton v-else-if="controls.kind === 'ticket'" :to="controls.to" label="Open the ticket" icon="i-lucide-ticket" size="sm" color="neutral" variant="outline" class="self-start" data-decision-ticket />
      <BrakemanPlanControl v-else-if="controls.kind === 'plan'" :ticket-key="controls.ticket" :n="controls.n" />
      <UButton v-else-if="controls.to" :to="controls.to" label="Open its page" icon="i-lucide-arrow-up-right" size="sm" color="neutral" variant="ghost" class="self-start" />
      <UAlert v-if="refusal" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="refusal" data-card-refused />
    </div>

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
