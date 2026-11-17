<script setup lang="ts">
import type { ComparisonSupporting } from '~/utils/compare'
import { askClosedWords, brakemanWords, claimCutWords, claimSource, moreFindingsWords, reviewSource, verdictWord } from '~/utils/compareSelect'

/**
 * Supporting information, not evidence (Program item CP1, docs/plans/compare-crew.md, PR-7): what the candidates' agents said of their
 * own work, what their reviews found, and Brakeman's draft through BrakemanBrief, each marked with who said it, in its own panel
 * below the measured part. Nothing here colours, counts or reorders the measured part, and nothing is drawn as Markdown or HTML: an
 * agent's words are text. Brakeman's state is said (queued, drafting, briefed, or why there is none), and Ask Brakeman to compare
 * queues one request by hand (`POST /api/brakeman/requests`, kind `compare`, the comparison's launched run): the template asks once,
 * when every lane is done, and one lost in a crash is asked again here.
 */
const props = defineProps<{ supporting: ComparisonSupporting | null | undefined; run: string; state?: string }>()
/** Why a draft is asked no more: the selection is answered (askClosedWords). */
const askClosed = computed(() => askClosedWords(props.state))

const bk = useBrakeman()
const b = computed(() => props.supporting?.brakeman ?? null)
/** The request as the live store of requests holds it: it moves before the comparison is read again. */
const req = computed(() => (b.value?.request ? (bk.requests.value.find((r) => r.id === b.value!.request) ?? null) : null))
watch(
  () => b.value?.request,
  (id) => {
    if (id && !req.value) void bk.readRequest(id)
  },
  { immediate: true },
)
const brakeman = computed(() => brakemanWords(b.value, req.value))
const claims = computed(() => props.supporting?.claims ?? [])
const reviews = computed(() => props.supporting?.reviews ?? [])

const asking = ref(false)
const askWords = ref('')
async function ask() {
  if (asking.value || !props.run) return
  asking.value = true
  askWords.value = ''
  try {
    const r = await bk.ask({ kind: 'compare', subjects: [`run:${props.run}`] })
    askWords.value = `Asked: ${r.id} is ${r.state}.`
  } catch (e) {
    askWords.value = `Not asked: ${(e as Error).message}`
  } finally {
    asking.value = false
  }
}
</script>

<template>
  <section class="flex min-w-0 flex-col gap-3 rounded-lg border border-dashed border-default p-3" data-compare-supporting>
    <header class="flex min-w-0 flex-col gap-0.5">
      <h3 class="text-sm font-semibold text-highlighted">Supporting information, not evidence</h3>
      <p class="text-xs text-muted">What the agents, their reviews and Brakeman said. The measured part above is read from git and the ledger alone; nothing here changes it.</p>
    </header>

    <div class="flex min-w-0 flex-col gap-1.5" data-compare-claims>
      <h4 class="text-xs font-medium text-muted">The candidates' own summaries</h4>
      <p v-if="!claims.length" class="text-xs text-dimmed">No candidate's agent left a summary.</p>
      <ul v-else class="flex flex-col gap-1.5">
        <li v-for="c in claims" :key="`${c.task}/${c.attempt}`" class="flex min-w-0 flex-col gap-0.5 text-sm" :data-compare-claim="c.task">
          <span class="text-xs text-muted" data-compare-source>{{ claimSource(c.task, c.attempt) }}</span>
          <p class="whitespace-pre-wrap break-words text-default">{{ c.summary }}</p>
          <span v-if="claimCutWords(c)" class="text-xs text-muted" data-compare-claim-cut>{{ claimCutWords(c) }}</span>
        </li>
      </ul>
    </div>

    <div v-if="reviews.length" class="flex min-w-0 flex-col gap-1.5" data-compare-reviews>
      <h4 class="text-xs font-medium text-muted">Their reviews</h4>
      <ul class="flex flex-col gap-1.5">
        <li v-for="r in reviews" :key="`${r.task}/${r.attempt}`" class="flex min-w-0 flex-col gap-0.5 text-sm" :data-compare-review="r.task">
          <span class="text-xs text-muted" data-compare-source>{{ reviewSource(r.task, r.attempt) }}: {{ verdictWord(r.verdict) }}</span>
          <ul v-if="r.findings.length" class="flex list-disc flex-col gap-0.5 ps-5 text-xs text-default">
            <li v-for="(f, i) in r.findings" :key="i" class="break-words">{{ f }}</li>
          </ul>
          <span v-if="moreFindingsWords(r)" class="text-xs text-muted" data-compare-findings-more>{{ moreFindingsWords(r) }}</span>
        </li>
      </ul>
    </div>

    <div class="flex min-w-0 flex-col gap-1.5" data-compare-brakeman :data-brakeman-state="req?.state ?? b?.state ?? ''">
      <div class="flex min-w-0 flex-wrap items-center gap-2">
        <h4 class="text-xs font-medium text-muted">Brakeman's draft</h4>
        <span class="min-w-0 break-words text-xs text-default" data-compare-brakeman-words>{{ brakeman.words }}</span>
        <UButton label="Ask Brakeman to compare" icon="i-lucide-sparkles" size="xs" color="neutral" variant="outline" class="ms-auto max-lg:min-h-11" :loading="asking" :disabled="asking || brakeman.open || !run || !!askClosed" data-compare-ask-brakeman @click="ask" />
      </div>
      <p v-if="askClosed" class="break-words text-xs text-muted" data-compare-ask-closed>{{ askClosed }}</p>
      <p v-if="askWords" class="break-words text-xs text-muted" data-compare-ask-words>{{ askWords }}</p>
      <!-- Brakeman's table scrolls inside its card, never the page. -->
      <div v-if="brakeman.briefed && req" class="min-w-0 overflow-x-auto" data-compare-brief>
        <BrakemanBrief :key="req.id" :request="req" />
      </div>
    </div>
  </section>
</template>
