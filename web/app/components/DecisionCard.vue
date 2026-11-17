<script setup lang="ts">
import type { Decision, DecisionRef } from '~/utils/decisions'
import { acceptedAnswer, approverWords, askedWhere, assignmentWords, authorizesLine, decisionWords, detailsOpen, historyWords, optionsOf, recommended, routedFor, stepLook, subjectWords } from '~/utils/decisions'
import { relativeTime } from '~/utils/sessions'
import type { CaptureSendView } from '~/utils/captureSend'

/**
 * A durable decision whole (Program item DR1, docs/plans/decisions.md, "web/app"; UX1 PR-20, docs/plans/ux-review.md): the question,
 * the context (Markdown, as Dispatch renders a description), what a yes lets happen in one line, the alternatives with their tradeoffs
 * and the recommended one marked, the uncertainty, the evidence as chips (each handed its resolution from the record's `refs`, in the
 * chip's baseline mode: what changed since the question was asked, from the resolution alone), who may answer, the four states apart
 * (asked, the answer, checked by Yardmaster, applied or why not, each with its time) and the history. The id, the type, the plan's
 * origin, the subject and the steps' receipts fold under Details, open at first for a plan's approval (`detailsOpen`).
 *
 * `controls` says the card is drawn above DecisionRespond while it draws its buttons: the buttons are then the one list of choices,
 * with the recommendation and the tradeoffs on them, and the card leaves out its option rows. A card drawn where nobody answers (a
 * decision link's, a person's page) or a record no longer answered keeps them: there they are the only list of choices. What it is
 * answered with is DecisionRespond's. Brand palette and Nuxt UI only.
 */
const props = withDefaults(defineProps<{ decision: Decision; refs?: readonly DecisionRef[]; now: number; controls?: boolean; captures?: boolean }>(), {
  controls: false,
  captures: false,
})

/** Whether the run that holds it runs here now: an answer recorded on a run that does not waits for it to be continued. */
const runHeld = useDecisionRun(() => props.decision.run)
const words = computed(() => decisionWords(props.decision, { runHeld: runHeld.value, controls: props.controls }))
/** The accepted answer, once there is one: the option picked, the words, the rationale and who. */
const answer = computed(() => acceptedAnswer(props.decision))
const options = computed(() => optionsOf(props.decision))
const best = computed(() => recommended(props.decision))
const subject = computed(() => subjectWords(props.decision.request.subject))
const yes = computed(() => authorizesLine(props.decision))
const receipts = computed(() => words.value.steps.filter((s) => s.receipt))
const refOf = (ref: string) => props.refs?.find((r) => r.ref === ref)
const tones = { warning: 'text-warning', muted: 'text-muted', error: 'text-error', default: 'text-default', dimmed: 'text-dimmed' }
const showHistory = ref(false)

/**
 * The captures sent to it (PV1 PR-11: a capture attached to a decision is a comment on it, and a send of the capture's), from
 * capture_sends, the admin's read, made only where the admin's page draws the card and says so (`captures`): a page that draws it for
 * a link's holder (the join page) reads nothing of the server's API.
 */
const { request } = useApi()
const captures = ref<CaptureSendView[]>([])
async function readCaptures() {
  if (!props.captures) return
  try {
    const r = await request<{ sends?: CaptureSendView[] }>(`/api/decisions/${encodeURIComponent(props.decision.id)}/captures`)
    captures.value = (r.sends ?? []).filter((s) => s.state === 'sent')
  } catch {
    captures.value = []
  }
}
watch(
  () => [props.decision.id, props.decision.rev],
  () => void readCaptures(),
  { immediate: true },
)
/** Details open or folded: as `detailsOpen` says for the decision, then as the person leaves it, through every new rev of the record. */
const showDetails = ref(detailsOpen(props.decision))
watch(
  () => props.decision.id,
  () => (showDetails.value = detailsOpen(props.decision)),
)
</script>

<template>
  <article class="flex min-w-0 flex-col gap-3 text-sm" data-decision-card :data-decision-id="decision.id" :data-decision-state="decision.state">
    <header class="flex min-w-0 flex-wrap items-center gap-2">
      <span class="min-w-0 break-words text-xs" :class="tones[words.tone]" data-decision-state-words>{{ words.state }}</span>
    </header>

    <p class="break-words text-base font-medium text-highlighted" data-decision-question>{{ decision.request.question }}</p>
    <p class="text-xs text-muted">
      Asked by {{ askedWhere(decision) }} in
      <NuxtLink :to="`/runs/${encodeURIComponent(decision.run)}?view=tasks${decision.asker ? `&task=${encodeURIComponent(decision.asker.task)}` : ''}`" class="font-mono hover:underline" data-decision-run>{{ decision.run }}</NuxtLink>
      {{ relativeTime(decision.createdAt, now, { suffix: true }) }} · answered by <span data-decision-approver>{{ approverWords(decision) }}</span><span v-if="routedFor(decision)" :data-decision-routed-for="routedFor(decision)!.person">, for {{ routedFor(decision)!.person }} (routed by {{ routedFor(decision)!.by }})</span>
    </p>
    <p v-if="assignmentWords(decision)" class="text-sm text-default" data-decision-assignment>{{ assignmentWords(decision) }}.</p>

    <MarkdownPreview v-if="decision.request.context" :text="decision.request.context" dir="" :relative-links="false" class="text-sm" data-decision-context />

    <!-- What a yes lets happen, in one line: a gate's own sentence, or an agent's words led by "A yes authorizes". -->
    <p v-if="yes" class="break-words text-sm text-default" data-decision-authorizes-line>{{ yes.lead }}<span data-decision-authorizes>{{ yes.words }}</span></p>

    <ol v-if="!controls && decision.type !== 'assignment' && options.length" class="flex flex-col gap-1" data-decision-options>
      <li v-for="(o, i) in options" :key="i" class="flex min-w-0 flex-col rounded-md px-2.5 py-1.5 ring ring-default" :data-decision-option="i">
        <span class="flex flex-wrap items-center gap-2">
          <span class="min-w-0 font-medium wrap-anywhere" :data-decision-option-label="o.label">{{ o.label }}</span>
          <UBadge v-if="i === best" label="recommended" color="primary" variant="subtle" size="sm" data-decision-recommended />
        </span>
        <span v-if="o.tradeoffs" class="break-words text-xs text-muted">{{ o.tradeoffs }}</span>
      </li>
    </ol>

    <p v-if="decision.request.uncertainty" class="break-words text-xs"><span class="text-muted">Uncertainty: </span><span data-decision-uncertainty>{{ decision.request.uncertainty }}</span></p>

    <div v-if="decision.request.evidence?.length" class="flex flex-wrap gap-1.5" data-decision-evidence>
      <BrakemanRefChip v-for="ref in decision.request.evidence" :key="ref" :ref-name="ref" :resolution="refOf(ref)" baseline />
    </div>

    <!-- The fine print, folded as the inbox folds its evidence: the id, the type, where it comes from, the subject, the receipts. -->
    <div class="flex flex-col gap-1.5" data-decision-details>
      <UButton
        label="Details"
        :icon="showDetails ? 'i-lucide-chevron-down' : 'i-lucide-chevron-right'"
        color="neutral"
        variant="ghost"
        size="sm"
        class="min-h-11 self-start"
        :aria-expanded="showDetails"
        data-decision-details-toggle
        @click="showDetails = !showDetails"
      />
      <dl v-if="showDetails" class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 rounded-md bg-elevated/40 p-2 text-xs" data-decision-details-list>
        <dt class="text-muted">Decision</dt>
        <dd class="break-words"><span class="font-mono" data-decision-card-id>{{ decision.id }}</span> · <span data-decision-type>{{ decision.type }}</span><span v-if="decision.origin === 'plan'" data-decision-from-plan> · From the plan</span></dd>
        <template v-if="subject.text">
          <dt class="text-muted">About</dt>
          <dd class="break-words font-mono" data-decision-subject>
            <NuxtLink v-if="subject.to" :to="subject.to" class="hover:underline">{{ subject.text }}</NuxtLink>
            <template v-else>{{ subject.text }}</template>
          </dd>
        </template>
        <template v-if="decision.request.expertise?.length">
          <dt class="text-muted">Needs</dt>
          <dd class="break-words" data-decision-expertise>{{ decision.request.expertise.join(', ') }}</dd>
        </template>
        <template v-for="s in receipts" :key="s.key">
          <dt class="text-muted">{{ s.label }}</dt>
          <dd class="break-words font-mono" data-step-receipt :data-step-receipt-of="s.key">{{ s.receipt }}</dd>
        </template>
      </dl>
    </div>

    <!-- The accepted answer: what was picked or said, why, and who (Codex's round 1 on #719); neutral, as a done step is (invariant 7). -->
    <dl v-if="answer" class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 rounded-md bg-elevated/40 p-2 text-xs ring ring-default" data-decision-answer>
      <template v-if="answer.choice">
        <dt class="text-muted">Answer</dt>
        <dd class="font-medium wrap-anywhere" data-decision-answer-choice>{{ answer.choice }}</dd>
      </template>
      <template v-if="answer.text">
        <dt class="text-muted">{{ answer.choice ? 'Said' : 'Answer' }}</dt>
        <dd class="whitespace-pre-wrap break-words" data-decision-answer-text>{{ answer.text }}</dd>
      </template>
      <template v-if="answer.words && !answer.choice && !answer.text">
        <dt class="text-muted">Answer</dt>
        <dd class="break-words" data-decision-answer-words>{{ answer.words }}</dd>
      </template>
      <template v-if="answer.rationale">
        <dt class="text-muted">Why</dt>
        <dd class="whitespace-pre-wrap break-words" data-decision-answer-rationale>{{ answer.rationale }}</dd>
      </template>
      <template v-if="answer.by">
        <dt class="text-muted">By</dt>
        <dd class="break-words" data-decision-answer-by>{{ answer.by }}</dd>
      </template>
      <template v-if="answer.for">
        <dt class="text-muted">For</dt>
        <dd class="break-words" data-decision-answer-for>{{ answer.for }}</dd>
      </template>
    </dl>

    <!-- The four states apart: each with what it rests on and when; the receipts are in Details. -->
    <ol class="flex flex-col gap-1 rounded-md bg-elevated/40 p-2" data-decision-steps>
      <li v-for="s in words.steps" :key="s.key" class="flex min-w-0 items-start gap-2 text-xs" :data-decision-step="s.key" :data-step-status="s.status" :data-step-icon="stepLook(s).icon">
        <UIcon :name="stepLook(s).icon" class="mt-0.5 size-3.5 flex-none" :class="tones[stepLook(s).tone]" data-step-mark />
        <span class="min-w-0 flex-1 break-words">
          <span class="font-medium" data-step-label>{{ s.label }}</span>
          <span class="text-muted" data-step-words> · {{ s.words }}</span>
        </span>
        <span v-if="s.at" class="flex-none text-dimmed" :title="s.at">{{ relativeTime(s.at, now, { suffix: true }) }}</span>
      </li>
    </ol>

    <div v-if="captures.length" class="flex flex-col gap-1" data-decision-captures>
      <span class="text-xs font-medium text-muted">Captures</span>
      <ul class="flex flex-col gap-0.5 text-xs">
        <li v-for="c in captures" :key="c.id" class="flex min-w-0 flex-wrap gap-x-2" :data-decision-capture="c.id">
          <span class="font-mono" data-mono="id">capture:{{ c.capture }}/{{ c.id }}</span>
          <span class="text-muted">r{{ c.rev }} · {{ c.createdBy || 'admin' }} {{ relativeTime(c.createdAt, now, { suffix: true }) }}</span>
        </li>
      </ul>
    </div>

    <div v-if="decision.history?.length" class="flex flex-col gap-1">
      <UButton :label="showHistory ? 'Hide history' : `History (${decision.history.length})`" :icon="showHistory ? 'i-lucide-chevron-down' : 'i-lucide-chevron-right'" size="xs" color="neutral" variant="ghost" class="self-start" data-decision-history-toggle @click="showHistory = !showHistory" />
      <ol v-if="showHistory" class="flex flex-col gap-0.5 text-xs" data-decision-history>
        <li v-for="h in decision.history" :key="h.seq" class="flex min-w-0 gap-2" :data-history-kind="h.kind">
          <span class="flex-none text-dimmed" :title="h.at">{{ relativeTime(h.at, now, { suffix: true }) }}</span>
          <span class="min-w-0 break-words">{{ historyWords(h) }}<span v-if="h.by" class="text-muted"> · {{ h.by }}</span></span>
        </li>
      </ol>
    </div>
  </article>
</template>
