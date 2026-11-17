<script setup lang="ts">
import type { LinkRead, LinkReadSummary, NewLink, TicketLink } from '~/composables/useDispatch'
import { candidateName, linkLabel, parseCandidate, verificationWords, type CandidateOption } from '~/utils/dispatch'
import { relativeTime } from '~/utils/sessions'
import { checksWords, githubHref, linkStateWords, readAge } from '~/utils/tracker'
import { itemRef } from '~/utils/publication'

/**
 * A ticket's links outward (docs/plans/dispatch.md, PR-11): where it was delivered, the merged pull request's or the release's URL that a
 * person adds (a merge is such a link here; its merged state is never read, IN1a's), and pages of another tracker's (external); each with
 * its note, opened in a new tab, and removed. A delivery link is what a close policy's "a delivery link" asks for. The run links are the
 * Runs section's (DispatchRuns.vue). Each add and remove is one operation on the ticket, made by the page; a refusal is said here.
 * A delivery may name the candidate it delivers (Program item DS2, PR-12): one of the ticket's runs' standing results, picked with its
 * receipts in short; the server reads the candidate's receipts from the ledger and the link keeps them, so the row says whether it was
 * verified ("Verified: build#2 passed 2 checks", "Not verified: build#3 failed the check never (exit 1)") from what was recorded, and
 * that the result was withdrawn since when the runs' candidates say so. A decision record (reserved, DR1) is the Decisions section's.
 * A link to a GitHub issue or pull request (IN1a) shows what GitHub says of it, from its last read: open, closed or merged (with when and
 * the merge commit), a pull request's checks at its head commit as a row of chips with their counts, and, when the read could not hold
 * every check, "incomplete: N checks not read, see them on GitHub" in place of any word that they all passed; the read's age, Read again,
 * and a failure's words. GitHub's text is drawn as text, a link only where githubHref admits it.
 */
const props = defineProps<{
  links: readonly TicketLink[]
  locked: boolean
  now: number
  /** A link made: '' once the store has it, else why not, in words. */
  add: (link: NewLink) => Promise<string>
  remove: (link: TicketLink) => Promise<boolean>
  /** The candidates a delivery may name: the ticket's runs' standing results (utils/dispatch.ts, candidateOptions). */
  candidates?: readonly CandidateOption[]
  /** The runs' candidates are being read. */
  candidatesLoading?: boolean
  /** A run's candidates could not be read, in words ('' when every read answered): they are asked again until one does. */
  candidatesError?: string
  /** Whether a delivered candidate's result was withdrawn since, as its run's candidates read now say. */
  withdrawn?: (candidate: string) => boolean
  /** The links' last reads of their GitHub items, by link id (the ticket's `linkReads`). */
  reads?: Record<string, LinkReadSummary>
  /** Whether a link's URL names a GitHub item a token is kept for: it can be read. */
  readable?: (url: string) => boolean
  /** A link's item read again from GitHub: '' once read, else why not. */
  readAgain?: (link: TicketLink) => Promise<string>
  /** A link's last read whole, its checks among it, or null. */
  readWhole?: (link: TicketLink) => Promise<LinkRead | null>
  /** Opens Publish to GitHub with the link as its target (IN1a): offered on a link to a GitHub item. */
  publish?: (link: TicketLink) => void
}>()

type FormKind = Exclude<NewLink['kind'], 'run'>
const KINDS: ReadonlyArray<{ value: FormKind; label: string; icon: string; description: string }> = [
  { value: 'delivery', label: 'Delivery', icon: 'i-lucide-package-check', description: "Where it was delivered: the merged pull request's or the release's URL" },
  { value: 'external', label: 'External', icon: 'i-lucide-external-link', description: "A page of another tracker's, or anything else it relates to" },
]
const kindOf = (k: string) => KINDS.find((x) => x.value === k) ?? KINDS[1]!

const shown = computed(() => {
  const order = (l: TicketLink) => (l.kind === 'delivery' ? 0 : 1)
  return props.links.filter((l) => l.kind !== 'run' && l.kind !== 'decision' && !l.deletedAt).sort((a, b) => order(a) - order(b) || a.createdAt.localeCompare(b.createdAt))
})

const adding = ref(false)
const kind = ref<FormKind>('delivery')
const url = ref('')
const note = ref('')
/** The candidate a delivery names, NONE for none: a select's value is never empty. */
const NONE = 'none'
const candidate = ref(NONE)
const candidateItems = computed(() => [
  { label: 'No candidate', value: NONE, description: 'A delivery that names no candidate is not verified' },
  ...(props.candidates ?? []).map((c) => ({ label: c.label, value: c.value, description: c.description })),
])
const error = ref('')
/**
 * Each opening of the form, and each cancel of it, is a new one: an add answers only the opening it was sent from, so one still on its way
 * after a cancel never closes, or says its refusal in, a form opened since with another link typed in it.
 */
const opening = ref(0)
/** The opening whose add is on its way, null when none is. */
const sendingFrom = ref<number | null>(null)
const sending = computed(() => sendingFrom.value === opening.value && adding.value)
const urlProblem = computed(() => (url.value.trim() && !/^https?:\/\/[^\s/]+/i.test(url.value.trim()) ? 'An http or https URL' : ''))

function openForm(k: FormKind = 'delivery') {
  opening.value++
  kind.value = k
  url.value = ''
  note.value = ''
  // None until the person names one: a delivery's candidate is a person's statement of what it delivers.
  candidate.value = NONE
  error.value = ''
  adding.value = true
}

function cancel() {
  opening.value++
  adding.value = false
}

async function submit() {
  const u = url.value.trim()
  if (!u || urlProblem.value || sending.value) return
  const from = opening.value
  sendingFrom.value = from
  error.value = ''
  try {
    const named = kind.value === 'delivery' && candidate.value !== NONE ? { candidate: candidate.value } : {}
    const words = await props.add({ kind: kind.value, url: u, ...(note.value.trim() ? { note: note.value.trim() } : {}), ...named })
    if (from !== opening.value) return
    if (words) error.value = words
    else adding.value = false
  } finally {
    if (sendingFrom.value === from) sendingFrom.value = null
  }
}

const removing = ref('')
async function drop(l: TicketLink) {
  removing.value = l.id
  try {
    await props.remove(l)
  } finally {
    if (removing.value === l.id) removing.value = ''
  }
}

/** The note's bound, the store's, in characters. */
const MAX_NOTE = 1000

// A GitHub link's read (IN1a): its summary from the ticket's read, and its checks from its whole read, asked once per read recorded.
const summaryOf = (l: TicketLink) => props.reads?.[l.id]
const isGitHub = (l: TicketLink) => !!summaryOf(l) || !!props.readable?.(l.url)
/** The item's host, for the one rule of what is a link: the link's own URL's host. */
const hostOf = (url: string) => /^https:\/\/([^/?#]+)\//.exec(url)?.[1] ?? ''
const whole = ref(new Map<string, LinkRead | null>())
/** The words of a Read again that failed, by link, until a later read is recorded. */
const readFailed = ref(new Map<string, string>())
const wholeAsked = new Map<string, string>()
watch(
  () => shown.value.map((l) => `${l.id}@${summaryOf(l)?.readAt ?? ''}`).join(' '),
  () => {
    for (const l of shown.value) {
      const s = summaryOf(l)
      if (!s?.readAt || !props.readWhole) continue
      const mark = s.readAt
      if (wholeAsked.get(l.id) === mark) continue
      // A read recorded since (the server's own, another tab's): a failed press's words are the past.
      if (wholeAsked.has(l.id) && readFailed.value.has(l.id)) {
        const left = new Map(readFailed.value)
        left.delete(l.id)
        readFailed.value = left
      }
      wholeAsked.set(l.id, mark)
      void props
        .readWhole(l)
        .then((r) => {
          if (wholeAsked.get(l.id) === mark) whole.value = new Map(whole.value).set(l.id, r)
        })
        .catch(() => wholeAsked.delete(l.id))
    }
  },
  { immediate: true },
)
const readingNow = ref('')
async function readIt(l: TicketLink) {
  if (!props.readAgain || readingNow.value) return
  readingNow.value = l.id
  try {
    const words = await props.readAgain(l)
    readFailed.value = new Map(readFailed.value).set(l.id, words)
  } finally {
    readingNow.value = ''
  }
}
const checkWords = (c: { conclusion?: string; status: string }) => (c.conclusion ? (c.conclusion === 'success' ? 'passed' : c.conclusion === 'failure' ? 'failed' : c.conclusion.replace(/_/g, ' ')) : c.status === 'completed' ? 'done' : 'pending')

/** A delivery row's verification, from the receipt it recorded. */
const verification = (l: TicketLink) => verificationWords(l, !!l.candidate && !!props.withdrawn?.(l.candidate))
const tones = { warning: 'text-warning', error: 'text-error', muted: 'text-muted', default: 'text-default', success: 'text-success' }
</script>

<template>
  <section class="flex flex-col gap-2" data-ticket-links :data-count="shown.length">
    <div class="flex items-center gap-2">
      <h3 class="text-xs font-semibold uppercase tracking-wide text-muted">Links</h3>
      <div class="flex-1" />
      <UButton v-if="!locked && !adding" label="Add a link" icon="i-lucide-link" size="xs" color="neutral" variant="ghost" data-link-add-open @click="openForm()" />
    </div>
    <p v-if="!shown.length && !adding" class="text-sm text-dimmed">No delivery or external link yet.</p>
    <ul v-if="shown.length" class="flex flex-col gap-1.5">
      <li v-for="l in shown" :key="l.id" class="flex min-w-0 items-start gap-2 rounded-md px-3 py-2 text-sm ring ring-default" :data-link="l.ref" :data-link-kind="l.kind">
        <UIcon :name="kindOf(l.kind).icon" class="mt-0.5 size-4 flex-none text-muted" :title="kindOf(l.kind).label" />
        <div class="flex min-w-0 flex-1 flex-col gap-0.5">
          <div class="flex min-w-0 flex-wrap items-baseline gap-x-2">
            <UBadge :label="kindOf(l.kind).label" size="sm" variant="subtle" :color="l.kind === 'delivery' ? 'success' : 'neutral'" class="flex-none" />
            <a :href="l.url" target="_blank" rel="noopener noreferrer" class="min-w-0 truncate font-medium hover:underline" :title="l.url" data-link-url>{{ linkLabel(l.url) }}</a>
          </div>
          <p v-if="l.note" class="whitespace-pre-wrap break-words text-xs text-muted" data-link-note>{{ l.note }}</p>
          <div v-if="l.candidate" class="flex min-w-0 flex-col gap-0.5 text-xs" data-link-candidate :data-candidate="l.candidate" :data-link-verified="verification(l).state">
            <span class="flex min-w-0 flex-wrap items-center gap-x-1.5">
              <UIcon :name="verification(l).state === 'verified' ? 'i-lucide-shield-check' : 'i-lucide-shield-alert'" class="size-3.5 flex-none" :class="tones[verification(l).tone]" />
              <span :class="tones[verification(l).tone]" data-link-verification>{{ verification(l).text }}</span>
              <span class="font-mono text-dimmed" :title="parseCandidate(l.candidate)?.commit ?? l.candidate">{{ candidateName(l.candidate) }} · {{ parseCandidate(l.candidate)?.commit.slice(0, 7) ?? '' }}</span>
            </span>
            <span v-if="l.verified" class="text-dimmed">recorded {{ relativeTime(l.verified.at, now, { suffix: true }) }}<template v-if="l.verified.receipts.length">, where it ran: {{ [...new Set(l.verified.receipts.map((r) => r.where))].join(', ') }}</template></span>
            <span v-if="verification(l).withdrawn" class="text-warning" data-link-withdrawn>{{ verification(l).withdrawn }}</span>
          </div>
          <div v-if="isGitHub(l)" class="flex min-w-0 flex-col gap-1 text-xs" :data-link-read="l.id" :data-link-state="summaryOf(l) ? linkStateWords(summaryOf(l)!).key : 'unknown'">
            <template v-if="summaryOf(l)?.readAt">
              <span class="flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-1">
                <UBadge :label="linkStateWords(summaryOf(l)!).label" color="neutral" variant="outline" size="sm" class="flex-none" data-link-state-chip />
                <span v-if="summaryOf(l)!.merged && summaryOf(l)!.mergedAt" class="text-muted">merged {{ relativeTime(summaryOf(l)!.mergedAt!, now, { suffix: true }) }}</span>
                <span v-if="summaryOf(l)!.merged && summaryOf(l)!.mergeSha" class="font-mono text-dimmed" data-mono="commit" :title="summaryOf(l)!.mergeSha" data-link-merge-sha>{{ summaryOf(l)!.mergeSha!.slice(0, 7) }}</span>
                <span v-if="summaryOf(l)!.headSha" class="text-muted" data-link-check-counts>
                  · checks: {{ checksWords(summaryOf(l)!.checks).allPassed ? `all ${summaryOf(l)!.checks.passed} passed` : checksWords(summaryOf(l)!.checks).counts }}
                </span>
              </span>
              <span v-if="summaryOf(l)!.checks.incomplete" class="text-muted" data-link-incomplete>{{ checksWords(summaryOf(l)!.checks).incomplete }}</span>
              <ul v-if="whole.get(l.id)?.checks.length" class="flex flex-wrap gap-1" data-link-checks>
                <li v-for="c in whole.get(l.id)!.checks" :key="c.id" :data-link-check="c.id">
                  <a v-if="githubHref(c.url, hostOf(l.url))" :href="githubHref(c.url, hostOf(l.url))!" target="_blank" rel="noopener noreferrer" class="hover:underline">
                    <UBadge :label="`${c.name}: ${checkWords(c)}`" color="neutral" variant="subtle" size="sm" />
                  </a>
                  <UBadge v-else :label="`${c.name}: ${checkWords(c)}`" color="neutral" variant="subtle" size="sm" />
                </li>
              </ul>
            </template>
            <span class="flex flex-wrap items-center gap-x-1.5 text-dimmed">
              <span>{{ readingNow === l.id ? 'reading GitHub…' : `GitHub: ${readAge(summaryOf(l)?.readAt, now)}` }}</span>
              <UButton v-if="!locked && readAgain" label="Read again" icon="i-lucide-refresh-cw" size="xs" color="neutral" variant="link" class="px-0" :loading="readingNow === l.id" data-link-read-again @click="readIt(l)" />
            </span>
            <span v-if="readFailed.get(l.id)" class="text-warning" data-link-read-error>{{ readFailed.get(l.id) }}</span>
            <span v-else-if="summaryOf(l)?.error" class="text-warning" data-link-read-error>The last read failed: {{ summaryOf(l)!.error }}</span>
          </div>
          <p class="text-xs text-dimmed">added by {{ l.createdBy || 'admin' }} {{ relativeTime(l.createdAt, now, { suffix: true }) }}</p>
        </div>
        <IconButton v-if="!locked && publish && itemRef(l.url)" icon="i-lucide-send" size="xs" color="neutral" variant="ghost" class="flex-none" :label="`Publish to GitHub on ${linkLabel(l.url)}`" data-link-publish @click="publish(l)" />
        <IconButton v-if="!locked" icon="i-lucide-x" size="xs" color="neutral" variant="ghost" class="flex-none" :label="`Remove the link ${linkLabel(l.url)}`" :loading="removing === l.id" data-link-remove @click="drop(l)" />
      </li>
    </ul>

    <form v-if="adding && !locked" class="flex flex-col gap-2 rounded-md p-3 ring ring-default" data-link-form @submit.prevent="submit">
      <UAlert v-if="error" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="error" data-link-error />
      <URadioGroup v-model="kind" :items="KINDS.map((k) => ({ label: k.label, value: k.value, description: k.description }))" orientation="horizontal" size="sm" data-link-kind-pick />
      <UFormField :error="urlProblem || undefined" size="sm">
        <UInput v-model="url" :placeholder="kind === 'delivery' ? 'https://github.com/acme/api/pull/12' : 'https://…'" autofocus class="w-full" aria-label="The link's URL" data-link-url-input />
      </UFormField>
      <UInput v-model="note" :maxlength="MAX_NOTE" placeholder="A note (optional)…" size="sm" class="w-full" aria-label="The link's note" data-link-note-input />
      <UFormField v-if="kind === 'delivery'" label="The candidate it delivers" :hint="candidatesLoading ? 'reading the runs…' : undefined" description="One of this ticket's runs' standing results: its receipts are recorded with the link, and say whether it is verified." size="sm">
        <USelect v-model="candidate" :items="candidateItems" size="sm" class="w-full" aria-label="The candidate it delivers" data-link-candidate-pick />
        <p v-if="candidatesError" class="mt-1 text-xs text-warning" data-link-candidates-error>{{ candidatesError }}</p>
      </UFormField>
      <div class="flex justify-end gap-2">
        <UButton label="Cancel" color="neutral" variant="ghost" size="sm" data-link-cancel @click="cancel" />
        <UButton type="submit" label="Add" icon="i-lucide-plus" size="sm" :loading="sending" :disabled="!url.trim() || !!urlProblem" data-link-submit />
      </div>
    </form>
  </section>
</template>
