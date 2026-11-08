<script setup lang="ts">
import type { CandidateReceipt, LaunchSummary, PlanBody, PlanSummary, RunCandidate } from '~/composables/useDispatch'
import { ApiError } from '~/composables/useApi'
import { ContinuationNeeded, useCandidateDiff, useFileSides, useReview, useSentReviews, useShowWhenReady, type ReviewFinding, type ReviewSummary, type SideText } from '~/composables/useReview'
import { useReviewDraft } from '~/composables/useReviewDraft'
import type { useTicketCandidates } from '~/composables/useTicketCandidates'
import { candidateRef, parseCandidate } from '~/utils/dispatch'
import type { DispatchOrder } from '~/utils/dispatchOrder'
import { shortCommit } from '~/utils/dispatchPlans'
import { CommandRefused } from '~/utils/tasks'
import { receiptChips, receiptFailed, receiptKey, receiptName } from '~/utils/dispatchEvidence'
import { lineageOf, type LineageAttempt, type LineageReviewTask } from '~/utils/reviewLineage'
import {
  latest,
  recheckWords,
  type PreparedRepair,
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
 * drops every reply on its way, reads again whatever had not come in. Beside them (PR-7): the ticket's acceptance criteria as the run
 * was launched with them and as they are now, the plan revision that launched it and the task's expected checks; how the task's
 * candidates came to be (ReviewLineage); the candidate's checks, a failed one's Prepare a repair (ReviewCheckFailure: its receipt, its
 * class, Check again when Yardmaster takes it, a check note added to the draft); every finding of the task's reviews across its
 * candidates, located on the one shown, with the agent's addressed notes and the marks a person makes (ReviewFindings); and the ticket's
 * decisions and delivery (the page's own sections, in the `decisions` slot). One column on a phone, where a note on lines is made by
 * typing them (Note on lines), since Monaco's selection is a desktop gesture.
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
  /** The ticket's acceptance criteria now. */
  acceptance?: string
  /** The launches the page knows, the plan revisions and the bodies read: what launched the candidate's run. */
  launches?: readonly LaunchSummary[]
  plans?: readonly PlanSummary[]
  bodies?: ReadonlyMap<string, PlanBody>
  /** By run: the run it continues (the ticket's Runs entries). */
  continues?: ReadonlyMap<string, string>
  /** A failed receipt of the candidate to prepare a repair from at once, `<id>/<k>` (a link's: the ticket's receipt, the run page). */
  repair?: string
  /** A section to scroll to at once: `findings` (the palette's Open findings). */
  focus?: string
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
// The task's findings across its candidates, located on the one shown: read when it moves and whenever the ticket is read again.
const findings = ref<ReviewFinding[]>([])
const findingsError = ref('')
const findingsLoading = ref(false)
const findingAsks = latest()
let viewClosed = false
onScopeDispose(() => (viewClosed = true))
async function readFindings(): Promise<void> {
  const key = props.ticketKey
  const at = chosen.value
  if (viewClosed || !at || !cref.value) return
  const current = findingAsks.ask()
  findingsLoading.value = true
  try {
    await review.readFindings(key, at, (fs) => {
      if (!current() || key !== props.ticketKey || at !== chosen.value) return
      findings.value = fs
      findingsError.value = ''
    })
  } catch (e) {
    if (current()) findingsError.value = (e as Error).message
  } finally {
    if (current()) findingsLoading.value = false
  }
}
watch(chosen, () => (findings.value = []))
watch(() => [props.ticketKey, chosen.value, props.reads, props.resets], () => void readFindings(), { immediate: true })
async function markFinding(f: ReviewFinding, verdict: 'resolved' | 'open' | 'withdrawn', note: string): Promise<string> {
  try {
    await review.mark(f.id, chosen.value, verdict, note)
    void readFindings()
    return ''
  } catch (e) {
    return (e as Error).message || 'The mark was not made.'
  }
}
function openFinding(f: ReviewFinding) {
  if (f.kind !== 'line' && f.kind !== 'file') return
  const path = f.locatedOn === chosen.value && f.located?.state === 'renamed' ? f.located.path : f.path
  const from = f.locatedOn === chosen.value && f.located?.state === 'moved' ? f.located.from : f.from
  const to = f.locatedOn === chosen.value && f.located?.state === 'moved' ? f.located.to : f.to
  openNote({ key: f.id, kind: f.kind, intent: f.intent, side: f.side, path, from, to, message: f.message })
}

// The ticket's criteria as the run was launched with them and now, the plan revision that launched it, the task's expected checks.
const launch = computed<LaunchSummary | null>(() => {
  const run = cref.value?.run
  if (!run) return null
  for (let r: string | undefined = run, n = 0; r && n < 64; r = props.continues?.get(r), n++) {
    const l = props.launches?.find((x) => x.runId === r)
    if (l) return l
  }
  return null
})
const launchPlan = computed(() => (launch.value ? (props.plans?.find((p) => p.id === launch.value!.planId) ?? null) : null))
/** The ticket's acceptance criteria as the launch's package froze them (a launch never changes), read once per launch. */
const dispatchApi = useDispatchApi()
const launchedCriteria = ref<{ id: string; text: string } | null>(null)
watch(
  () => launch.value?.id,
  (id) => {
    if (!id || launchedCriteria.value?.id === id) return
    dispatchApi
      .launchRead(id)
      .then((l) => {
        const pkg = l.package as { ticket?: { acceptance?: string } } | null
        if (launch.value?.id === id) launchedCriteria.value = { id, text: pkg?.ticket?.acceptance ?? '' }
      })
      .catch(() => {})
  },
  { immediate: true },
)
const criteriaThen = computed(() => (launchedCriteria.value && launchedCriteria.value.id === launch.value?.id ? launchedCriteria.value.text : null))
const expectedChecks = computed(() => {
  const l = launch.value
  const task = cref.value?.task
  if (!l || !task) return null
  return props.bodies?.get(l.planId)?.crew.tasks?.find((t) => t.id === task)?.checks ?? null
})

// How the task's candidates came to be, oldest first.
const lineage = computed(() => {
  const task = entry.value?.task ?? cref.value?.task
  if (!task) return []
  const attempts: LineageAttempt[] = []
  const reviewTasks: LineageReviewTask[] = []
  for (const r of props.candidates.runs.value) {
    for (const t of r.tasks) {
      for (const c of t.candidates) {
        if (t.kind === 'review') reviewTasks.push({ run: r.run, task: t.task, attempt: c.attempt, reviewed: c.start, verdict: c.verdict })
        else if (t.task === task && c.commit) {
          const bad = c.receipts.find((x) => receiptFailed(x))
          attempts.push({ run: r.run, task, attempt: c.attempt, start: c.start, commit: c.commit, state: c.state, ...(bad ? { failed: receiptName(bad) } : {}) })
        }
      }
    }
  }
  const input = { attempts, reviews: sent.value, reviewTasks, continues: props.continues ?? new Map<string, string>() }
  return [...attempts].sort((a, b) => a.attempt - b.attempt).map((a) => lineageOf({ run: a.run, task, attempt: a.attempt, commit: a.commit!, start: a.start }, input))
})

// The candidate's checks, and a failed one's repair prepared.
const receipts = computed<CandidateReceipt[]>(() => entry.value?.c.receipts ?? [])
/** The task failed: its candidate shown failed and no later one of it is listed. */
const taskFailed = computed(() => {
  const e = entry.value
  return !!e && e.c.state === 'failed' && !options.value.some((o) => o.task === e.task && o.c.attempt > e.c.attempt)
})
const repairOpen = ref(false)
const prepared = ref<PreparedRepair | null>(null)
const prepareError = ref('')
const preparing = ref(false)
const prepareAsks = latest()
async function prepare(id: string, k: number) {
  const c = cref.value
  if (!c) return
  const current = prepareAsks.ask()
  repairOpen.value = true
  prepared.value = null
  prepareError.value = ''
  preparing.value = true
  try {
    const p = await review.prepare(c, id, k)
    if (current()) prepared.value = p
  } catch (e) {
    if (current()) prepareError.value = (e as Error).message
  } finally {
    if (current()) preparing.value = false
  }
}
async function recheck(): Promise<string> {
  const c = cref.value
  const p = prepared.value
  if (!c || !p) throw new Error('Nothing to check again.')
  const reply = await review.recheck(c, p.recheck.revision)
  return recheckWords(c, { run: reply.run || p.recheck.run, attempt: reply.attempt })
}
function addCheckNote(n: Omit<DraftNote, 'key'>) {
  drafts.add(n)
}
// A link's receipt (`&repair=<id>/<k>`) prepared once its candidate is shown, once for each link.
let preparedFor = ''
watch(
  () => [props.repair, cref.value] as const,
  ([want, c]) => {
    const m = /^([a-z0-9][a-z0-9_-]{0,39})\/([1-9][0-9]*)$/.exec(want ?? '')
    if (!m || !c || preparedFor === `${chosen.value}|${want}`) return
    preparedFor = `${chosen.value}|${want}`
    void prepare(m[1]!, Number(m[2]))
  },
  { immediate: true },
)

// A note on lines typed (a phone's way, Monaco's selection being a desktop gesture): the side and the lines of the file shown.
const typed = ref<{ side: Side; from: number; to: number } | null>(null)
function noteOnTyped() {
  const t = typed.value
  const f = shownFile.value
  if (!t || !f || !notable.value || !ready.value) return
  const path = t.side === 'base' ? (f.from ?? f.path) : f.path
  const at = noteFromSelection(path, t.side, lineOf(t.side === 'base' ? sides.value.base : sides.value.candidate), t.from, t.to)
  composer.value = { note: { ...at, intent: 'change', severity: 'major', message: '' }, inline: true, at: notableKey.value }
  typed.value = null
}
watch(sidesKey, () => (typed.value = null))

// The palette's rows: Send review (n notes) asks as the button does; Open findings scrolls to them.
const draftPanel = useTemplateRef<{ ask: () => void }>('draftPanel')
const findingsSection = useTemplateRef<HTMLElement>('findingsSection')
function showFindings() {
  void nextTick(() => findingsSection.value?.scrollIntoView({ block: 'start' }))
}
watch(
  () => props.focus,
  (f) => {
    if (f === 'findings') showFindings()
  },
  { immediate: true },
)
defineExpose({ notes: computed(() => draft.value.notes.length), send: () => draftPanel.value?.ask(), showFindings })

</script>

<template>
  <section class="flex flex-col gap-4" data-review-view :data-review-candidate="chosen" :data-review-diff-base="diff?.base ?? ''">
    <div class="flex flex-wrap items-center gap-2">
      <h2 class="text-base font-semibold text-highlighted">Review</h2>
      <UButton label="Back to the ticket" icon="i-lucide-arrow-left" size="xs" color="neutral" variant="ghost" :to="{ query: {} }" data-review-back />
    </div>
    <!-- The requirements and the plan: the ticket's criteria as launched and now, the revision that launched the run, the task's checks. -->
    <section class="flex flex-col gap-1.5 rounded-lg p-3 text-xs ring ring-default" data-review-requirements>
      <h3 class="text-sm font-semibold text-highlighted">Requirements and plan</h3>
      <template v-if="launch">
        <p class="text-muted" data-review-plan :data-review-plan-n="launch.planN">
          Launched from plan r{{ launch.planN }}<template v-if="launchPlan"> · {{ launchPlan.crewName }} · {{ launchPlan.state }}<template v-if="launchPlan.approvedBy"> by {{ launchPlan.approvedBy }}</template></template>, at the ticket's revision {{ launch.ticketRev }}.
        </p>
        <span class="font-medium text-muted">The acceptance criteria as launched</span>
        <pre v-if="criteriaThen !== null" class="max-h-40 overflow-auto whitespace-pre-wrap break-words rounded bg-elevated/40 p-2 font-sans" data-review-criteria-launched>{{ criteriaThen || '(none)' }}</pre>
        <span v-else class="text-dimmed">Reading the launch's package…</span>
        <template v-if="criteriaThen !== null && (acceptance ?? '') !== criteriaThen">
          <span class="font-medium text-warning" data-review-criteria-changed>Changed since the launch: now</span>
          <pre class="max-h-40 overflow-auto whitespace-pre-wrap break-words rounded bg-elevated/40 p-2 font-sans" data-review-criteria-now>{{ acceptance || '(none)' }}</pre>
        </template>
      </template>
      <template v-else>
        <p class="text-dimmed">This run was not launched from a plan revision of the ticket (it was linked by hand): its criteria are the ticket's now.</p>
        <pre class="max-h-40 overflow-auto whitespace-pre-wrap break-words rounded bg-elevated/40 p-2 font-sans" data-review-criteria-now>{{ acceptance || '(none)' }}</pre>
      </template>
      <template v-if="expectedChecks">
        <span class="font-medium text-muted">{{ cref?.task }}'s expected checks</span>
        <ul class="flex flex-col gap-0.5" data-review-expected-checks>
          <li v-for="c in expectedChecks" :key="c.id"><span class="font-mono">{{ c.id }}</span>: <code class="font-mono text-dimmed">{{ c.argv.join(' ') }}</code></li>
          <li v-if="!expectedChecks.length" class="text-dimmed">None: the task names no check.</li>
        </ul>
      </template>
    </section>

    <p v-if="!options.length && candidates.loading.value" class="text-sm text-dimmed">Reading the ticket's candidates…</p>
    <p v-else-if="!options.length && !cref" class="text-sm text-muted" data-review-none>None of the ticket's runs has a candidate to review yet.</p>
    <template v-else>
      <ReviewCandidate :options="options" :candidate="chosen" :bases="offered" :base="draft.base" :locked="lockWords" :lineage="lineage" @candidate="(v: string) => emit('candidate', v)" @base="drafts.setBase" />
      <UAlert v-if="diffError" color="warning" variant="subtle" icon="i-lucide-triangle-alert" :title="`The diff could not be read: ${diffError}`" data-review-diff-error />

      <div class="flex flex-col gap-4 lg:flex-row lg:items-start">
        <div class="flex min-w-0 flex-1 flex-col gap-3 lg:flex-row lg:items-start">
          <ReviewFiles v-if="diff" class="lg:w-64 lg:flex-none" :files="diff.files" :selected="file" :notes="notesByPath" :truncated="diff.truncated" @select="(p: string) => (file = p)" />
          <p v-else-if="diffLoading" class="text-xs text-dimmed">Reading the diff…</p>

          <div v-if="shownFile" class="flex min-w-0 flex-1 flex-col gap-1.5" data-review-diff :data-review-diff-path="shownFile.path">
            <div class="flex min-w-0 flex-wrap items-center gap-2 text-xs">
              <span class="min-w-0 break-all font-mono text-highlighted">{{ shownFile.path }}</span>
              <span class="text-dimmed">{{ shortCommit(diff!.base) }} → {{ shortCommit(diff!.candidate) }}</span>
              <UButton label="Note on lines" icon="i-lucide-list-ordered" size="xs" color="neutral" variant="ghost" class="ml-auto" :disabled="!notable || !ready || binary" data-review-note-lines @click="typed = { side: 'candidate', from: 1, to: 1 }" />
              <UButton label="Note on this file" icon="i-lucide-file-pen-line" size="xs" color="neutral" variant="ghost" :disabled="!notable" data-review-note-file @click="noteOnFile" />
            </div>
            <div v-if="typed" class="flex flex-wrap items-end gap-2 rounded-md p-2 text-xs ring ring-default" data-review-typed>
              <UFormField label="Side" size="xs">
                <USelect v-model="typed.side" :items="[{ label: 'candidate', value: 'candidate' }, { label: 'base', value: 'base' }]" size="xs" class="w-28" data-review-typed-side />
              </UFormField>
              <UFormField label="From line" size="xs">
                <UInput v-model.number="typed.from" type="number" min="1" size="xs" class="w-20" data-review-typed-from />
              </UFormField>
              <UFormField label="To line" size="xs">
                <UInput v-model.number="typed.to" type="number" min="1" size="xs" class="w-20" data-review-typed-to />
              </UFormField>
              <UButton label="Add a note" size="xs" :disabled="!typed.from || !typed.to" data-review-typed-add @click="noteOnTyped" />
              <UButton label="Cancel" size="xs" color="neutral" variant="ghost" @click="typed = null" />
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
          ref="draftPanel"
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

      <!-- The candidate's checks: its own receipts; a failed one's repair prepared. -->
      <section class="flex flex-col gap-2" data-review-checks :data-review-checks-count="receipts.length">
        <h3 class="text-sm font-semibold text-highlighted">Checks of this candidate</h3>
        <p v-if="!receipts.length" class="text-xs text-dimmed">No check ran against this commit.</p>
        <ul v-else class="flex flex-wrap gap-1.5">
          <li v-for="r in receipts" :key="receiptKey(r)" class="flex items-center gap-1 rounded border border-default/60 px-1.5 py-0.5 text-xs" data-review-check :data-review-check-key="receiptKey(r)" :data-review-check-failed="receiptFailed(r) ? 'true' : 'false'">
            <span class="font-mono">{{ receiptName(r) }}</span>
            <UBadge v-for="x in receiptChips(r)" :key="x.label" :label="x.label" :color="x.color" variant="subtle" size="sm" />
            <span class="text-dimmed">{{ r.where }}</span>
            <UButton v-if="receiptFailed(r)" label="Prepare a repair" icon="i-lucide-wrench" size="xs" color="neutral" variant="outline" :data-review-prepare="`${r.id}/${r.k}`" @click="prepare(r.id, r.k)" />
          </li>
        </ul>
      </section>

      <section ref="findingsSection" class="flex flex-col gap-2">
        <ReviewFindings :findings="findings" :candidate="chosen" :error="findingsError" :loading="findingsLoading" :now="now" :mark="markFinding" @open="openFinding" />
      </section>

      <section class="flex flex-col gap-2" data-review-sent-list :data-review-sent-count="sentHere.length">
        <h3 class="text-sm font-semibold text-highlighted">Reviews of this candidate</h3>
        <p v-if="sentError" class="text-xs text-warning" data-review-sent-error>The reviews could not be read: {{ sentError }}</p>
        <p v-if="!sentHere.length && !sentError" class="text-xs text-dimmed">None sent yet.</p>
        <ul v-if="sentHere.length" class="flex flex-col gap-1.5">
          <ReviewApplication v-for="r in sentHere" :key="r.id" :review="r" :task="entry?.task ?? cref?.task ?? ''" :commit="cref?.commit ?? ''" :now="now" :retry="(c: boolean) => retry(r, c)" />
        </ul>
      </section>
    </template>

    <!-- The ticket's decisions and delivery: the page's own sections. -->
    <div class="flex flex-col gap-4" data-review-decisions>
      <slot name="decisions" />
    </div>
  </section>

  <ReviewCheckFailure v-model:open="repairOpen" :prepared="prepared" :error="prepareError" :loading="preparing" :task-failed="taskFailed" :recheck="recheck" @add="addCheckNote" />

  <UModal :open="!!modalNote" :title="modalNote?.editing ? 'Edit the note' : 'A note'" :ui="{ content: 'max-w-xl' }" @update:open="(o: boolean) => !o && (composer = null)">
    <template #body>
      <ReviewNote v-if="modalNote" :note="modalNote.note" :editing="!!modalNote.editing" @save="saveNote" @cancel="composer = null" />
    </template>
  </UModal>
</template>
