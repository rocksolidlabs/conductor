<script setup lang="ts">
import type { Decision, OpReceipt, ResponseReceipt } from '~/utils/decisions'
import { answerAsk, answerAsWords, askForMore, commentDecision, offersAskForMore, confirmAuthorizes, optionSubject, optionsOf, rationaleRequired, recommended, refusalOf, respondDecision, responseBody, sendDecisionWrite } from '~/utils/decisions'
import { mintCommandId } from '~/utils/tasks'
import type { PolicyNow } from '~/utils/decisionPolicy'
import { heldBackWords, MIN_OVERRIDE_REASON, operatorHeldBack, overrideAllowed, overrideReasonReady, policyRefusalWords, routedPolicy } from '~/utils/decisionPolicy'

/**
 * The operator's answer to a durable decision (Program item DR1, docs/plans/decisions.md, "web/app"): the controls by its type (a
 * button per option, the recommended one marked on it and each option's tradeoffs in a line under them, UX1 PR-20; a clarification's
 * text beside them), a rationale, Comment and Ask for more (none on a comparison's selection; disabled, with the record's words,
 * when no live agent asked it). An approval's and an assignment's answer is asked first: the choice, then Confirm, which sends it
 * against the rev the card showed when it was picked. The receipt is shown (Recorded); a refusal is shown in the server's words (who
 * answered first, what changed), with Read again. A choice picked stays to be confirmed even when the record moves meanwhile: the
 * server judges it, and its refusal says what happened. A write's reply goes to what `begin` answered when the write started
 * (useDecisionRecord's), so a reply from before the stream came back is dropped (Codex's round 3 on #719).
 *
 * A comparison's selection (origin `compare`, Program item CP1) is the same act wherever it is drawn (the comparison, the card, the
 * ticket's Decisions, Needs you, a phone): nothing preselected or marked recommended, each candidate's commit on its line under the
 * buttons, linked to its comparison; the rationale required, Confirm held until it holds text; the confirm step saying the answer is
 * final, its button naming the candidate ("Select approach-b (final)"). The server refuses a selection without a rationale too.
 */
const props = withDefaults(defineProps<{ decision: Decision; begin: () => (decision: Decision) => void; policyNow?: PolicyNow | null }>(), { policyNow: null })
const emit = defineEmits<{ read: []; controls: [shown: boolean] }>()

const { request } = useApi()
const identity = useIdentity()
const post = <R,>(path: string, body: unknown) => request<R>(path, { method: 'POST', body })

const d = computed(() => props.decision)
const options = computed(() => optionsOf(d.value))
const best = computed(() => recommended(d.value))
const confirms = computed(() => d.value.type === 'approval' || d.value.type === 'assignment')
const more = computed(() => askForMore(d.value))
/** Ask for more is offered where an agent may be asked; a comparison's selection has none, ever (offersAskForMore). */
const asks = computed(() => offersAskForMore(d.value))
const ticket = useDecisionRunInfo(() => d.value.run).ticket
const rationaleNeeded = computed(() => rationaleRequired(d.value))

/** The choice picked and the rev the card showed then, waiting for Confirm. */
const pending = ref<{ choice: number; rev: number } | null>(null)
/** A clarification's picked option, and its words. */
const picked = ref<number | undefined>()
const text = ref('')
const rationale = ref('')
const receipt = ref<ResponseReceipt | null>(null)
const refusal = ref('')
const busy = ref(false)
const comment = ref('')
const opWords = ref('')
const commentBusy = ref(false)

/** The controls show while it is open, and while a picked choice waits for Confirm whatever happened since. */
const answering = computed(() => d.value.state === 'open' || !!pending.value)
const moved = computed(() => !!pending.value && (d.value.state !== 'open' || d.value.rev !== pending.value.rev))
// Whether the buttons are drawn, said to the card above (UX1 PR-20): while they are, they are the one list of choices.
watch(answering, (shown) => emit('controls', shown), { immediate: true })
onBeforeUnmount(() => emit('controls', false))
/** The options that say what they cost or what they continue (a selection's candidate), in order: one muted line each under the buttons. */
const tradeoffs = computed(() => options.value.flatMap((o, i) => {
  const subject = optionSubject(o, ticket.value)
  return o.tradeoffs || subject ? [{ i, label: o.label, tradeoffs: o.tradeoffs ?? '', subject }] : []
}))
/** How the answer is asked for with the choice picked: the confirm's label, the finality words, the rationale's words, and whether it may go. */
const ask = computed(() => answerAsk(d.value, pending.value?.choice ?? picked.value, rationale.value))

/**
 * A decision router's policy in force (SW1): the operator's answer buttons are held back with whose it is to answer, and, where the
 * policy lets the operator answer in its place, Override… opens a dialog that sends the answer as an override, its reason (at least
 * MIN_OVERRIDE_REASON characters) the rationale. Ask the router again asks it for a route anew.
 */
const policy = computed(() => routedPolicy(d.value))
const heldBack = computed(() => operatorHeldBack(d.value, props.policyNow))
const heldWords = computed(() => (policy.value ? heldBackWords(policy.value) : ''))
const canOverride = computed(() => heldBack.value && overrideAllowed(policy.value))
const overriding = ref(false)
const overrideChoice = ref<number | undefined>()
const overrideText = ref('')
const overrideReason = ref('')
const overrideReady = computed(() => overrideReasonReady(overrideReason.value) && (overrideChoice.value !== undefined || !!overrideText.value.trim()))

function openOverride() {
  overrideChoice.value = undefined
  overrideText.value = ''
  overrideReason.value = ''
  refusal.value = ''
  overriding.value = true
}

async function sendOverride() {
  if (busy.value || !overrideReady.value) return
  busy.value = true
  refusal.value = ''
  const take = props.begin()
  try {
    const body = responseBody({ choice: overrideChoice.value, text: overrideText.value, rationale: overrideReason.value, expectedRev: d.value.rev, byName: identity.name.value, override: true })
    const r = await respondDecision((path, b) => post<{ receipt: ResponseReceipt; decision: Decision }>(path, b), d.value.id, body)
    receipt.value = r.receipt
    overriding.value = false
    take(r.decision)
  } catch (e) {
    refusal.value = refusalWords(e)
    receipt.value = null
  } finally {
    busy.value = false
  }
}

/** A refusal in words: the policy's own codes said first, then the server's words. */
function refusalWords(e: unknown): string {
  const err = refusalOf(e)
  const lead = policyRefusalWords(err.code, d.value.id, policy.value)
  return lead && lead !== err.message ? `${lead} ${err.message}` : err.message
}

const askingRouter = ref(false)
async function askRouter() {
  if (askingRouter.value) return
  askingRouter.value = true
  opWords.value = ''
  const take = props.begin()
  try {
    const r = await sendDecisionWrite(
      (b: { opId: string; to: { kind: 'router' }; expectedRev: number }) => post<{ receipt: OpReceipt; decision: Decision }>(`/api/decisions/${encodeURIComponent(d.value.id)}/route`, b),
      { opId: mintCommandId(), to: { kind: 'router' }, expectedRev: d.value.rev },
    )
    opWords.value = r.receipt.words
    take(r.decision)
  } catch (e) {
    opWords.value = refusalWords(e)
  } finally {
    askingRouter.value = false
  }
}

function pick(i: number) {
  refusal.value = ''
  if (confirms.value) pending.value = { choice: i, rev: d.value.rev }
  else picked.value = picked.value === i ? undefined : i
}

async function send(choice: number | undefined, rev: number) {
  // A selection is never sent without its rationale (the server refuses one too).
  if (busy.value || !ask.value.ready) return
  busy.value = true
  refusal.value = ''
  const take = props.begin()
  try {
    const body = responseBody({ choice, text: confirms.value ? '' : text.value, rationale: rationale.value, expectedRev: rev, byName: identity.name.value })
    const r = await respondDecision((path, b) => post<{ receipt: ResponseReceipt; decision: Decision }>(path, b), d.value.id, body)
    receipt.value = r.receipt
    pending.value = null
    picked.value = undefined
    text.value = ''
    rationale.value = ''
    take(r.decision)
  } catch (e) {
    refusal.value = refusalWords(e)
    receipt.value = null
  } finally {
    busy.value = false
  }
}

const canSend = computed(() => !busy.value && ask.value.ready && (picked.value !== undefined || !!text.value.trim()))

async function opSend(kind: 'comment' | 'more_info') {
  if (commentBusy.value || !comment.value.trim()) return
  commentBusy.value = true
  opWords.value = ''
  const take = props.begin()
  try {
    const r = await commentDecision((path, b) => post<{ receipt: OpReceipt; decision: Decision }>(path, b), d.value.id, { kind, text: comment.value, expectedRev: d.value.rev })
    opWords.value = r.receipt.words
    comment.value = ''
    take(r.decision)
  } catch (e) {
    opWords.value = refusalOf(e).message
  } finally {
    commentBusy.value = false
  }
}

function readAgain() {
  refusal.value = ''
  pending.value = null
  emit('read')
}

// Another decision in the card: nothing of the last one's answer stays.
watch(
  () => d.value.id,
  () => {
    pending.value = null
    picked.value = undefined
    text.value = ''
    rationale.value = ''
    receipt.value = null
    refusal.value = ''
    opWords.value = ''
    comment.value = ''
  },
)
</script>

<template>
  <section class="flex min-w-0 flex-col gap-2" data-decision-respond :data-decision-id="decision.id">
    <template v-if="answering">
      <!-- The buttons are the choices (UX1 PR-20, invariant 6): each label whole, wrapped inside its button, a run with no space
           (a hash) broken anywhere, never cut; the buttons wrap, a choice longer than the row taking a row of its own; 44 px each
           way under touch (invariant 8). -->
      <div class="flex flex-wrap gap-2" data-decision-choices>
        <UButton
          v-for="(o, i) in options"
          :key="i"
          :label="o.label"
          size="sm"
          class="max-w-full max-lg:min-h-11 max-lg:min-w-11"
          :ui="{ label: 'min-w-0 text-clip whitespace-normal wrap-anywhere text-start' }"
          :color="(pending?.choice ?? picked) === i ? 'primary' : 'neutral'"
          :variant="(pending?.choice ?? picked) === i ? 'solid' : i === best ? 'soft' : 'outline'"
          :disabled="busy || heldBack"
          :data-decision-choice="i"
          :data-decision-choice-label="o.label"
          :data-decision-recommended="i === best ? '' : undefined"
          @click="pick(i)"
        >
          <template v-if="i === best" #trailing>
            <UBadge label="recommended" color="neutral" variant="subtle" size="sm" class="shrink-0" data-decision-recommended-mark />
          </template>
        </UButton>
      </div>
      <!-- A router's policy in force: whose it is to answer, and the operator's override where the policy allows one. -->
      <div v-if="heldBack" class="flex flex-wrap items-center gap-2 rounded-md bg-elevated/40 p-2 text-sm ring ring-default" data-decision-held-back>
        <span class="min-w-0 flex-1 break-words" data-decision-held-back-words>{{ heldWords }}.</span>
        <UButton v-if="canOverride" label="Override…" icon="i-lucide-shield-alert" size="sm" color="warning" variant="outline" class="max-lg:min-h-11" :disabled="busy" data-decision-override @click="openOverride" />
      </div>
      <!-- Each choice's tradeoffs, one muted line each under the buttons. -->
      <ul v-if="tradeoffs.length" class="flex flex-col gap-0.5 text-xs text-muted" data-decision-tradeoffs>
        <li v-for="t in tradeoffs" :key="t.i" class="break-words" :data-decision-tradeoff="t.i">
          <span class="font-medium">{{ t.label }}:</span> {{ t.tradeoffs }}<template v-if="t.subject"><template v-if="t.tradeoffs"> · </template><span :data-decision-option-subject="t.i"><NuxtLink v-if="t.subject.to" :to="t.subject.to" class="hover:underline">{{ t.subject.text }}</NuxtLink><template v-else>{{ t.subject.text }}</template></span></template>
        </li>
      </ul>
      <UTextarea v-if="decision.type === 'clarification' && !heldBack" v-model="text" :rows="2" autoresize placeholder="Or answer in words…" :maxlength="4096" :disabled="busy" data-decision-text />
      <UTextarea v-if="!heldBack" v-model="rationale" :rows="1" autoresize :placeholder="ask.placeholder" :maxlength="2000" :disabled="busy" :required="rationaleNeeded" :aria-required="rationaleNeeded" data-decision-rationale :data-rationale-required="rationaleNeeded ? '' : undefined" />

      <div v-if="pending" class="flex flex-col gap-2 rounded-md bg-warning/10 p-2 ring ring-warning/30" data-decision-confirm-step>
        <p class="text-sm wrap-anywhere">
          Answer {{ decision.id }} <span class="font-semibold" data-decision-confirm-choice>{{ options[pending.choice]?.label }}</span>?
          <template v-if="confirmAuthorizes(decision, pending.choice)"> <span data-decision-confirm-authorizes>{{ confirmAuthorizes(decision, pending.choice) }}</span></template>
        </p>
        <p v-if="ask.final" class="text-sm text-default" data-decision-final>{{ ask.final }}</p>
        <p v-if="answerAsWords(decision)" class="text-xs text-muted" data-decision-answer-as>{{ answerAsWords(decision) }}.</p>
        <p v-if="!ask.ready" class="text-xs text-muted" data-decision-rationale-needed>Say why first: the rationale is the record of this selection.</p>
        <p v-if="moved" class="text-xs text-warning" data-decision-moved>{{ decision.id }} changed since you picked it: it is {{ decision.state }} now. Confirm sends it anyway, and the answer says what happened.</p>
        <div class="flex justify-end gap-2">
          <UButton label="Back" color="neutral" variant="ghost" size="sm" class="max-lg:min-h-11 max-lg:min-w-11" :disabled="busy" data-decision-back @click="pending = null" />
          <UButton :label="ask.confirm" color="primary" size="sm" class="max-w-full max-lg:min-h-11 max-lg:min-w-11" :ui="{ label: 'min-w-0 text-clip whitespace-normal wrap-anywhere text-start' }" :loading="busy" :disabled="!ask.ready" data-decision-confirm @click="send(pending.choice, pending.rev)" />
        </div>
      </div>
      <div v-else-if="!confirms" class="flex items-center justify-end gap-2">
        <span v-if="answerAsWords(decision)" class="mr-auto text-xs text-muted" data-decision-answer-as>{{ answerAsWords(decision) }}.</span>
        <UButton label="Answer" icon="i-lucide-send" color="primary" size="sm" class="max-lg:min-h-11 max-lg:min-w-11" :disabled="!canSend" :loading="busy" data-decision-send @click="send(picked, decision.rev)" />
      </div>
    </template>

    <!-- Done, in neutral ink with its check, as a done step is (UX1 PR-20, invariant 7: green is a live agent's alone). -->
    <p v-if="receipt" class="flex items-start gap-1.5 rounded-md bg-elevated/40 p-2 text-sm text-default" data-decision-receipt><UIcon name="i-lucide-circle-check" class="mt-0.5 size-4 flex-none" data-decision-receipt-mark /><span class="min-w-0 break-words">{{ receipt.words }}<span v-if="receipt.k" class="font-mono text-xs text-muted"> · receipt {{ receipt.decision }}/{{ receipt.k }}</span></span></p>
    <div v-if="refusal" class="flex flex-wrap items-center gap-2 rounded-md bg-error/10 p-2 text-sm text-error" data-decision-refusal>
      <span class="min-w-0 flex-1 break-words" data-decision-refusal-words>{{ refusal }}</span>
      <UButton label="Read again" icon="i-lucide-refresh-cw" size="xs" color="neutral" variant="outline" data-decision-read-again @click="readAgain" />
    </div>

    <UModal v-if="canOverride" v-model:open="overriding" :title="`Override ${decision.id}`" description="Answer in its router's policy's place. The override is kept with its reason and your name." data-decision-override-dialog>
      <template #body>
        <div class="flex flex-col gap-3" data-decision-override-body>
          <div v-if="options.length" class="flex flex-wrap gap-2">
            <UButton
              v-for="(o, i) in options"
              :key="i"
              :label="o.label"
              size="sm"
              class="max-w-full max-lg:min-h-11"
              :ui="{ label: 'min-w-0 text-clip whitespace-normal wrap-anywhere text-start' }"
              :color="overrideChoice === i ? 'primary' : 'neutral'"
              :variant="overrideChoice === i ? 'solid' : 'outline'"
              :data-decision-override-choice="i"
              @click="overrideChoice = overrideChoice === i ? undefined : i"
            />
          </div>
          <UTextarea v-if="decision.type === 'clarification'" v-model="overrideText" :rows="2" autoresize placeholder="Or answer in words…" :maxlength="4096" data-decision-override-text />
          <UFormField label="Why you answer in its place" :hint="`At least ${MIN_OVERRIDE_REASON} characters`" required>
            <UTextarea v-model="overrideReason" :rows="2" autoresize class="w-full" :maxlength="2000" placeholder="The reason, kept with the override" data-decision-override-reason />
          </UFormField>
          <p v-if="overrideReason && !overrideReasonReady(overrideReason)" class="text-xs text-muted" data-decision-override-short>Say a little more: a reason of at least {{ MIN_OVERRIDE_REASON }} characters.</p>
          <p v-if="refusal" class="break-words text-sm text-error" data-decision-override-refusal>{{ refusal }}</p>
        </div>
      </template>
      <template #footer>
        <div class="flex w-full justify-end gap-2">
          <UButton label="Cancel" color="neutral" variant="ghost" :disabled="busy" data-decision-override-cancel @click="overriding = false" />
          <UButton label="Send the override" color="warning" :loading="busy" :disabled="!overrideReady" data-decision-override-send @click="sendOverride" />
        </div>
      </template>
    </UModal>

    <!-- A comment is kept in the history; Ask for more types it to the asking agent, which answers with a new request. -->
    <div class="flex flex-col gap-1.5 border-t border-default pt-2" data-decision-comment-box>
      <UTextarea v-model="comment" :rows="1" autoresize :placeholder="asks ? 'A comment, or what the agent should say more about…' : 'A comment, kept with the decision'" :maxlength="4000" :disabled="commentBusy" data-decision-comment-text />
      <div class="flex flex-wrap items-center justify-end gap-2">
        <span v-if="asks && !more.enabled" class="min-w-0 flex-1 break-words text-xs text-muted" data-decision-more-why>{{ more.why }}</span>
        <UButton v-if="policy && decision.state === 'open'" label="Ask the router again" icon="i-lucide-route" size="xs" color="neutral" variant="outline" :loading="askingRouter" data-decision-ask-router @click="askRouter" />
        <UButton label="Comment" icon="i-lucide-message-square" size="xs" color="neutral" variant="outline" :disabled="!comment.trim() || commentBusy" data-decision-comment @click="opSend('comment')" />
        <UButton v-if="asks" label="Ask for more" icon="i-lucide-message-circle-question" size="xs" color="neutral" variant="outline" :disabled="!more.enabled || !comment.trim() || commentBusy" :title="more.enabled ? undefined : more.why" data-decision-more @click="opSend('more_info')" />
      </div>
      <p v-if="opWords" class="text-xs text-muted" data-decision-op-words>{{ opWords }}</p>
    </div>
  </section>
</template>
