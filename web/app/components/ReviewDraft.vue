<script setup lang="ts">
import { ContinuationNeeded } from '~/composables/useReview'
import { shortCommit } from '~/utils/dispatchPlans'
import { MAX_REPAIR_PAYLOAD, MAX_REVIEW_SUMMARY, kib, noteWhere, type DraftNote, type Verdict } from '~/utils/review'

/**
 * The draft of a review (Program item RV1, PR-6): the notes gathered, each opened at its lines, edited or dropped; a general note; the
 * verdict and the summary; what stops it from being sent, the batch's size as the run would keep it among them; and Send review, which
 * asks first for Request changes ("Yardmaster reopens build at a1b2c3d…") and, when the server answers that the run must be continued
 * first (continuation_needed), asks a second time in the server's words before it sends again with `continueRun`. The draft is this
 * browser's until it is sent; a failure keeps it as it is.
 */
const props = defineProps<{
  notes: DraftNote[]
  verdict: Verdict
  summary: string
  /** Why it cannot be sent now, in words. */
  problems: string[]
  /** The request's size as the run keeps it, for Request changes. */
  bytes?: number
  task: string
  commit: string
  /** The draft's base is none the page offers now: why, in words; the draft is shown and not sent. */
  stale?: string
  /** The storage refused it: kept in this page alone. */
  unsaved?: boolean
  /** Sends the draft: null once recorded, a ContinuationNeeded to ask about, else why not in words. */
  send: (continueRun: boolean) => Promise<ContinuationNeeded | string | null>
}>()
const emit = defineEmits<{ open: [note: DraftNote]; edit: [note: DraftNote]; drop: [note: DraftNote]; general: []; verdict: [v: Verdict]; summary: [s: string]; discard: [] }>()

const verdicts = [
  { label: 'Request changes', value: 'request_changes', description: 'Yardmaster reopens the task at this candidate with these notes.' },
  { label: 'Comment', value: 'comment', description: 'Recorded beside the candidate; no run changes.' },
  { label: 'Approve', value: 'approve', description: 'Recorded; it accepts nothing, delivers nothing and closes nothing.' },
]
const confirmOpen = ref(false)
const continuation = ref<ContinuationNeeded | null>(null)
const sending = ref(false)
const failure = ref('')

const blocked = computed(() => !!props.stale || props.problems.length > 0)
const count = computed(() => (props.notes.length === 1 ? '1 note' : `${props.notes.length} notes`))
const confirmWords = computed(() => `Yardmaster reopens ${props.task} at ${shortCommit(props.commit)}: its next attempt starts there with ${count.value}, and what rests on it runs again.`)

function intentWords(n: DraftNote): string {
  return n.intent === 'change' ? (n.severity ?? 'change') : n.intent
}
function intentColor(n: DraftNote): 'error' | 'warning' | 'info' | 'neutral' {
  if (n.intent !== 'change') return 'neutral'
  return n.severity === 'blocker' ? 'error' : n.severity === 'major' ? 'warning' : n.severity === 'minor' ? 'info' : 'neutral'
}

function ask() {
  failure.value = ''
  if (blocked.value) return
  if (props.verdict === 'request_changes') confirmOpen.value = true
  else void go(false)
}
async function go(continueRun: boolean) {
  confirmOpen.value = false
  continuation.value = null
  sending.value = true
  try {
    const out = await props.send(continueRun)
    if (out instanceof ContinuationNeeded) continuation.value = out
    else if (out) failure.value = out
  } finally {
    sending.value = false
  }
}
</script>

<template>
  <section class="flex flex-col gap-3 rounded-lg p-3 ring ring-default" data-review-draft :data-review-draft-notes="notes.length">
    <div class="flex items-center gap-2">
      <h3 class="text-sm font-semibold text-highlighted">Draft</h3>
      <UBadge :label="count" color="neutral" variant="subtle" size="sm" data-review-draft-count />
      <UButton label="A general note" icon="i-lucide-message-square-plus" size="xs" color="neutral" variant="ghost" class="ml-auto" data-review-general @click="emit('general')" />
    </div>
    <p class="text-xs text-dimmed">Kept in this browser until it is sent; nothing is a record before.<template v-if="unsaved"> This browser did not keep it: it lasts as long as this page.</template></p>

    <UAlert v-if="stale" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="stale" description="Its notes are shown to copy or drop; it is not sent." data-review-draft-stale>
      <template #actions>
        <UButton label="Drop the draft" size="xs" color="neutral" variant="outline" data-review-draft-discard @click="emit('discard')" />
      </template>
    </UAlert>

    <ol v-if="notes.length" class="flex flex-col gap-1.5">
      <li v-for="n in notes" :key="n.key" class="flex flex-col gap-1 rounded border border-default/60 bg-elevated/20 px-2 py-1.5 text-xs" data-review-draft-note :data-review-note-kind="n.kind" :data-review-note-side="n.side ?? ''" :data-review-note-path="n.path ?? ''">
        <div class="flex min-w-0 flex-wrap items-center gap-1.5">
          <UBadge :label="intentWords(n)" :color="intentColor(n)" variant="subtle" size="sm" />
          <button v-if="n.kind !== 'general'" type="button" class="min-w-0 break-all text-left font-mono text-muted hover:underline" data-review-draft-open @click="emit('open', n)">{{ noteWhere(n) }}</button>
          <span v-else class="text-muted">{{ noteWhere(n) }}</span>
          <span class="ml-auto flex flex-none items-center">
            <UButton icon="i-lucide-pencil" size="xs" color="neutral" variant="ghost" aria-label="Edit the note" data-review-draft-edit @click="emit('edit', n)" />
            <UButton icon="i-lucide-x" size="xs" color="neutral" variant="ghost" aria-label="Drop the note" data-review-draft-drop @click="emit('drop', n)" />
          </span>
        </div>
        <p class="whitespace-pre-wrap break-words" data-review-draft-message>{{ n.message }}</p>
        <p v-if="n.resolution" class="text-muted">Asked: {{ n.resolution }}</p>
      </li>
    </ol>
    <p v-else class="text-xs text-dimmed">Select lines in the diff and press Add a note (or Ctrl+Shift+M), note a whole file from its header, or add a general note.</p>

    <URadioGroup :model-value="verdict" :items="verdicts" size="sm" legend="Verdict" data-review-verdict @update:model-value="(v: unknown) => emit('verdict', v as Verdict)" />
    <UTextarea :model-value="summary" :rows="2" autoresize :maxrows="8" placeholder="A summary (optional)" aria-label="Summary" :maxlength="MAX_REVIEW_SUMMARY" data-review-summary @update:model-value="(v: unknown) => emit('summary', String(v ?? ''))" />
    <p v-if="verdict === 'request_changes' && bytes !== undefined" class="text-[11px] text-dimmed" data-review-draft-bytes :data-bytes="bytes">{{ kib(bytes) }} of {{ MAX_REPAIR_PAYLOAD >> 10 }} KiB as the run keeps it; the server also measures the repair attempt's task file.</p>

    <ul v-if="problems.length" class="flex flex-col gap-0.5 text-xs text-warning" data-review-draft-problems>
      <li v-for="p in problems" :key="p">{{ p }}</li>
    </ul>
    <UAlert v-if="failure" color="error" variant="subtle" icon="i-lucide-circle-x" title="Not sent" :description="failure" data-review-draft-failure />

    <div class="flex items-center justify-end gap-2">
      <UButton v-if="notes.length && !stale" label="Drop the draft" size="sm" color="neutral" variant="ghost" @click="emit('discard')" />
      <UButton label="Send review" icon="i-lucide-send" size="sm" :disabled="blocked" :loading="sending" data-review-send @click="ask" />
    </div>
  </section>

  <UModal v-model:open="confirmOpen" title="Request changes?" :description="confirmWords" :ui="{ content: 'max-w-md' }" data-review-confirm>
    <template #footer>
      <div class="flex w-full justify-end gap-2">
        <UButton label="Keep the draft" color="neutral" variant="ghost" @click="confirmOpen = false" />
        <UButton label="Request changes" icon="i-lucide-send" data-review-confirm-send @click="go(false)" />
      </div>
    </template>
  </UModal>
  <UModal :open="!!continuation" title="Continue the run first?" :description="continuation?.words" :ui="{ content: 'max-w-md' }" data-review-continue @update:open="(o: boolean) => !o && (continuation = null)">
    <template #footer>
      <div class="flex w-full justify-end gap-2">
        <UButton label="Keep the draft" color="neutral" variant="ghost" @click="continuation = null" />
        <UButton label="Continue the run and send" icon="i-lucide-play" data-review-continue-send @click="go(true)" />
      </div>
    </template>
  </UModal>
</template>
