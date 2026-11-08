<script setup lang="ts">
import type { RunCandidate } from '~/composables/useDispatch'
import { ApiError } from '~/composables/useApi'
import { ContinuationNeeded, useCandidateDiff, useFileSides, useReview, useSentReviews, useShowWhenReady, type ReviewSummary, type SideText } from '~/composables/useReview'
import { useReviewDraft } from '~/composables/useReviewDraft'
import type { useTicketCandidates } from '~/composables/useTicketCandidates'
import { candidateRef, parseCandidate } from '~/utils/dispatch'
import type { DispatchOrder } from '~/utils/dispatchOrder'
import { shortCommit } from '~/utils/dispatchPlans'
import { CommandRefused } from '~/utils/tasks'
import {
  barTop,
  batchProblems,
  changeRequestOf,
  composerOnFileMove,
  noteFromSelection,
  noteIsOn,
  notesPerFile,
  offeredBases,
  repairBytes,
  reviewBody,
  withDraftBase,
  type BaseChoice,
  type ReviewCandidateOption,
  type DiffMark,
  type DiffSelection,
  type DraftNote,
  type Side,
} from '~/utils/review'

/**
 * A ticket's Review view (Program item RV1, docs/plans/review-repair.md, PR-6), the ticket page's `?view=review&candidate=…`: one
 * candidate of the ticket's runs and the base its diff is read against (ReviewCandidate), the files that differ (ReviewFiles) and each
 * file's diff in Monaco, where selecting lines on either side shows Add a note under them (or Ctrl+Shift+M); a note on a whole file
 * from its header, or on the candidate as a whole; the notes gathered in this browser's draft (ReviewDraft) and sent as one review with
 * a verdict and a summary; and the reviews of the candidate already sent, with what became of each (ReviewApplication). The draft's
 * base is the review's: a reload opens the diff at it, and a base-side note locks it. Every read goes through the page's ordering gate,
 * and the reviews are read again whenever the page reads the ticket again (its live events name the ticket); a stream's reset, which
 * drops every reply on its way, reads again whatever had not come in. Findings across
 * candidates, their marks and a failed check's repair are a later part of this view, which says so. One column on a phone.
 */
const props = defineProps<{
  ticketKey: string
  /** The candidate the route names (`<run>/<task>/<n>@<commit>`), '' for the first standing one. */
  candidate: string
  candidates: ReturnType<typeof useTicketCandidates>
  gate: DispatchOrder
  /** Counts the page's reads of the ticket: each reads the reviews again. */
  reads: number
  /** Counts the stream's starts the page saw (a snapshot, which resets the gate): each reads again what had not come in. */
  resets: number
  now: number
}>()
const emit = defineEmits<{ candidate: [ref: string] }>()

const review = useReview(props.gate)
const toast = useToast()

interface Option extends ReviewCandidateOption {
  run: string
  task: string
  c: RunCandidate
}
/** The candidates of the ticket's runs: every work attempt that submitted a commit, newest first per task, in the runs' order. */
const options = computed<Option[]>(() => {
  const out: Option[] = []
  for (const r of props.candidates.runs.value) {
    for (const t of r.tasks) {
      if (t.kind !== 'work') continue
      for (const c of t.candidates) {
        if (!c.commit) continue
        out.push({ value: candidateRef({ run: r.run, task: t.task, attempt: c.attempt, commit: c.commit }), label: `${t.task} · attempt ${c.attempt} · ${shortCommit(c.commit)} · ${r.label || r.run}`, standing: c.standing, run: r.run, task: t.task, c })
      }
    }
  }
  return out
})
const chosen = computed(() => props.candidate || options.value.find((o) => o.standing)?.value || options.value[0]?.value || '')
const cref = computed(() => parseCandidate(chosen.value))
const entry = computed(() => options.value.find((o) => o.value === chosen.value))
// The candidate resolved for a route that names none goes into the route: the view stays on it when another comes to stand (a request
// applied makes the repair's), and with it the draft being edited and a send's answer.
watch(
  chosen,
  (c) => {
    if (!props.candidate && c) emit('candidate', c)
  },
  { immediate: true },
)

/** The bases the page lists for the candidate (from the candidates it has read), null until it has read its run's candidates. */
const listed = computed<BaseChoice[] | null>(() => {
  const e = entry.value
  const base = e ? props.candidates.bases.get(e.run) : undefined
  if (!e || !base) return null
  const same = props.candidates.runs.value.find((r) => r.run === e.run)?.tasks.find((t) => t.task === e.task)?.candidates ?? []
  return offeredBases(base, e.c, same)
})

/** The candidate and base pairs the server refused a diff of (none of the run's chain): a draft on one is stale. */
const refused = ref(new Set<string>())
/**
 * The bases offered: those listed, and the draft's own while the server has not refused it, a result of the task the candidates read
 * (six a task) no longer list being still one the server takes; the diff is read at it, and a refusal makes the draft stale.
 */
const offered = computed<BaseChoice[] | null>(() => withDraftBase(listed.value, draft.value.base, refused.value.has(`${chosen.value}|${draft.value.base}`)))
const drafts = useReviewDraft({ ticket: () => props.ticketKey, candidate: () => chosen.value, offered: () => offered.value?.map((b) => b.commit) ?? null })
const draft = drafts.draft
const lockWords = computed(() => {
  const l = drafts.lock.value
  if (!l) return ''
  return `${l.count === 1 ? '1 note is' : `${l.count} notes are`} on the base ${shortCommit(l.base)}: send or drop ${l.count === 1 ? 'it' : 'them'} to compare against another.`
})
const staleWords = computed(() => (drafts.stale.value ? `This draft was made against ${shortCommit(draft.value.base)}, which this candidate no longer offers as a base.` : ''))
/** The base the diff is read at: the draft's, the run's ('') while it has none, or while it is one no longer offered. */
const readBase = computed(() => (drafts.stale.value ? '' : draft.value.base))

// The diff, read again when the candidate or the base moves: shown only while they are the ones wanted, cleared at a move.
const diffs = useCandidateDiff(review, {
  known: (b) => {
    if (!drafts.stale.value) drafts.knowBase(b)
  },
  refused: (k, b) => {
    refused.value = new Set(refused.value).add(`${k}|${b}`)
  },
})
const diff = computed(() => diffs.shown.value?.diff ?? null)
/** The candidate the diff shown is of. */
const diffKey = computed(() => diffs.shown.value?.key ?? '')
const diffError = diffs.error
const diffLoading = diffs.loading
watch([chosen, readBase], ([k, b]) => void diffs.read(k, b), { immediate: true })

// The file shown, and its two sides.
const file = ref('')
watch(diff, (d) => {
  if (d && !d.files.some((f) => f.path === file.value)) file.value = d.files[0]?.path ?? ''
})
const shownFile = computed(() => diff.value?.files.find((f) => f.path === file.value) ?? null)
const fileSides = useFileSides(review)
const sides = fileSides.sides
const sidesKey = computed(() => (diff.value && shownFile.value ? `${diffKey.value}:${diff.value.base}:${shownFile.value.path}` : ''))
watch(
  sidesKey,
  (key) => void fileSides.read(key && diff.value && shownFile.value ? { key, candidate: diffKey.value, base: diff.value.base, file: shownFile.value } : null),
  { immediate: true },
)
const textOf = (s: SideText | null) => (s && typeof s === 'object' ? s.text : '')
const ready = computed(() => sides.value.key === sidesKey.value && sides.value.base !== null && sides.value.candidate !== null)
const binary = computed(() => sides.value.base === 'binary' || sides.value.candidate === 'binary')
const truncatedSide = computed(() => [sides.value.base, sides.value.candidate].some((s) => s && typeof s === 'object' && s.truncated))
/**
 * Whether a note on the diff shown may be made: it is the chosen candidate's at the draft's base (a base offered), so a base-side note's
 * lines, and the anchor_commit the server gives them, are of the base the review is sent with.
 */
const notable = computed(() => !drafts.stale.value && diffs.at(chosen.value, draft.value.base))
const notableKey = computed(() => (notable.value ? `${chosen.value}|${draft.value.base}` : ''))

/** The draft's notes on the file shown, in its margin: a base-side note by the path the base has the file at (a rename's old one). */
const marks = computed<DiffMark[]>(() => {
  const f = shownFile.value
  if (!f) return []
  return draft.value.notes.filter((n) => n.kind === 'line' && noteIsOn(n, f) && n.side && n.from && n.to).map((n) => ({ side: n.side!, from: n.from!, to: n.to!, label: n.message }))
})
/** The notes on each file of the diff, both sides together. */
const notesByPath = computed(() => notesPerFile(draft.value.notes, diff.value?.files ?? []))

// A selection: the bar under it, and the note's form.
const selection = ref<DiffSelection | null>(null)
/** A note being made or edited: `at`, for a note on the diff, the candidate and the base it was made on; `inline` for a selection's form. */
const composer = ref<{ note: Omit<DraftNote, 'key'>; editing?: string; inline?: boolean; at?: string } | null>(null)
// A move of the file shown (the first diff coming in among them) drops a form on the diff left; a general note's, or an edit's, stays.
watch(sidesKey, () => {
  selection.value = null
  composer.value = composerOnFileMove(composer.value)
})
// A move of the candidate or the base drops a note being made on the diff left.
watch(notableKey, () => {
  selection.value = null
  if (composer.value?.at) composer.value = null
})
/** The editor's box, whose height keeps the selection's bar inside it. */
const box = useTemplateRef<HTMLElement>('box')
const boxHeight = ref(0)
function onSelect(s: DiffSelection | null) {
  selection.value = s
  boxHeight.value = box.value?.clientHeight ?? 0
  if (s?.open) noteOnSelection()
}
function lineOf(s: SideText | null): string[] {
  return textOf(s).split(/\r\n|\r|\n/)
}
function noteOnSelection() {
  const s = selection.value
  const f = shownFile.value
  if (!s || !f || !notable.value || !ready.value) return
  const path = s.side === 'base' ? (f.from ?? f.path) : f.path
  const at = noteFromSelection(path, s.side, lineOf(s.side === 'base' ? sides.value.base : sides.value.candidate), s.from, s.to)
  composer.value = { note: { ...at, intent: 'change', severity: 'major', message: '' }, inline: true, at: notableKey.value }
}
const modalNote = computed(() => (composer.value && !composer.value.inline ? composer.value : null))
function noteOnFile() {
  const f = shownFile.value
  if (!f || !notable.value) return
  composer.value = {
    note: { kind: 'file', intent: 'change', severity: 'major', path: f.status === 'D' ? (f.from ?? f.path) : f.path, side: f.status === 'D' ? 'base' : 'candidate', message: '' },
    at: notableKey.value,
  }
}
function noteGeneral() {
  composer.value = { note: { kind: 'general', intent: 'change', severity: 'major', message: '' } }
}
function editNote(n: DraftNote) {
  const { key, ...note } = n
  composer.value = { note, editing: key }
}
function saveNote(n: Omit<DraftNote, 'key'>) {
  const c = composer.value
  if (!c) return
  // A note on the diff is kept only while the diff shown is the one it was made on (a move drops the form, notableKey's watch).
  if (!c.editing && c.at !== undefined && c.at !== notableKey.value) {
    composer.value = null
    return
  }
  if (c.editing) drafts.update(c.editing, n)
  else drafts.add(n)
  composer.value = null
  selection.value = null
}
const editor = useTemplateRef<{ show: (side: Side, from: number, to: number) => void }>('editor')
/** A note's lines shown once its own file is ready: cancelled when another file is shown first. */
const shower = useShowWhenReady({ key: () => sidesKey.value, ready: () => ready.value })
function openNote(n: DraftNote) {
  if (!n.path) return
  const f = diff.value?.files.find((x) => x.path === n.path || x.from === n.path)
  if (!f) {
    toast.add({ title: `${n.path} is not in this diff`, description: 'Its note stands: the candidate side is the candidate\'s whatever the base.', icon: 'i-lucide-info', color: 'neutral' })
    return
  }
  file.value = f.path
  if (n.kind === 'line' && n.side && n.from) shower.show(sidesKey.value, () => editor.value?.show(n.side!, n.from!, n.to ?? n.from!))
  else shower.cancel()
}

/**
 * Drops the draft. A stale one's replacement is made on the base of the diff shown, read with none asked (the run's), as knowBase makes
 * a new draft's: the diff stays as it is, and no read of it again would tell it.
 */
function discard() {
  const s = diffs.shown.value
  drafts.discard(s && s.key === chosen.value && s.at === '' ? s.diff.base : '')
}

// The batch Send review sends: the draft's pending send whose answer was lost, as it was sent, else the draft. Its body, what stops it,
// its size as the run keeps it.
const toSend = computed(() => draft.value.pending?.sent ?? draft.value)
const body = computed(() => reviewBody(chosen.value, toSend.value.base, toSend.value.verdict, toSend.value.summary, toSend.value.notes))
const problems = computed(() => {
  const p = batchProblems(body.value, review.by())
  if (!toSend.value.base) p.unshift('The base is not known yet: the diff is being read.')
  return p
})
const bytes = computed(() => (toSend.value.verdict === 'request_changes' ? repairBytes(changeRequestOf(body.value, review.by())) : undefined))
const resend = computed(() => {
  const p = drafts.resend.value
  return p ? { notes: p.sent.notes.length, verdict: p.sent.verdict } : undefined
})

async function send(continueRun: boolean): Promise<ContinuationNeeded | string | null> {
  // The draft's one send (useReviewDraft): refused in words while one is under way; else its pending send again, or the draft as it is.
  // Its answer settles that send alone, by its op id, whichever draft the page shows when it lands.
  const s = drafts.sending()
  if (typeof s === 'string') return s
  const what = reviewBody(chosen.value, s.sent.base, s.sent.verdict, s.sent.summary, s.sent.notes)
  try {
    const reply = await review.submit(props.ticketKey, what, { opId: s.opId, continueRun })
    drafts.sent(s)
    toast.add({ title: reply.receipt.repeat ? 'The review was sent already' : 'Review sent', icon: 'i-lucide-check', color: 'success' })
    void readReviews()
    return null
  } catch (e) {
    // The server answered: nothing was kept under that op id but a refusal's receipt, so the next send is an operation of its own. A
    // reply lost twice keeps the send pending, and the next Send review asks the same operation again, as it was sent.
    if (e instanceof ApiError && e.status === 0) drafts.lost(s)
    else drafts.unsent(s)
    if (e instanceof ContinuationNeeded) return e
    if (e instanceof CommandRefused) return e.message
    return (e as Error).message
  }
}

// The reviews of the candidate already sent: the ticket's, every page, those of the candidate shown.
const sentReviews = useSentReviews(review, () => props.ticketKey)
const sent = sentReviews.list
const sentError = sentReviews.error
const readReviews = sentReviews.read
watch(() => [props.ticketKey, props.reads], () => void readReviews(), { immediate: true })
// A stream's reset dropped every reply on its way: what had not come in is read again (nothing else would ask), and the reviews.
watch(
  () => props.resets,
  () => {
    void diffs.again()
    void fileSides.again()
    void readReviews()
  },
)
const sentHere = computed(() => sent.value.filter((r) => r.candidate === chosen.value))
async function retry(r: ReviewSummary, continueRun: boolean): Promise<ContinuationNeeded | string | null> {
  try {
    await review.retry(r.id, { expectedRev: r.rev, continueRun })
    void readReviews()
    return null
  } catch (e) {
    if (e instanceof ContinuationNeeded) return e
    return (e as Error).message
  }
}
</script>

<template>
  <section class="flex flex-col gap-4" data-review-view :data-review-candidate="chosen" :data-review-diff-base="diff?.base ?? ''">
    <div class="flex flex-wrap items-center gap-2">
      <h2 class="text-base font-semibold text-highlighted">Review</h2>
      <UButton label="Back to the ticket" icon="i-lucide-arrow-left" size="xs" color="neutral" variant="ghost" :to="{ query: {} }" data-review-back />
    </div>
    <p class="text-xs text-muted" data-review-later>
      Not in this view yet: the findings of every review across the ticket's candidates with their marks, the candidate's lineage and checks, and a failed check's repair. The
      ticket's sections show the receipts and the repairs meanwhile.
    </p>

    <p v-if="!options.length && candidates.loading.value" class="text-sm text-dimmed">Reading the ticket's candidates…</p>
    <p v-else-if="!options.length && !cref" class="text-sm text-muted" data-review-none>None of the ticket's runs has a candidate to review yet.</p>
    <template v-else>
      <ReviewCandidate :options="options" :candidate="chosen" :bases="offered" :base="draft.base" :locked="lockWords" @candidate="(v: string) => emit('candidate', v)" @base="drafts.setBase" />
      <UAlert v-if="diffError" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="`The diff could not be read: ${diffError}`" data-review-diff-error />

      <div class="flex flex-col gap-4 lg:flex-row lg:items-start">
        <div class="flex min-w-0 flex-1 flex-col gap-3 lg:flex-row lg:items-start">
          <ReviewFiles v-if="diff" class="lg:w-64 lg:flex-none" :files="diff.files" :selected="file" :notes="notesByPath" :truncated="diff.truncated" @select="(p: string) => (file = p)" />
          <p v-else-if="diffLoading" class="text-xs text-dimmed">Reading the diff…</p>

          <div v-if="shownFile" class="flex min-w-0 flex-1 flex-col gap-1.5" data-review-diff :data-review-diff-path="shownFile.path">
            <div class="flex min-w-0 flex-wrap items-center gap-2 text-xs">
              <span class="min-w-0 break-all font-mono text-highlighted">{{ shownFile.path }}</span>
              <span class="text-dimmed">{{ shortCommit(diff!.base) }} → {{ shortCommit(diff!.candidate) }}</span>
              <UButton label="Note on this file" icon="i-lucide-file-pen-line" size="xs" color="neutral" variant="ghost" class="ml-auto" :disabled="!notable" data-review-note-file @click="noteOnFile" />
            </div>
            <p v-if="sides.error" class="text-xs text-warning">The file could not be read: {{ sides.error }}</p>
            <p v-else-if="binary" class="text-xs text-dimmed" data-review-binary>A binary file, or one past 1 MiB: its lines are not shown. A note on the whole file still may be made.</p>
            <p v-if="truncatedSide" class="text-xs text-warning">The file is past 1 MiB: its first MiB is shown.</p>
            <div v-if="ready && !binary" ref="box" class="relative h-[60vh] min-h-80 overflow-hidden rounded-md border border-default">
              <DiffEditor
                ref="editor"
                :key="sidesKey"
                :path="shownFile.path"
                :original="textOf(sides.base)"
                :modified="textOf(sides.candidate)"
                selectable
                :marks="marks"
                @select="onSelect"
              />
              <div
                v-if="selection && !composer && notable"
                class="absolute z-10 flex items-center gap-0.5 rounded-md border border-default bg-default p-0.5 shadow-md"
                :style="{ top: `${barTop(selection.top, boxHeight)}px`, left: `${Math.max(8, selection.left)}px` }"
                data-review-selection-bar
                :data-review-selection-side="selection.side"
              >
                <UButton label="Add a note" icon="i-lucide-message-square-plus" size="xs" color="neutral" variant="ghost" data-review-add-note @mousedown.prevent @click="noteOnSelection" />
              </div>
            </div>
            <p v-else-if="!binary && !sides.error" class="text-xs text-dimmed">Reading the file…</p>
            <!-- The selection's note form under the editor, outside its clipping box: whole wherever the lines were. -->
            <ReviewNote v-if="composer && composer.inline" class="max-w-3xl" :note="composer.note" :editing="!!composer.editing" @save="saveNote" @cancel="composer = null" />
          </div>
        </div>

        <ReviewDraft
          :key="chosen"
          class="lg:w-96 lg:flex-none"
          :notes="draft.notes"
          :verdict="draft.verdict"
          :summary="draft.summary"
          :problems="problems"
          :bytes="bytes"
          :task="entry?.task ?? cref?.task ?? ''"
          :commit="cref?.commit ?? ''"
          :attempt="entry?.c"
          :stale="staleWords"
          :unsaved="drafts.unsaved.value"
          :flying="drafts.flying.value"
          :resend="resend"
          :send="send"
          @open="openNote"
          @edit="editNote"
          @drop="(n: DraftNote) => drafts.drop(n.key)"
          @general="noteGeneral"
          @verdict="drafts.setVerdict"
          @summary="drafts.setSummary"
          @discard="discard"
        />
      </div>

      <section class="flex flex-col gap-2" data-review-sent-list :data-review-sent-count="sentHere.length">
        <h3 class="text-sm font-semibold text-highlighted">Reviews of this candidate</h3>
        <p v-if="sentError" class="text-xs text-warning" data-review-sent-error>The reviews could not be read: {{ sentError }}</p>
        <p v-if="!sentHere.length && !sentError" class="text-xs text-dimmed">None sent yet.</p>
        <ul v-if="sentHere.length" class="flex flex-col gap-1.5">
          <ReviewApplication v-for="r in sentHere" :key="r.id" :review="r" :task="entry?.task ?? cref?.task ?? ''" :commit="cref?.commit ?? ''" :now="now" :retry="(c: boolean) => retry(r, c)" />
        </ul>
      </section>
    </template>
  </section>

  <UModal :open="!!modalNote" :title="modalNote?.editing ? 'Edit the note' : 'A note'" :ui="{ content: 'max-w-xl' }" @update:open="(o: boolean) => !o && (composer = null)">
    <template #body>
      <ReviewNote v-if="modalNote" :note="modalNote.note" :editing="!!modalNote.editing" @save="saveNote" @cancel="composer = null" />
    </template>
  </UModal>
</template>
