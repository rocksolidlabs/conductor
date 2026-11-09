<script setup lang="ts">
import type { Decision, DecisionRef } from '~/utils/decisions'
import { acceptedAnswer, approverWords, askedWhere, assignmentWords, decisionWords, historyWords, optionsOf, recommended, routedFor, subjectWords } from '~/utils/decisions'
import { relativeTime } from '~/utils/sessions'

/**
 * A durable decision whole (Program item DR1, docs/plans/decisions.md, "web/app"): the question, the context (Markdown, as Dispatch
 * renders a description), the alternatives with their tradeoffs and the recommended one marked, the uncertainty, what a yes
 * authorizes, the evidence as chips (each handed its resolution from the record's `refs`, in the chip's baseline mode: what changed
 * since the question was asked, from the resolution alone), the subject, who may answer, the four states apart (open, response
 * recorded, validated by Yardmaster, applied or why not, each with its receipt and time) and the history. What it is answered with is
 * DecisionRespond's. Brand palette and Nuxt UI only.
 */
const props = defineProps<{ decision: Decision; refs?: readonly DecisionRef[]; now: number }>()

/** Whether the run that holds it runs here now: an answer recorded on a run that does not waits for it to be continued. */
const runHeld = useDecisionRun(() => props.decision.run)
const words = computed(() => decisionWords(props.decision, { runHeld: runHeld.value }))
/** The accepted answer, once there is one: the option picked, the words, the rationale and who. */
const answer = computed(() => acceptedAnswer(props.decision))
const options = computed(() => optionsOf(props.decision))
const best = computed(() => recommended(props.decision))
const subject = computed(() => subjectWords(props.decision.request.subject))
const refOf = (ref: string) => props.refs?.find((r) => r.ref === ref)
const tones = { warning: 'text-warning', success: 'text-success', muted: 'text-muted', error: 'text-error', default: 'text-default' }
const stepIcon = { done: 'i-lucide-circle-check', waiting: 'i-lucide-loader-circle', pending: 'i-lucide-circle', ended: 'i-lucide-circle-slash' }
const stepTone = { done: 'text-success', waiting: 'text-warning', pending: 'text-dimmed', ended: 'text-muted' }
const showHistory = ref(false)
</script>

<template>
  <article class="flex min-w-0 flex-col gap-3 text-sm" data-decision-card :data-decision-id="decision.id" :data-decision-state="decision.state">
    <header class="flex min-w-0 flex-wrap items-center gap-2">
      <span class="font-mono font-semibold text-highlighted" data-decision-card-id>{{ decision.id }}</span>
      <UBadge :label="decision.type" color="neutral" variant="subtle" size="sm" />
      <UBadge v-if="decision.origin === 'plan'" label="plan gate" color="info" variant="subtle" size="sm" />
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

    <ol v-if="decision.type !== 'assignment' && options.length" class="flex flex-col gap-1" data-decision-options>
      <li v-for="(o, i) in options" :key="i" class="flex min-w-0 flex-col rounded-md px-2.5 py-1.5 ring ring-default" :data-decision-option="i">
        <span class="flex flex-wrap items-center gap-2">
          <span class="font-medium">{{ o.label }}</span>
          <UBadge v-if="i === best" label="recommended" color="primary" variant="subtle" size="sm" data-decision-recommended />
        </span>
        <span v-if="o.tradeoffs" class="break-words text-xs text-muted">{{ o.tradeoffs }}</span>
      </li>
    </ol>

    <dl class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
      <template v-if="decision.request.uncertainty">
        <dt class="text-muted">Uncertainty</dt>
        <dd class="break-words" data-decision-uncertainty>{{ decision.request.uncertainty }}</dd>
      </template>
      <template v-if="decision.request.authorizes">
        <dt class="text-muted">A yes authorizes</dt>
        <dd class="break-words" data-decision-authorizes>{{ decision.request.authorizes }}</dd>
      </template>
      <template v-if="subject.text">
        <dt class="text-muted">About</dt>
        <dd class="break-words font-mono" data-decision-subject>
          <NuxtLink v-if="subject.to" :to="subject.to" class="hover:underline">{{ subject.text }}</NuxtLink>
          <template v-else>{{ subject.text }}</template>
        </dd>
      </template>
      <template v-if="decision.request.expertise?.length">
        <dt class="text-muted">Needs</dt>
        <dd>{{ decision.request.expertise.join(', ') }}</dd>
      </template>
    </dl>

    <div v-if="decision.request.evidence?.length" class="flex flex-wrap gap-1.5" data-decision-evidence>
      <BrakemanRefChip v-for="ref in decision.request.evidence" :key="ref" :ref-name="ref" :resolution="refOf(ref)" baseline />
    </div>

    <!-- The accepted answer: what was picked or said, why, and who (Codex's round 1 on #719). -->
    <dl v-if="answer" class="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 rounded-md bg-success/5 p-2 text-xs ring ring-success/20" data-decision-answer>
      <template v-if="answer.choice">
        <dt class="text-muted">Answer</dt>
        <dd class="break-words font-medium" data-decision-answer-choice>{{ answer.choice }}</dd>
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

    <!-- The four states apart: each with what it rests on and when. -->
    <ol class="flex flex-col gap-1 rounded-md bg-elevated/40 p-2" data-decision-steps>
      <li v-for="s in words.steps" :key="s.key" class="flex min-w-0 items-start gap-2 text-xs" :data-decision-step="s.key" :data-step-status="s.status">
        <UIcon :name="stepIcon[s.status]" class="mt-0.5 size-3.5 flex-none" :class="stepTone[s.status]" />
        <span class="min-w-0 flex-1 break-words">
          <span class="font-medium">{{ s.label }}</span>
          <span class="text-muted" data-step-words> · {{ s.words }}</span>
          <span v-if="s.receipt" class="font-mono text-dimmed" data-step-receipt> · {{ s.receipt }}</span>
        </span>
        <span v-if="s.at" class="flex-none text-dimmed" :title="s.at">{{ relativeTime(s.at, now, { suffix: true }) }}</span>
      </li>
    </ol>

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
