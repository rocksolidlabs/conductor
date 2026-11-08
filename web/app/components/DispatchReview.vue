<script setup lang="ts">
import type { RunCandidate } from '~/composables/useDispatch'
import { ApiError } from '~/composables/useApi'
import { ContinuationNeeded, useCandidateDiff, useReview, type DiffSide, type ReviewSummary } from '~/composables/useReview'
import { useReviewDraft } from '~/composables/useReviewDraft'
import type { useTicketCandidates } from '~/composables/useTicketCandidates'
import { candidateRef, parseCandidate } from '~/utils/dispatch'
import type { DispatchOrder } from '~/utils/dispatchOrder'
import { shortCommit } from '~/utils/dispatchPlans'
import { CommandRefused, mintCommandId } from '~/utils/tasks'
import {
  barTop,
  batchProblems,
  changeRequestOf,
  noteFromSelection,
  offeredBases,
  repairBytes,
  reviewBody,
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
 * and the reviews are read again whenever the page reads the ticket again (its live events name the ticket). Findings across
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

/** The bases the server takes for the candidate, null until the page has read its run's candidates. */
const offered = computed<BaseChoice[] | null>(() => {
  const e = entry.value
  const base = e ? props.candidates.bases.get(e.run) : undefined
  if (!e || !base) return null
  const same = props.candidates.runs.value.find((r) => r.run === e.run)?.tasks.find((t) => t.task === e.task)?.candidates ?? []
  return offeredBases(base, e.c, same)
})

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
type SideText = DiffSide | 'absent' | 'binary'
const sides = ref<{ key: string; base: SideText | null; candidate: SideText | null; error: string }>({ key: '', base: null, candidate: null, error: '' })
const sidesKey = computed(() => (diff.value && shownFile.value ? `${diffKey.value}:${diff.value.base}:${shownFile.value.path}` : ''))
async function readSides() {
  const ck = diffKey.value
  const c = parseCandidate(ck)
  const d = diff.value
  const f = shownFile.value
  const key = sidesKey.value
  if (!c || !d || !f) return
  sides.value = { key, base: null, candidate: null, error: '' }
  if (f.binary) {
    sides.value = { key, base: 'binary', candidate: 'binary', error: '' }
    return
  }
  const one = async (side: Side, path: string, absent: boolean) => {
    if (absent) return void (sides.value = { ...sides.value, [side]: 'absent' })
    try {
      await review.readSide(c, ck, d.base, path, side, (s) => {
        if (sides.value.key === key) sides.value = { ...sides.value, [side]: s }
      })
    } catch (e) {
      if (sides.value.key !== key) return
      if (e instanceof ApiError && e.status === 404) sides.value = { ...sides.value, [side]: 'absent' }
      else if (e instanceof ApiError && e.status === 415) sides.value = { ...sides.value, [side]: 'binary' }
      else sides.value = { ...sides.value, error: (e as Error).message }
    }
  }
  await Promise.all([one('base', f.from ?? f.path, f.status === 'A'), one('candidate', f.path, f.status === 'D')])
}
watch(sidesKey, () => void readSides(), { immediate: true })
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

/** The draft's notes on the file shown, in its margin. */
const marks = computed<DiffMark[]>(() =>
  draft.value.notes.filter((n) => n.kind === 'line' && n.path === file.value && n.side && n.from && n.to).map((n) => ({ side: n.side!, from: n.from!, to: n.to!, label: n.message })),
)
const notesByPath = computed(() => {
  const out: Record<string, number> = {}
  for (const n of draft.value.notes) if (n.path) out[n.path] = (out[n.path] ?? 0) + 1
  return out
})

// A selection: the bar under it, and the note's form.
const selection = ref<DiffSelection | null>(null)
/** A note being made or edited: `at`, for a note on the diff, the candidate and the base it was made on; `inline` for a selection's form. */
const composer = ref<{ note: Omit<DraftNote, 'key'>; editing?: string; inline?: boolean; at?: string } | null>(null)
watch(sidesKey, () => {
  selection.value = null
  composer.value = null
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
function openNote(n: DraftNote) {
  if (!n.path) return
  const f = diff.value?.files.find((x) => x.path === n.path || x.from === n.path)
  if (!f) {
    toast.add({ title: `${n.path} is not in this diff`, description: 'Its note stands: the candidate side is the candidate\'s whatever the base.', icon: 'i-lucide-info', color: 'neutral' })
    return
  }
  file.value = f.path
  if (n.kind === 'line' && n.side && n.from) {
    const show = () => editor.value?.show(n.side!, n.from!, n.to ?? n.from!)
    if (ready.value && sidesKey.value.endsWith(`:${f.path}`)) show()
    else {
      const stop = watch(ready, (r) => {
        if (!r) return
        stop()
        void nextTick(show)
      })
    }
  }
}

/**
 * Drops the draft. A stale one's replacement is made on the base of the diff shown, read with none asked (the run's), as knowBase makes
 * a new draft's: the diff stays as it is, and no read of it again would tell it.
 */
function discard() {
  const s = diffs.shown.value
  drafts.discard(s && s.key === chosen.value && s.at === '' ? s.diff.base : '')
}

// The batch: its body, what stops it, its size as the run keeps it.
const body = computed(() => reviewBody(chosen.value, draft.value.base, draft.value.verdict, draft.value.summary, draft.value.notes))
const problems = computed(() => {
  const p = batchProblems(body.value, review.by())
  if (!draft.value.base) p.unshift('The base is not known yet: the diff is being read.')
  return p
})
const bytes = computed(() => (draft.value.verdict === 'request_changes' ? repairBytes(changeRequestOf(body.value, review.by())) : undefined))

async function send(continueRun: boolean): Promise<ContinuationNeeded | string | null> {
  // The draft sent, as it is now: its answer settles that draft, whichever the page shows when it lands.
  const what = body.value
  const s = drafts.sending(draft.value.opId || mintCommandId())
  try {
    const reply = await review.submit(props.ticketKey, what, { opId: s.opId, continueRun })
    drafts.sent(s)
    toast.add({ title: reply.receipt.repeat ? 'The review was sent already' : 'Review sent', icon: 'i-lucide-check', color: 'success' })
    void readReviews()
    return null
  } catch (e) {
    // The server answered: nothing was kept under that op id but a refusal's receipt, so the next send is an operation of its own. A
    // reply lost twice keeps the id, and the next send asks the same operation again.
    if (!(e instanceof ApiError && e.status === 0)) drafts.unsent(s)
    if (e instanceof ContinuationNeeded) return e
    if (e instanceof CommandRefused) return e.message
    return (e as Error).message
  }
}

// The reviews of the candidate already sent: the ticket's, every page, those of the candidate shown.
const sent = ref<ReviewSummary[]>([])
const sentError = ref('')
async function readReviews() {
  const key = props.ticketKey
  try {
    await review.readReviews(key, (rs) => {
      if (key !== props.ticketKey) return
      sent.value = rs
      sentError.value = ''
    })
  } catch (e) {
    sentError.value = (e as Error).message
  }
}
watch(() => [props.ticketKey, props.reads], () => void readReviews(), { immediate: true })
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
          :stale="staleWords"
          :unsaved="drafts.unsaved.value"
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
        <p v-if="sentError" class="text-xs text-warning">The reviews could not be read: {{ sentError }}</p>
        <p v-else-if="!sentHere.length" class="text-xs text-dimmed">None sent yet.</p>
        <ul v-else class="flex flex-col gap-1.5">
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
