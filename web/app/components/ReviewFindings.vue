<script setup lang="ts">
import type { ReviewFinding } from '~/composables/useReview'
import { candidateName } from '~/utils/dispatch'
import { findingsInOrder, locatedWords, markBlocked, noteWhere, findingStateWords } from '~/utils/review'
import { relativeTime } from '~/utils/sessions'

/**
 * The findings of every review of the ticket's task across its candidates (Program item RV1, docs/plans/review-repair.md, PR-7): open
 * first, each with the candidate it was made on, where its lines are on the candidate shown (the server's located words: same lines,
 * moved, the lines changed, the file is gone, renamed), what the repair attempts said they addressed (the agent's claim, never its
 * resolution), its last mark, and Resolved / Still open / Withdraw against the candidate shown, each with an optional note. A located
 * state never changes a finding's state: only a mark does, made by a person against a later candidate of its task, and Resolved never on
 * the commit the finding was made on (the server judges both again, and its refusal is said in its words).
 */
const props = defineProps<{
  findings: readonly ReviewFinding[]
  /** The candidate shown, which a mark is made against. */
  candidate: string
  error: string
  loading: boolean
  now: number
  /** Marks a finding: '' once recorded, else why not in words. */
  mark: (f: ReviewFinding, verdict: 'resolved' | 'open' | 'withdrawn', note: string) => Promise<string>
}>()
const emit = defineEmits<{ open: [f: ReviewFinding] }>()

const shown = computed(() => findingsInOrder(props.findings))
const open = computed(() => props.findings.filter((f) => f.state === 'open').length)
const verdicts = [
  { verdict: 'resolved', label: 'Resolved', icon: 'i-lucide-check' },
  { verdict: 'open', label: 'Still open', icon: 'i-lucide-circle-dot' },
  { verdict: 'withdrawn', label: 'Withdraw', icon: 'i-lucide-undo-2' },
] as const
/** The mark being made: its finding, its verdict, the note typed, why not, and whether it is on its way. */
const making = ref<{ id: string; verdict: 'resolved' | 'open' | 'withdrawn'; note: string; why: string; busy: boolean } | null>(null)
const blocked = computed(() => (making.value ? (markBlocked(props.findings.find((f) => f.id === making.value!.id)?.candidate ?? '', props.candidate, making.value.verdict) ?? '') : ''))

function start(f: ReviewFinding, verdict: 'resolved' | 'open' | 'withdrawn') {
  making.value = { id: f.id, verdict, note: '', why: '', busy: false }
}
async function confirm(f: ReviewFinding) {
  const m = making.value
  if (!m || m.id !== f.id || blocked.value) return
  m.busy = true
  m.why = ''
  const why = await props.mark(f, m.verdict, m.note).finally(() => {
    if (making.value === m) m.busy = false
  })
  if (making.value !== m) return
  if (why) m.why = why
  else making.value = null
}
function intentLabel(f: ReviewFinding): string {
  return f.intent === 'change' ? (f.severity ?? 'change') : f.intent
}
function where(f: ReviewFinding): string {
  if (f.kind === 'check') return `${f.check ? `${f.check.phase} ${f.check.id}` : 'a check'} failed`
  return noteWhere(f)
}
</script>

<template>
  <section class="flex flex-col gap-2" data-review-findings :data-review-findings-count="findings.length" :data-review-findings-open="open">
    <div class="flex items-center gap-2">
      <h3 class="text-sm font-semibold text-highlighted">Findings</h3>
      <UBadge v-if="findings.length" :label="`${open} open of ${findings.length}`" :color="open ? 'warning' : 'success'" variant="subtle" size="sm" />
      <UIcon v-if="loading" name="i-lucide-loader" class="size-3 animate-spin text-dimmed" />
    </div>
    <p class="text-xs text-dimmed">Every review's findings of this task, located on the candidate shown. Only a mark made here changes a finding's state.</p>
    <p v-if="error" class="text-xs text-warning" data-review-findings-error>The findings could not be read: {{ error }}</p>
    <p v-else-if="!findings.length && !loading" class="text-xs text-dimmed">No finding yet: a review's notes become findings once it is sent.</p>
    <ul v-if="shown.length" class="flex flex-col gap-1.5">
      <li
        v-for="f in shown"
        :key="f.id"
        class="flex flex-col gap-1 rounded border border-default/60 bg-elevated/20 px-2 py-1.5 text-xs"
        data-review-finding
        :data-finding-id="f.id"
        :data-finding-state="f.state"
        :data-finding-kind="f.kind"
        :data-finding-intent="f.intent"
        :data-finding-located="f.locatedOn === candidate ? (f.located?.state ?? '') : ''"
      >
        <div class="flex min-w-0 flex-wrap items-center gap-1.5">
          <UBadge :label="findingStateWords(f.state).label" :color="findingStateWords(f.state).color" variant="subtle" size="sm" data-finding-state-badge />
          <UBadge :label="intentLabel(f)" color="neutral" variant="outline" size="sm" />
          <button v-if="f.kind === 'line' || f.kind === 'file'" type="button" class="min-w-0 break-all text-left font-mono text-muted hover:underline" data-finding-open @click="emit('open', f)">{{ where(f) }}</button>
          <span v-else class="text-muted">{{ where(f) }}</span>
          <span class="ml-auto text-dimmed" :title="f.candidate">on {{ candidateName(f.candidate) }}</span>
        </div>
        <p class="whitespace-pre-wrap break-words" data-finding-message>{{ f.message }}</p>
        <p v-if="f.resolution" class="text-muted">Asked: {{ f.resolution }}</p>
        <p v-if="f.locatedOn === candidate && f.located" class="text-muted" data-finding-located-words>On this candidate: {{ locatedWords(f.located) }}</p>
        <p v-else-if="f.kind === 'line'" class="text-dimmed">Located on {{ f.locatedOn ? candidateName(f.locatedOn) : 'no candidate yet' }}<template v-if="f.located">: {{ locatedWords(f.located) }}</template></p>
        <ul v-if="f.addressed?.length" class="flex flex-col gap-0.5" data-finding-addressed>
          <li v-for="a in f.addressed" :key="`${a.run}/${a.attempt}`" class="text-info">The agent says (attempt {{ a.attempt }}): {{ a.note || 'addressed' }}</li>
        </ul>
        <p v-if="f.latest" class="text-dimmed" data-finding-latest>
          Marked {{ f.latest.verdict === 'open' ? 'still open' : f.latest.verdict }} by {{ f.latest.createdBy || f.latest.actor }} on {{ candidateName(f.latest.candidate) }} {{ relativeTime(f.latest.createdAt, now, { suffix: true }) }}<template v-if="f.latest.note">: {{ f.latest.note }}</template>
        </p>
        <div class="flex flex-wrap items-center gap-1">
          <UButton
            v-for="v in verdicts"
            :key="v.verdict"
            :label="v.label"
            :icon="v.icon"
            size="xs"
            color="neutral"
            :variant="making?.id === f.id && making.verdict === v.verdict ? 'soft' : 'ghost'"
            :data-finding-mark="v.verdict"
            @click="start(f, v.verdict)"
          />
        </div>
        <div v-if="making?.id === f.id" class="flex flex-col gap-1" data-finding-mark-form :data-finding-mark-verdict="making.verdict">
          <UTextarea v-model="making.note" :rows="1" autoresize :maxrows="4" :maxlength="1000" placeholder="A note on the mark (optional)" aria-label="A note on the mark" />
          <p v-if="blocked" class="text-warning" data-finding-mark-blocked>{{ blocked }}</p>
          <p v-if="making.why" class="text-error" data-finding-mark-error>{{ making.why }}</p>
          <div class="flex justify-end gap-1">
            <UButton label="Cancel" size="xs" color="neutral" variant="ghost" @click="making = null" />
            <UButton :label="`Mark it ${making.verdict === 'open' ? 'still open' : making.verdict} on ${candidateName(candidate)}`" size="xs" :disabled="!!blocked" :loading="making.busy" data-finding-mark-confirm @click="confirm(f)" />
          </div>
        </div>
      </li>
    </ul>
  </section>
</template>
