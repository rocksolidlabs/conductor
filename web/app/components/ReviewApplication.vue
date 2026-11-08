<script setup lang="ts">
import { ContinuationNeeded, type ReviewSummary } from '~/composables/useReview'
import { relativeTime } from '~/utils/sessions'
import { applicationWords } from '~/utils/review'

/**
 * A review sent and what became of it (Program item RV1, PR-6), as separate facts: recorded in Dispatch (who, when, the verdict, its
 * notes counted), and for a request what Yardmaster did with it: applied (the run, the task reopened, the attempt that starts at the
 * candidate, the command and the run's revision), refused and why, or waiting on the ledger. A refused request offers Request again
 * (its next try, at most five), which asks first, in the server's words, when the run must be continued to take it.
 */
const props = defineProps<{
  review: ReviewSummary
  task: string
  commit: string
  now: number
  retry: (continueRun: boolean) => Promise<ContinuationNeeded | string | null>
}>()

const MAX_TRIES = 5
const words = computed(() => applicationWords(props.review.application, props.task, props.commit))
const verdictWords = { request_changes: 'changes requested', comment: 'comment', approve: 'approved' } as const
const notes = computed(() => {
  const c = props.review.counts
  const n = c.open + c.resolved + c.withdrawn
  return n === 1 ? '1 note' : `${n} notes`
})
const tone = { success: 'text-success', warning: 'text-warning', error: 'text-error', muted: 'text-muted' }
const continuation = ref<ContinuationNeeded | null>(null)
const busy = ref(false)
const failure = ref('')

async function again(continueRun: boolean) {
  continuation.value = null
  failure.value = ''
  busy.value = true
  try {
    const out = await props.retry(continueRun)
    if (out instanceof ContinuationNeeded) continuation.value = out
    else if (out) failure.value = out
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <li class="flex flex-col gap-1 rounded border border-default/60 bg-elevated/20 px-2 py-1.5 text-xs" data-review-sent :data-review-id="review.id" :data-review-verdict="review.verdict" :data-review-application="review.application.state">
    <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
      <UBadge :label="verdictWords[review.verdict]" :color="review.verdict === 'approve' ? 'success' : review.verdict === 'request_changes' ? 'warning' : 'neutral'" variant="subtle" size="sm" />
      <span class="text-muted" data-review-recorded>Recorded in Dispatch: {{ notes }} by {{ review.createdBy || review.actor }} {{ relativeTime(review.createdAt, now, { suffix: true }) }}</span>
    </div>
    <p v-if="review.summary" class="whitespace-pre-wrap break-words">{{ review.summary }}</p>
    <p v-if="review.verdict === 'request_changes' || review.application.state !== 'none'" :class="tone[words.tone]" data-review-applied :data-review-attempt="review.application.attempt ?? ''" :data-review-run="review.application.run ?? ''">
      <span class="font-medium">{{ words.title }}.</span> {{ words.detail }}
      <NuxtLink v-if="review.application.run" :to="`/runs/${encodeURIComponent(review.application.run)}`" class="text-muted hover:underline">Open the run</NuxtLink>
    </p>
    <div v-if="review.application.state === 'refused'" class="flex flex-wrap items-center gap-2">
      <UButton
        label="Request again"
        icon="i-lucide-rotate-cw"
        size="xs"
        color="neutral"
        variant="outline"
        :loading="busy"
        :disabled="(review.application.try ?? 1) >= MAX_TRIES"
        data-review-retry
        @click="again(false)"
      />
      <span class="text-dimmed">try {{ review.application.try ?? 1 }} of {{ MAX_TRIES }}: judged against the run as it is now</span>
    </div>
    <p v-if="failure" class="text-error" data-review-retry-failure>{{ failure }}</p>
  </li>

  <UModal :open="!!continuation" title="Continue the run first?" :description="continuation?.words" :ui="{ content: 'max-w-md' }" @update:open="(o: boolean) => !o && (continuation = null)">
    <template #footer>
      <div class="flex w-full justify-end gap-2">
        <UButton label="Not now" color="neutral" variant="ghost" @click="continuation = null" />
        <UButton label="Continue the run and request again" icon="i-lucide-play" data-review-retry-continue @click="again(true)" />
      </div>
    </template>
  </UModal>
</template>
