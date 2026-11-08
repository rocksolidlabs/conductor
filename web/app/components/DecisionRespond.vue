<script setup lang="ts">
import type { Decision, OpReceipt, ResponseReceipt } from '~/utils/decisions'
import { askForMore, commentDecision, confirmAuthorizes, optionsOf, recommended, refusalOf, respondDecision, responseBody } from '~/utils/decisions'

/**
 * The operator's answer to a durable decision (Program item DR1, docs/plans/decisions.md, "web/app"): the controls by its type (a
 * button per option; a clarification's text beside them), a rationale, Comment and Ask for more (disabled, with the record's words,
 * when no live agent asked it). An approval's and an assignment's answer is asked first: the choice, then Confirm, which sends it
 * against the rev the card showed when it was picked. The receipt is shown (Recorded); a refusal is shown in the server's words (who
 * answered first, what changed), with Read again. A choice picked stays to be confirmed even when the record moves meanwhile: the
 * server judges it, and its refusal says what happened. A write's reply goes to what `begin` answered when the write started
 * (useDecisionRecord's), so a reply from before the stream came back is dropped (Codex's round 3 on #719).
 */
const props = defineProps<{ decision: Decision; begin: () => (decision: Decision) => void }>()
const emit = defineEmits<{ read: [] }>()

const { request } = useApi()
const identity = useIdentity()
const post = <R,>(path: string, body: unknown) => request<R>(path, { method: 'POST', body })

const d = computed(() => props.decision)
const options = computed(() => optionsOf(d.value))
const best = computed(() => recommended(d.value))
const confirms = computed(() => d.value.type === 'approval' || d.value.type === 'assignment')
const more = computed(() => askForMore(d.value))

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

function pick(i: number) {
  refusal.value = ''
  if (confirms.value) pending.value = { choice: i, rev: d.value.rev }
  else picked.value = picked.value === i ? undefined : i
}

async function send(choice: number | undefined, rev: number) {
  if (busy.value) return
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
    const err = refusalOf(e)
    refusal.value = err.message
    receipt.value = null
  } finally {
    busy.value = false
  }
}

const canSend = computed(() => !busy.value && (picked.value !== undefined || !!text.value.trim()))

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
      <div class="flex flex-wrap gap-2" data-decision-choices>
        <UButton
          v-for="(o, i) in options"
          :key="i"
          :label="o.label"
          size="sm"
          :color="(pending?.choice ?? picked) === i ? 'primary' : 'neutral'"
          :variant="(pending?.choice ?? picked) === i ? 'solid' : i === best ? 'soft' : 'outline'"
          :disabled="busy"
          :data-decision-choice="i"
          :data-decision-choice-label="o.label"
          @click="pick(i)"
        />
      </div>
      <UTextarea v-if="decision.type === 'clarification'" v-model="text" :rows="2" autoresize placeholder="Or answer in words…" :maxlength="4096" :disabled="busy" data-decision-text />
      <UTextarea v-model="rationale" :rows="1" autoresize placeholder="Why (optional, kept with the answer)…" :maxlength="2000" :disabled="busy" data-decision-rationale />

      <div v-if="pending" class="flex flex-col gap-2 rounded-md bg-warning/10 p-2 ring ring-warning/30" data-decision-confirm-step>
        <p class="text-sm">
          Answer {{ decision.id }} <span class="font-semibold">{{ options[pending.choice]?.label }}</span>?
          <template v-if="confirmAuthorizes(decision, pending.choice)"> <span data-decision-confirm-authorizes>{{ confirmAuthorizes(decision, pending.choice) }}</span></template>
        </p>
        <p v-if="moved" class="text-xs text-warning" data-decision-moved>{{ decision.id }} changed since you picked it: it is {{ decision.state }} now. Confirm sends it anyway, and the answer says what happened.</p>
        <div class="flex justify-end gap-2">
          <UButton label="Back" color="neutral" variant="ghost" size="sm" :disabled="busy" data-decision-back @click="pending = null" />
          <UButton :label="`Answer ${options[pending.choice]?.label ?? ''}`" color="primary" size="sm" :loading="busy" data-decision-confirm @click="send(pending.choice, pending.rev)" />
        </div>
      </div>
      <div v-else-if="!confirms" class="flex justify-end">
        <UButton label="Answer" icon="i-lucide-send" color="primary" size="sm" :disabled="!canSend" :loading="busy" data-decision-send @click="send(picked, decision.rev)" />
      </div>
    </template>

    <p v-if="receipt" class="rounded-md bg-success/10 p-2 text-sm text-success" data-decision-receipt>{{ receipt.words }}<span v-if="receipt.k" class="font-mono text-xs text-muted"> · receipt {{ receipt.decision }}/{{ receipt.k }}</span></p>
    <div v-if="refusal" class="flex flex-wrap items-center gap-2 rounded-md bg-error/10 p-2 text-sm text-error" data-decision-refusal>
      <span class="min-w-0 flex-1 break-words" data-decision-refusal-words>{{ refusal }}</span>
      <UButton label="Read again" icon="i-lucide-refresh-cw" size="xs" color="neutral" variant="outline" data-decision-read-again @click="readAgain" />
    </div>

    <!-- A comment is kept in the history; Ask for more types it to the asking agent, which answers with a new request. -->
    <div class="flex flex-col gap-1.5 border-t border-default pt-2" data-decision-comment-box>
      <UTextarea v-model="comment" :rows="1" autoresize placeholder="A comment, or what the agent should say more about…" :maxlength="4000" :disabled="commentBusy" data-decision-comment-text />
      <div class="flex flex-wrap items-center justify-end gap-2">
        <span v-if="!more.enabled" class="min-w-0 flex-1 break-words text-xs text-muted" data-decision-more-why>{{ more.why }}</span>
        <UButton label="Comment" icon="i-lucide-message-square" size="xs" color="neutral" variant="outline" :disabled="!comment.trim() || commentBusy" data-decision-comment @click="opSend('comment')" />
        <UButton label="Ask for more" icon="i-lucide-message-circle-question" size="xs" color="neutral" variant="outline" :disabled="!more.enabled || !comment.trim() || commentBusy" :title="more.enabled ? undefined : more.why" data-decision-more @click="opSend('more_info')" />
      </div>
      <p v-if="opWords" class="text-xs text-muted" data-decision-op-words>{{ opWords }}</p>
    </div>
  </section>
</template>
