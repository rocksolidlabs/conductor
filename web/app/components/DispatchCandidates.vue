<script setup lang="ts">
import type { CandidateReceipt, RunCandidate } from '~/composables/useDispatch'
import { candidateKey } from '~/composables/useRunCandidates'
import { type ReceiptPick, candidateWords, findingsCount, findingsWords, followPick, pickReceipt, receiptChips, receiptKey, receiptName, receiptState, repairs } from '~/utils/dispatchEvidence'
import { shortCommit } from '~/utils/dispatchPlans'
import { REVIEW_ENTRY } from '~/composables/useReview'
import { candidateRef, parseCandidate } from '~/utils/dispatch'
import { previewPath } from '~/utils/capture'
import { checkoutOfAttempt } from '~/composables/useCandidatePreview'
import { runCompare } from '~/utils/compare'
import { SELECTION_WORDS, candidateSelection } from '~/utils/compareSelect'
import { noCandidateWords } from '~/utils/dispatchEvidence'
import { contextCountWords } from '~/utils/attemptContext'

/**
 * A run's candidates (Program item DS2, docs/plans/dispatch-evidence.md, "web/app", PR-11), read from the ledger (GET
 * /api/runs/{run}/candidates), so a run the server let go still shows them: each task's attempts that submitted something, newest first,
 * a page of tasks at a time with More tasks following the page's `next`, so every task is reachable; each with its commit and Copy,
 * whether it stands as the task's result, was withdrawn or accepted anyway, and its receipts as chips with where each ran, opened in full
 * (DispatchReceipt) with the log and its hash; a review's verdict and its findings counted, their text read from the task route when the
 * review is opened (GET /api/runs/{run}/tasks/{task}, which has them whole); then the run's repairs (DispatchRepairs), who took each up
 * read from the same route, every attempt with when it started, one that never submitted included, a record's too. The pages shown
 * are read again from the start, gathered for the window, when the run's revision moves (its run events, through the live store): the
 * reads are useRunCandidates'. A list, never a table: one column on a phone. On a ticket's page (RV1), each standing work candidate
 * offers Review, the ticket's Review view at it, with the open findings of its task counted; a failed receipt opened offers Prepare a
 * repair there; and a person's repairs (the ticket's reviews that asked for changes) are listed with the run's (PR-7). A comparison's
 * run (its `compare` part, as the page read the run or the live store holds it) labels each work candidate "selected" or "not selected" once its selection is
 * applied (Program item CP1, PR-7), read from the comparison alone, in neutral ink.
 * A candidate whose attempt was given context says how many blocks ("context: 3 blocks"); opened, the list (AttemptContext, CT-3), read
 * from the task route as a review's findings are.
 * A candidate whose attempt's preview is offered now offers Open in Conductor, the preview page (PV1). A continuation with no attempt of its
 * own that submitted says its results are the run it continues', whose candidates that run lists (noCandidateWords).
 */
const props = defineProps<{
  runId: string
  /** The run's revision in the ledger as the live store holds it: a move reads the candidates again. Absent for a record. */
  revision?: number
  /** The run is a comparison's, as the page that draws it read the run (a record the live store leaves out among them). */
  compare?: boolean
  /** The run this one continues, when it is a continuation: its results kept from it are that run's candidates, listed with it. */
  continues?: string
}>()

const copy = useCopy()
// Open in Conductor (docs/plans/browser-evidence.md): a candidate whose attempt's preview the live run offers now, framed on the
// preview page.
const live = useAttention()
function previewOf(c: RunCandidate): string {
  const t = live.runOf(props.runId)?.tasks?.find((x) => x.id === c.task)
  return t?.attempt?.n === c.attempt && t.attempt.preview ? previewPath({ run: props.runId, task: c.task, attempt: c.attempt }) : ''
}
// A candidate's preview (PV1 PR-8): Preview this candidate on a work candidate with its commit; the live run's attempt names its
// checkout while one is open, and then Open and Close are offered.
const previews = useCandidatePreview(() => props.runId)
const router = useRouter()
// The checkout open of any candidate: the live run's current attempt's, else the task route's read of the attempt, which is read for every
// task with a candidate the live run does not name as current (an older attempt, a run the server no longer holds), and again after
// each command, so an open checkout is offered Open and Close wherever it was opened from.
function checkoutOf(c: RunCandidate): { seq: number; state: string } | null {
  const a = live.runOf(props.runId)?.tasks?.find((x) => x.id === c.task)?.attempt
  return checkoutOfAttempt(c.attempt, a, details.get(c.task)?.attempts)
}
async function previewCandidate(c: RunCandidate) {
  const opened = await previews.open(c.task, c.attempt)
  // Its checkout as the task route says it, before the page moves, so the card offers Open and Close on the way back; a refusal (open
  // already, or two open) is read the same way, and offers them for what is open.
  await readTask(c.task)
  if (opened) await router.push(previewPath({ run: props.runId, task: c.task, attempt: c.attempt, seq: opened.seq }))
}
async function closeCandidate(task: string, attempt: number, seq: number, run = props.runId, on?: string) {
  await previews.close(task, attempt, seq, run === props.runId ? undefined : run, on)
  if (run === props.runId) await readTask(task)
}
const { pages, tasks, managed, next, error, loading, more, opened, findings, findingsError, toggleFindings, details, detailsError, readTask, contextOpen, contextReading, toggleContext } = useRunCandidates({
  runId: () => props.runId,
  revision: () => props.revision,
})
watch(
  tasks,
  (ts) => {
    const current = (task: string) => live.runOf(props.runId)?.tasks?.find((x) => x.id === task)?.attempt?.n
    for (const t of ts) if (!details.has(t.task) && t.candidates.some((c) => c.kind === 'work' && c.commit && c.attempt !== current(t.task))) void readTask(t.task)
  },
  { immediate: true },
)
const fixes = computed(() => repairs(tasks.value, details))
const attention = useAttention()
/** The comparison of a compare run (the page's word for it, else the live store's), read only for one: its applied selection labels the candidates. */
const comparison = useComparison({ run: () => (props.compare || runCompare(attention.runOf(props.runId)) ? props.runId : '') }).comparison
const selectionOf = (c: RunCandidate) => (c.kind === 'work' ? candidateSelection(comparison.value, { task: c.task, attempt: c.attempt, commit: c.commit ?? '' }) : undefined)
const ckey = candidateKey
/** The ticket page's Review entry: absent anywhere else. */
const review = inject(REVIEW_ENTRY, null)
const reviewRef = (c: RunCandidate) => candidateRef({ run: props.runId, task: c.task, attempt: c.attempt, commit: c.commit ?? '' })
/** The person's repairs of this run: the ticket's reviews that asked for changes to one of its candidates, or were applied in it. */
const people = computed(() =>
  (review?.reviews() ?? []).filter((r) => r.verdict === 'request_changes' && (parseCandidate(r.candidate)?.run === props.runId || r.application.run === props.runId)),
)

function severityColor(s: string): 'error' | 'warning' | 'neutral' | 'info' {
  return s === 'blocker' ? 'error' : s === 'major' ? 'warning' : s === 'minor' ? 'info' : 'neutral'
}

// A receipt opened in full, kept by its identity and followed into the candidates as each read finds them: a check opened while it ran
// shows the finished receipt and its log once a read has it, and keeps showing it after a later read no longer holds the attempt.
const receiptOpen = ref(false)
const picked = ref<ReceiptPick | null>(null)
watch(tasks, (t) => {
  picked.value = followPick(t, picked.value)
})
const shown = computed(() => picked.value?.shown ?? null)
function openReceipt(c: RunCandidate, r: CandidateReceipt) {
  picked.value = pickReceipt(c, r)
  receiptOpen.value = true
}

// The context a candidate's attempt was given (CT-3): its count on the card; opened, the list from the task route's read, made again
// at every open (useRunCandidates' toggleContext).
const contextOf = (c: RunCandidate) => details.get(c.task)?.attempts?.find((a) => a.n === c.attempt)?.context

const tone = { warning: 'text-warning', error: 'text-error', muted: 'text-muted', default: 'text-default', success: 'text-success' }
</script>

<template>
  <div class="flex flex-col gap-2" data-candidates :data-candidates-run="runId" :data-candidates-tasks="tasks.length">
    <div class="flex items-center gap-2">
      <h5 class="text-xs font-medium text-muted">Candidates</h5>
      <UIcon v-if="loading" name="i-lucide-loader" class="size-3 animate-spin text-dimmed" />
    </div>
    <p v-if="error" class="text-xs text-warning" data-candidates-error>The candidates could not be read: {{ error }}</p>
    <p v-else-if="!pages" class="text-xs text-dimmed">Reading the candidates…</p>
    <p v-else-if="!managed" class="text-xs text-dimmed">Not a managed run: it has no tasks, and so no candidates.</p>
    <p v-else-if="!tasks.some((t) => t.candidates.length)" class="text-xs text-dimmed" data-candidates-none>{{ noCandidateWords(continues) }}</p>

    <ul v-if="tasks.length" class="flex flex-col gap-2">
      <li v-for="t in tasks" v-show="t.candidates.length" :key="t.task" class="flex flex-col gap-1.5" :data-candidate-task="t.task">
        <div class="flex items-center gap-2 text-xs">
          <span class="font-mono font-medium text-highlighted">{{ t.task }}</span>
          <UBadge :label="t.kind" color="neutral" variant="outline" size="sm" />
        </div>
        <ol class="flex flex-col gap-1.5">
          <li
            v-for="c in t.candidates"
            :key="ckey(c)"
            class="flex flex-col gap-1 rounded border border-default/60 bg-elevated/20 px-2 py-1.5 text-xs"
            :data-candidate="ckey(c)"
            :data-candidate-state="c.state"
            :data-candidate-commit="c.commit ?? ''"
            :data-candidate-start="c.start ?? ''"
          >
            <div class="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
              <span class="font-medium">attempt {{ c.attempt }}</span>
              <template v-if="c.kind === 'work' && c.commit">
                <code class="font-mono text-default" :title="c.commit" data-candidate-commit-short>{{ shortCommit(c.commit) }}</code>
                <IconButton icon="i-lucide-copy" size="xs" color="neutral" variant="ghost" :label="`Copy the commit of ${t.task} attempt ${c.attempt}`" data-candidate-copy @click="copy(c.commit, 'Commit copied', c.commit)" />
              </template>
              <span v-else-if="c.kind === 'review' && c.start" class="text-muted">reviewed <code class="font-mono" :title="c.start">{{ shortCommit(c.start) }}</code></span>
              <UBadge v-if="c.standing" label="result" color="primary" variant="subtle" size="sm" title="The task's result now" data-candidate-standing />
              <UBadge
                v-if="selectionOf(c)"
                :label="SELECTION_WORDS[selectionOf(c)!]"
                color="neutral"
                :variant="selectionOf(c) === 'selected' ? 'outline' : 'subtle'"
                size="sm"
                :class="selectionOf(c) === 'selected' ? 'text-default' : 'text-muted'"
                :data-candidate-selection="selectionOf(c)"
              />
              <UBadge v-if="c.withdrawn" label="withdrawn" color="neutral" variant="subtle" size="sm" title="A withdrawal took this result back" data-candidate-withdrawn />
              <UBadge
                v-if="c.acceptedAnyway"
                label="accepted anyway"
                color="warning"
                variant="subtle"
                size="sm"
                :title="`${c.acceptedAnyway.actor} took this result though a check failed`"
                data-candidate-accepted-anyway
              />
              <span :class="tone[candidateWords(c).tone]" data-candidate-words>{{ candidateWords(c).text }}</span>
              <UButton v-if="previewOf(c)" label="Open in Conductor" icon="i-lucide-scan" size="xs" color="neutral" variant="ghost" :to="previewOf(c)" :data-candidate-preview-open="ckey(c)" />
              <template v-if="c.kind === 'work' && c.commit">
                <template v-if="checkoutOf(c)">
                  <UButton label="Open the candidate's preview" icon="i-lucide-scan" size="xs" color="neutral" variant="ghost" :to="previewPath({ run: runId, task: c.task, attempt: c.attempt, seq: checkoutOf(c)!.seq })" :data-candidate-checkout-open="ckey(c)" />
                  <UButton label="Close" icon="i-lucide-circle-stop" size="xs" color="neutral" variant="ghost" :loading="previews.busy.value === `${c.task}:${c.attempt}:close`" :data-candidate-checkout-close="ckey(c)" @click="closeCandidate(c.task, c.attempt, checkoutOf(c)!.seq)" />
                </template>
                <UButton v-else label="Preview this candidate" icon="i-lucide-git-commit-horizontal" size="xs" color="neutral" variant="ghost" :loading="previews.busy.value === `${c.task}:${c.attempt}:open`" :data-candidate-preview="ckey(c)" @click="previewCandidate(c)" />
              </template>
              <span v-if="previews.refused.value[`${c.task}:${c.attempt}`]" class="text-muted" :data-candidate-preview-refused="ckey(c)">{{ previews.refused.value[`${c.task}:${c.attempt}`] }}</span>
              <template v-for="o in previews.busyOpen.value[`${c.task}:${c.attempt}`] ?? []" :key="`${o.run}/${o.task}/${o.attempt}/${o.seq}`">
                <UButton :label="`Open ${o.task} attempt ${o.attempt}'s preview`" icon="i-lucide-scan" size="xs" color="neutral" variant="ghost" :to="previewPath(o)" :data-candidate-busy-open="`${o.run}/${o.task}/${o.attempt}`" />
                <UButton label="Close it" icon="i-lucide-circle-stop" size="xs" color="neutral" variant="ghost" :loading="previews.busy.value === `${o.task}:${o.attempt}:close`" :data-candidate-busy-close="`${o.run}/${o.task}/${o.attempt}`" @click="closeCandidate(o.task, o.attempt, o.seq, o.run, `${c.task}:${c.attempt}`)" />
              </template>
              <span v-if="review && c.kind === 'work' && c.commit && c.standing" class="ml-auto flex flex-none items-center gap-1">
                <UBadge
                  v-if="review.openFindings(runId, t.task)"
                  :label="`${review.openFindings(runId, t.task)} open`"
                  color="warning"
                  variant="subtle"
                  size="sm"
                  :title="`Open findings of ${t.task}'s reviews`"
                  data-candidate-open-findings
                />
                <UButton label="Review" icon="i-lucide-scan-eye" size="xs" color="neutral" variant="outline" :to="review.href(reviewRef(c))" data-candidate-review />
              </span>
            </div>
            <div v-if="c.kind === 'review' && c.verdict" class="flex flex-wrap items-center gap-2">
              <UBadge :label="c.verdict === 'approved' ? 'approved' : 'needs changes'" :color="c.verdict === 'approved' ? 'success' : 'warning'" variant="subtle" size="sm" data-candidate-verdict />
              <template v-if="findingsCount(c.findings)">
                <span class="text-muted" :data-candidate-findings="findingsCount(c.findings)">{{ findingsWords(c.findings) }}</span>
                <UButton
                  :label="opened.has(ckey(c)) ? 'Hide findings' : 'Findings'"
                  :icon="opened.has(ckey(c)) ? 'i-lucide-chevron-down' : 'i-lucide-chevron-right'"
                  size="xs"
                  color="neutral"
                  variant="ghost"
                  :aria-expanded="opened.has(ckey(c))"
                  data-candidate-findings-open
                  @click="toggleFindings(c)"
                />
              </template>
            </div>
            <template v-if="opened.has(ckey(c))">
              <p v-if="findingsError.get(ckey(c))" class="text-warning">The findings could not be read: {{ findingsError.get(ckey(c)) }}</p>
              <p v-else-if="!findings.has(ckey(c))" class="text-dimmed">Reading the findings…</p>
              <ul v-else class="flex flex-col gap-0.5" data-candidate-findings-list>
                <li v-for="(f, i) in findings.get(ckey(c))" :key="i" class="flex items-start gap-1.5" data-candidate-finding>
                  <UBadge :label="f.severity" :color="severityColor(f.severity)" variant="subtle" size="sm" class="flex-none" />
                  <span v-if="f.path" class="flex-none font-mono text-muted">{{ f.path }}{{ f.line ? `:${f.line}` : '' }}</span>
                  <span class="min-w-0 break-words" data-candidate-finding-message>{{ f.message }}</span>
                </li>
              </ul>
            </template>
            <div v-if="c.context" class="flex flex-col gap-1">
              <UButton
                :label="contextCountWords(c.context)"
                :icon="contextOpen.has(ckey(c)) ? 'i-lucide-chevron-down' : 'i-lucide-chevron-right'"
                size="xs"
                color="neutral"
                variant="ghost"
                class="self-start"
                :aria-expanded="contextOpen.has(ckey(c))"
                :data-candidate-context="c.context"
                @click="toggleContext(c)"
              />
              <template v-if="contextOpen.has(ckey(c))">
                <p v-if="contextReading.has(ckey(c))" class="text-dimmed" data-candidate-context-reading>Reading the context…</p>
                <p v-else-if="detailsError.get(c.task)" class="text-warning">The context could not be read: {{ detailsError.get(c.task) }}</p>
                <AttemptContext v-else-if="contextOf(c)?.length" :run-id="runId" :task="c.task" :attempt="c.attempt" :rows="contextOf(c)!" />
                <p v-else class="text-dimmed" data-candidate-context-none>The task's read lists no context for this attempt.</p>
              </template>
            </div>
            <ul v-if="c.receipts.length" class="flex flex-wrap gap-1.5" data-candidate-receipts>
              <li v-for="r in c.receipts" :key="receiptKey(r)">
                <UButton
                  size="xs"
                  color="neutral"
                  variant="outline"
                  class="gap-1.5"
                  :title="`${receiptName(r)}: ${receiptChips(r).map((x) => x.label).join(', ')}, where it ran: ${r.where}`"
                  :data-receipt="receiptKey(r)"
                  :data-receipt-state="receiptState(r)"
                  :data-receipt-where="r.where"
                  @click="openReceipt(c, r)"
                >
                  <span class="font-mono">{{ receiptName(r) }}</span>
                  <UBadge v-for="x in receiptChips(r)" :key="x.label" :label="x.label" :color="x.color" variant="subtle" size="sm" />
                  <span class="text-dimmed" data-receipt-where-label>{{ r.where }}</span>
                </UButton>
              </li>
            </ul>
            <p v-else-if="c.kind === 'work'" class="text-dimmed">No check ran against this commit.</p>
          </li>
        </ol>
        <p v-if="t.more" class="text-xs text-dimmed">{{ t.more }} older {{ t.more === 1 ? 'attempt' : 'attempts' }}: on the run's page.</p>
      </li>
    </ul>
    <UButton v-if="next" label="More tasks" icon="i-lucide-chevrons-down" size="xs" color="neutral" variant="ghost" class="self-start" :loading="loading" data-candidates-more @click="more" />

    <DispatchRepairs :repairs="fixes" :errors="detailsError" :people="people" />
    <DispatchReceipt
      v-model:open="receiptOpen"
      :run-id="runId"
      :task="shown?.task ?? ''"
      :attempt="shown?.attempt ?? 0"
      :commit="shown?.commit ?? ''"
      :receipt="shown?.receipt ?? null"
      :prepare="review && shown?.commit && shown.receipt ? review.href(candidateRef({ run: runId, task: shown.task, attempt: shown.attempt, commit: shown.commit }), `${shown.receipt.id}/${shown.receipt.k}`) : undefined"
    />
  </div>
</template>
