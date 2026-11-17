<script setup lang="ts">
import { ApiError } from '~/composables/useApi'
import { ContinuationNeeded, useReviewApi } from '~/composables/useReview'
import { addCaptureNotes } from '~/composables/useReviewDraft'
import { annotationKs, captureNotes, reviewOffered, reviewSendProblems, reviewSendTo, reviewSendWords, type CaptureForReview, type ReviewSendAnswer } from '~/utils/captureReview'
import { candidateName, ticketPath } from '~/utils/dispatch'
import { SEVERITIES, type Severity } from '~/utils/review'
import { mintCommandId } from '~/utils/tasks'

/**
 * A capture sent to a review (Program item PV1, PR-10): in the capture editor's Send, Request a change (`kind` review: RV1's Request
 * changes of the candidate the capture shows, one finding per annotation chosen, Yardmaster repairing the task with the capture's
 * document and screenshot in its repair attempt) and Add to a review (`kind` review-draft: the same findings put in this browser's
 * review draft of that candidate, sent with the rest of the draft). Offered on a capture of a candidate's preview alone (Decision 9):
 * a capture of a worktree still being worked goes to a ticket, and the form says so. A run the server no longer runs is continued only
 * on the person's word, asked in the server's words.
 */
const props = defineProps<{
  capture: CaptureForReview
  kind: 'review' | 'review-draft'
}>()
const emit = defineEmits<{ sent: [] }>()

const api = useReviewApi()
const draft = computed(() => props.kind === 'review-draft')
const offered = computed(() => reviewOffered(props.capture))
const ks = ref<number[]>(annotationKs(props.capture))
const severity = ref<Severity>('major')
const summary = ref('')
const busy = ref(false)
const failure = ref('')
const answer = ref<ReviewSendAnswer | null>(null)
const continuation = ref<ContinuationNeeded | null>(null)
/** The op of the send under way, kept until it is answered, so a send whose answer was lost goes again under the same id. */
const opId = ref('')
/** Add to a review found every annotation chosen in the draft already, in words. */
const inDraft = ref('')

const form = computed(() => ({ draft: draft.value, ks: ks.value, severity: severity.value, summary: summary.value }))
const problems = computed(() => reviewSendProblems(props.capture, form.value))
const candidate = computed(() => props.capture.origin.candidate ?? '')
const severities = SEVERITIES.map((s) => ({ label: s, value: s }))
const result = computed(() => (answer.value ? reviewSendWords(answer.value, draft.value) : null))
const reviewLink = computed(() => {
  const key = answer.value?.key
  const path = key ? ticketPath(key) : ''
  return path ? { path, query: { view: 'review', candidate: candidate.value } } : null
})

function toggle(k: number, on: boolean) {
  ks.value = on ? [...new Set([...ks.value, k])].sort((a, b) => a - b) : ks.value.filter((x) => x !== k)
}
function noteOf(k: number): string {
  const a = props.capture.annotations[annotationKs(props.capture).indexOf(k)]
  return a?.note || (a?.kind === 'region' ? 'A region, no note' : 'No note')
}

async function send(continueRun = false) {
  if (problems.value.length || busy.value) return
  failure.value = ''
  continuation.value = null
  busy.value = true
  opId.value ||= mintCommandId()
  try {
    const out = await api.sendCaptureToReview(props.capture.id, { opId: opId.value, rev: props.capture.rev, to: reviewSendTo(props.capture, form.value, continueRun) })
    answer.value = out
    opId.value = ''
    if (draft.value && out.send.state === 'sent' && out.key) {
      const { added, kept } = addCaptureNotes(out.key, candidate.value, captureNotes(props.capture, out.send.id, ks.value))
      inDraft.value = added === 0 ? 'Your review draft holds these annotations of this revision already: nothing was added.' : ''
      if (!kept) failure.value = 'This browser did not keep the draft: it lasts as long as this page.'
    }
    emit('sent')
  } catch (e) {
    if (e instanceof ContinuationNeeded) continuation.value = e
    else {
      // A refusal the server answered ends this send; a lost answer keeps its id for the same send again.
      if (e instanceof ApiError && e.status > 0 && e.status < 500) opId.value = ''
      failure.value = e instanceof Error ? e.message : String(e)
    }
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <section class="flex flex-col gap-2" data-capture-send-review :data-send-kind="kind">
    <p class="text-xs text-muted">
      <template v-if="draft">The annotations chosen go into your review draft of {{ candidateName(candidate) }} as findings, to send with the rest of the draft.</template>
      <template v-else>A review of {{ candidateName(candidate) }} that requests changes, one finding per annotation chosen: Yardmaster repairs the task, and its repair attempt gets this capture's document and screenshot.</template>
    </p>
    <UAlert v-if="!offered.offered" color="neutral" variant="subtle" icon="i-lucide-info" :title="offered.why" data-capture-review-not-offered />
    <template v-else>
      <ul class="flex flex-col gap-1">
        <li v-for="k in annotationKs(capture)" :key="k" class="flex min-w-0 items-start gap-2 text-xs" :data-capture-review-annotation="k">
          <UCheckbox :model-value="ks.includes(k)" :aria-label="`Annotation ${k}`" @update:model-value="(v: unknown) => toggle(k, v === true)" />
          <span class="min-w-0 break-words"><span class="text-muted">{{ k }}.</span> {{ noteOf(k) }}</span>
        </li>
      </ul>
      <div v-if="!draft" class="flex flex-col gap-2">
        <USelect v-model="severity" :items="severities" size="xs" class="w-28" aria-label="Severity" data-capture-review-severity />
        <UTextarea v-model="summary" :rows="2" autoresize :maxrows="6" :maxlength="4000" placeholder="A summary (optional)" aria-label="Summary" data-capture-review-summary />
      </div>
      <ul v-if="problems.length" class="flex flex-col gap-0.5 text-xs text-muted" data-capture-review-problems>
        <li v-for="p in problems" :key="p">{{ p }}</li>
      </ul>
      <div class="flex justify-end">
        <UButton :label="draft ? 'Add to the review draft' : 'Request a change'" :icon="draft ? 'i-lucide-list-plus' : 'i-lucide-send'" size="sm" :disabled="problems.length > 0" :loading="busy" data-capture-review-send @click="send(false)" />
      </div>
    </template>
    <UAlert v-if="continuation" color="neutral" variant="subtle" icon="i-lucide-info" title="Continue the run first?" :description="continuation.words" data-capture-review-continue>
      <template #actions>
        <UButton label="Continue the run and request" size="xs" data-capture-review-continue-send @click="send(true)" />
      </template>
    </UAlert>
    <UAlert v-if="result" :color="result.tone === 'error' ? 'error' : 'neutral'" variant="subtle" :title="result.title" :description="result.detail" data-capture-review-result :data-send-state="answer?.send.state">
      <template v-if="reviewLink" #actions>
        <UButton label="Open the review" size="xs" color="neutral" variant="outline" :to="reviewLink" data-capture-review-open />
      </template>
    </UAlert>
    <p v-if="inDraft" class="text-xs text-muted" data-capture-review-in-draft>{{ inDraft }}</p>
    <UAlert v-if="failure" color="error" variant="subtle" icon="i-lucide-circle-x" title="Not sent" :description="failure" data-capture-review-failure />
  </section>
</template>
