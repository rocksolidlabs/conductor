import { computed, getCurrentScope, nextTick, onScopeDispose, watch, type InjectionKey } from 'vue'
import { ApiError } from './useApi'
import type { DispatchReceipt, OpBody, RunCandidate } from './useDispatch'
import { through, type DispatchOrder } from '~/utils/dispatchOrder'
import { mintCommandId, sendCommand } from '~/utils/tasks'
import { allPages, candidateRef, parseCandidate, type CandidateRef } from '~/utils/dispatch'
import { latest, recheckWords, type CheckRef, type Located, type NewFinding, type PreparedRepair, type ReviewApplication, type ReviewBody, type Side } from '~/utils/review'

/**
 * A person's reviews of a ticket's candidates (Program item RV1, docs/plans/review-repair.md, "web/app"): a candidate's diff and its
 * files' sides, the prepared failed-check request and Check again, a review sent and sent again, a ticket's reviews and findings, a
 * finding's marks. Every read goes through the page's ordering gate (utils/dispatchOrder.ts), so a slow reply never replaces a newer
 * one; every write mints its op id (or a command id) and asks once more with the same id when its reply is lost (utils/tasks.ts,
 * sendCommand), which the server answers from its receipt. A request the server would apply by continuing a run it does not run is
 * answered `continuation_needed`, thrown as ContinuationNeeded for the page to ask the person first.
 */

/** A file that differs between the base and the candidate. */
export interface DiffFile {
  path: string
  /** A rename's old path. */
  from?: string
  status: 'A' | 'M' | 'D' | 'R'
  added: number
  removed: number
  /** git calls it binary, or it is past 1 MiB: listed without its lines. */
  binary: boolean
}

/** GET /api/runs/{run}/tasks/{task}/attempts/{n}/diff: the candidate's commit, the base read against (the run's when none was asked). */
export interface CandidateDiff {
  candidate: string
  base: string
  files: DiffFile[]
  truncated?: boolean
}

/** One side of a file of the diff, as text: cut at 1 MiB (`truncated`), `commit` the one read. */
export interface DiffSide {
  text: string
  truncated: boolean
  commit: string
}

/** A finding of a review, as the store keeps it with its latest mark. */
export interface ReviewFinding extends NewFinding {
  id: string
  reviewId: string
  ticketId: string
  k: number
  anchorCommit?: string
  lines: string[]
  message: string
  /** A check finding's receipt, as the server read it from the ledger. */
  check?: CheckRef
  createdAt: string
  candidate: string
  state: 'open' | 'resolved' | 'withdrawn'
  marks: number
  latest?: FindingMark
  /** On a ticket's findings: the candidate it was located on, and where its lines are there. */
  locatedOn?: string
  located?: Located
  addressed?: Array<{ run: string; attempt: number; note?: string }>
}

export interface FindingMark {
  id: string
  findingId: string
  candidate: string
  verdict: 'resolved' | 'open' | 'withdrawn'
  located?: Located
  note?: string
  createdAt: string
  createdBy: string
  actor: string
  via: string
}

/** A review without its findings, counted by state. */
export interface ReviewSummary {
  id: string
  ticketId: string
  ticketKey: string
  candidate: string
  base: string
  verdict: 'request_changes' | 'comment' | 'approve'
  summary: string
  createdAt: string
  createdBy: string
  actor: string
  via: string
  application: ReviewApplication
  rev: number
  counts: { open: number; resolved: number; withdrawn: number }
}

export interface Review extends ReviewSummary {
  findings: ReviewFinding[]
}

export interface ReviewReply {
  receipt: DispatchReceipt
  /** Null for a review kept whose read after its commit failed: answered by its receipt, its request left to the server's worker. */
  review: Review | null
}

/** How a review is applied, beside what is recorded: the run's revision expected, and the word to continue a run the server does not run. */
export interface ApplyOptions {
  expectedRevision?: number
  continueRun?: boolean
  /** The op id minted before the send, kept with the draft so a resend is the same operation. */
  opId?: string
}

/** A request the server would apply only by continuing a run it does not run: the run, and what continuing it starts, in words. */
export class ContinuationNeeded extends Error {
  constructor(
    public run: string,
    public words: string,
  ) {
    super(words)
  }
}

/** The failure of a write as the page shows it: a ContinuationNeeded for the server's continuation_needed, else as it came. */
function continuation(e: unknown): unknown {
  if (e instanceof ApiError && e.status === 409 && e.code === 'continuation_needed') {
    const b = (e.body ?? {}) as { run?: string; words?: string }
    return new ContinuationNeeded(b.run ?? '', b.words || e.message)
  }
  return e
}

const enc = encodeURIComponent
const attemptPath = (c: Pick<CandidateRef, 'run' | 'task' | 'attempt'>) => `/api/runs/${enc(c.run)}/tasks/${enc(c.task)}/attempts/${c.attempt}`

/** The routes, as calls. */
export function useReviewApi() {
  const { request, requestText } = useApi()
  return {
    candidateDiff: (c: CandidateRef, base?: string) => request<CandidateDiff>(`${attemptPath(c)}/diff` + (base ? `?base=${enc(base)}` : '')),
    diffFile: async (c: CandidateRef, path: string, side: Side, base?: string): Promise<DiffSide> => {
      const r = await requestText(`${attemptPath(c)}/diff/file?path=${enc(path)}&side=${side}` + (base ? `&base=${enc(base)}` : ''))
      return { text: r.text, truncated: r.headers.get('X-Conductor-Truncated') === '1', commit: r.headers.get('X-Conductor-Commit') ?? '' }
    },
    prepareRepair: (c: Pick<CandidateRef, 'run' | 'task' | 'attempt'>, step: string, k: number) => request<PreparedRepair>(`${attemptPath(c)}/steps/${enc(step)}/${k}/repair`),
    recheck: (run: string, task: string, body: { commandId: string; attempt: number; commit: string; expectedRevision?: number }) =>
      request<{ receipt: DispatchReceipt; run: string; attempt: number }>(`/api/runs/${enc(run)}/tasks/${enc(task)}/recheck`, { method: 'POST', body }),
    submitReview: (key: string, body: OpBody & ReviewBody & Omit<ApplyOptions, 'opId'>) => request<ReviewReply>(`/api/dispatch/tickets/${enc(key)}/reviews`, { method: 'POST', body }),
    retryReview: (id: string, body: OpBody & Omit<ApplyOptions, 'opId'>) => request<ReviewReply>(`/api/dispatch/reviews/${enc(id)}/retry`, { method: 'POST', body }),
    ticketReviews: (key: string, before = '') => request<{ reviews: ReviewSummary[]; next?: string }>(`/api/dispatch/tickets/${enc(key)}/reviews` + (before ? `?before=${enc(before)}` : '')),
    review: (id: string) => request<{ review: Review }>(`/api/dispatch/reviews/${enc(id)}`).then((r) => r.review),
    findings: (key: string, q: { state?: 'open' | 'resolved' | 'withdrawn' | 'all'; candidate?: string; after?: string } = {}) => {
      const qs = Object.entries(q)
        .filter(([, v]) => v)
        .map(([k, v]) => `${k}=${enc(v!)}`)
        .join('&')
      return request<{ findings: ReviewFinding[]; next?: string }>(`/api/dispatch/tickets/${enc(key)}/findings` + (qs ? `?${qs}` : ''))
    },
    findingMarks: (id: string, after = '') => request<{ marks: FindingMark[]; next?: string }>(`/api/dispatch/findings/${enc(id)}/marks` + (after ? `?after=${enc(after)}` : '')),
    markFinding: (id: string, body: OpBody & { candidate: string; verdict: FindingMark['verdict']; note?: string; via: 'web' }) =>
      request<{ receipt: DispatchReceipt; mark: FindingMark }>(`/api/dispatch/findings/${enc(id)}/marks`, { method: 'POST', body }),
  }
}

/** The most reviews a ticket holds (the store's dispatch.MaxReviews): a whole read of them stops there, 25 pages of 20. */
export const MAX_REVIEWS = 500
/** The most findings a ticket holds: fifty a review (dispatch.MaxReviewFindings), MAX_REVIEWS reviews. */
export const MAX_FINDINGS = 50 * MAX_REVIEWS

/** The open findings of a run's task among a ticket's reviews: each review counts its own by state (its findings' latest marks), as the store does. */
export function openFindingsOf(reviews: readonly ReviewSummary[], run: string, task: string): number {
  let n = 0
  for (const r of reviews) {
    const c = parseCandidate(r.candidate)
    if (c?.run === run && c.task === task) n += r.counts?.open ?? 0
  }
  return n
}

/** The open findings of a task among a ticket's reviews, every run's: the findings the Review view lists for a candidate of that task. */
export function openFindingsOfTask(reviews: readonly ReviewSummary[], task: string): number {
  let n = 0
  for (const r of reviews) if (parseCandidate(r.candidate)?.task === task) n += r.counts?.open ?? 0
  return n
}

/**
 * What the ticket page gives the candidates it lists (DispatchCandidates): the open findings of a run's task, the Review view's link (at
 * a failed receipt to prepare a repair from, `<id>/<k>`), and the ticket's reviews, whose applied requests are a person's repairs.
 */
export interface ReviewEntry {
  openFindings: (run: string, task: string) => number
  href: (candidate: string, repair?: string) => { query: Record<string, string> }
  reviews: () => readonly ReviewSummary[]
}
export const REVIEW_ENTRY: InjectionKey<ReviewEntry> = Symbol('review-entry')

/** The page's reads and writes of reviews, through its ordering gate. */
export function useReview(gate: DispatchOrder) {
  const api = useReviewApi()
  const identity = useIdentity()

  /** The op's members every write carries: its id (minted unless given) and the display name. */
  function op(opId?: string): OpBody {
    const body: OpBody = { opId: opId ?? mintCommandId() }
    const name = identity.name.value.trim()
    if (name) body.byName = name
    return body
  }

  /** The thing of the gate a candidate's diff at a base is. */
  const diffThing = (candidate: string, base: string) => `review-diff:${candidate}:${base}`

  return {
    api,
    /** The display name the server records the request by, or the actor's when none is set: what a batch is measured with. */
    by: () => identity.name.value.trim() || 'admin',
    /** The candidate's diff against the base ('' for the run's); applied when the gate takes it. */
    readDiff: (c: CandidateRef, candidate: string, base: string, apply: (d: CandidateDiff) => void) => through(gate, diffThing(candidate, base), () => api.candidateDiff(c, base || undefined), apply),
    /** One side of a file of the diff; applied when the gate takes it. */
    readSide: (c: CandidateRef, candidate: string, base: string, path: string, side: Side, apply: (s: DiffSide) => void) =>
      through(gate, `review-file:${candidate}:${base}:${side}:${path}`, () => api.diffFile(c, path, side, base || undefined), apply),
    /** The ticket's reviews, newest first, every page (at most MAX_REVIEWS, the store's bound), applied together when the gate takes the whole read. */
    readReviews: (key: string, apply: (r: ReviewSummary[]) => void) =>
      through(gate, `reviews:${key}`, () => allPages((before) => api.ticketReviews(key, before).then((p) => ({ items: p.reviews ?? [], next: p.next })), MAX_REVIEWS), apply),
    /**
     * The ticket's findings of the candidate's task, every page, each located on that candidate (the server's `located`, against
     * `candidate`), with its state, its last mark and what the repair attempts said they addressed; applied when the gate takes the read.
     */
    readFindings: (key: string, candidate: string, apply: (f: ReviewFinding[]) => void) =>
      through(gate, `findings:${key}:${candidate}`, () => allPages((after) => api.findings(key, { state: 'all', candidate, after }).then((p) => ({ items: p.findings ?? [], next: p.next })), MAX_FINDINGS), apply),
    /** A failed receipt's prepared repair request (read whole, never cut). */
    prepare: (c: Pick<CandidateRef, 'run' | 'task' | 'attempt'>, step: string, k: number) => api.prepareRepair(c, step, k),
    /**
     * A finding marked against a candidate: its op id minted, asked once more with it on a lost reply; a refusal is a CommandRefused in
     * the store's words, a 409 invalid (not a later candidate, Resolved on the finding's own commit) an ApiError in the server's.
     */
    mark: (id: string, candidate: string, verdict: FindingMark['verdict'], note: string) => {
      const n = note.trim()
      return sendCommand((b: OpBody & { candidate: string; verdict: FindingMark['verdict']; note?: string; via: 'web' }) => api.markFinding(id, b), { ...op(), candidate, verdict, via: 'web' as const, ...(n ? { note: n } : {}) })
    },
    /**
     * Check again: the failed candidate named in its own run (`c`), sent to `run`, the run the prepared request says it goes to, at its
     * revision; a command id minted, asked once more with it on a lost reply. The reply names the run and the attempt it was checked in.
     */
    recheck: (c: CandidateRef, revision: number | undefined) =>
      sendCommand((b: { commandId: string; attempt: number; commit: string; expectedRevision?: number }) => api.recheck(c.run, c.task, b), {
        commandId: mintCommandId(),
        attempt: c.attempt,
        commit: c.commit,
        ...(revision ? { expectedRevision: revision } : {}),
      }),
    /** A review whole, ordered by its revision. */
    readReview: (id: string, apply: (r: Review) => void) => through(gate, `review:${id}`, () => api.review(id), apply, (r) => r.rev),

    /**
     * Sends a review; its reply's review through the gate as `review:<id>`, applied by `apply`. A lost reply is asked once more with
     * the same op id; a refusal with its receipt is a CommandRefused; a continuation_needed is a ContinuationNeeded; anything else
     * (413 too_large among them: nothing kept) as it came.
     */
    async submit(key: string, body: ReviewBody, o: ApplyOptions, apply?: (r: Review) => void): Promise<ReviewReply> {
      const n = gate.ask()
      const { opId, ...how } = o
      try {
        const reply = await sendCommand<OpBody & ReviewBody & Omit<ApplyOptions, 'opId'>, ReviewReply>((b) => api.submitReview(key, b), { ...op(opId), ...body, ...how })
        if (reply.review && gate.take(`review:${reply.review.id}`, n, reply.review.rev)) apply?.(reply.review)
        return reply
      } catch (e) {
        throw continuation(e)
      }
    },

    /** Request again: a refused request applied as its next try, the same rules as `submit`. */
    async retry(id: string, o: ApplyOptions & { expectedRev?: number }, apply?: (r: Review) => void): Promise<ReviewReply> {
      const n = gate.ask()
      const { opId, expectedRev, ...how } = o
      try {
        const reply = await sendCommand<OpBody & Omit<ApplyOptions, 'opId'>, ReviewReply>((b) => api.retryReview(id, b), { ...op(opId), ...(expectedRev ? { expectedRev } : {}), ...how })
        if (reply.review && gate.take(`review:${reply.review.id}`, n, reply.review.rev)) apply?.(reply.review)
        return reply
      } catch (e) {
        throw continuation(e)
      }
    },
  }
}

/** The diff a Review view shows: the candidate it is of (its reference), the base asked ('' for the run's), and the reply. */
export interface ShownDiff {
  key: string
  at: string
  diff: CandidateDiff
}

/**
 * The diff a Review view shows, bound to the selection it was read for: `read(candidate, base)` names the candidate and the base wanted
 * now, and a reply is shown only while its read is the latest asked (the late-answer rule, `latest`), so a slow reply for a base or a
 * candidate left (Y, then Z, then Y again) never stands beside a draft on another. A move clears what was shown at once: nothing of the
 * candidate or the base left can be noted while the new one is read, or after its read failed. `known` is told the base a reply was read
 * against (the run's, for none asked), the draft's base while it has none; `refused` that the server refused the base asked (400: none
 * of the run's chain), so the page stops offering it. `at(candidate, base)` says whether the diff shown is the candidate's at that base,
 * which a note on its lines or its files is made only on. `again()` reads the diff wanted again when it is not shown: a stream's reset
 * drops the replies on their way (DispatchOrder.reset), and nothing else would ask again.
 */
export function useCandidateDiff(r: Pick<ReturnType<typeof useReview>, 'readDiff'>, o: { known?: (base: string) => void; refused?: (key: string, base: string) => void } = {}) {
  const shown = ref<ShownDiff | null>(null)
  const error = ref('')
  const loading = ref(false)
  const asks = latest()
  /** The candidate and the base wanted now. */
  let wanted: { key: string; at: string } | null = null

  async function read(key: string, at: string): Promise<void> {
    const current = asks.ask()
    wanted = { key, at }
    const s = shown.value
    // Shown already: this candidate at this base, or read with none asked at the base it named.
    if (s && s.key === key && (s.at === at || s.diff.base === at)) {
      error.value = ''
      loading.value = false
      return
    }
    shown.value = null
    error.value = ''
    const c = parseCandidate(key)
    if (!c) {
      loading.value = false
      return
    }
    loading.value = true
    try {
      await r.readDiff(c, key, at, (d) => {
        if (!current()) return
        shown.value = { key, at, diff: d }
        o.known?.(d.base)
      })
    } catch (e) {
      if (!current()) return
      error.value = (e as Error).message
      if (at && e instanceof ApiError && e.status === 400) o.refused?.(key, at)
    } finally {
      if (current()) loading.value = false
    }
  }

  /** Reads the diff wanted again unless it is shown (or its read failed, which says so): after a stream's reset. */
  function again(): Promise<void> | undefined {
    if (!wanted || error.value) return
    return read(wanted.key, wanted.at)
  }

  const at = (key: string, base: string): boolean => {
    const s = shown.value
    return !!s && !!base && s.key === key && s.diff.base === base
  }

  return { shown, error, loading, read, again, at }
}

/** One side of a file as the Review view shows it: its text, or that it is absent there, or binary (or past 1 MiB). */
export type SideText = DiffSide | 'absent' | 'binary'

/** A file of a diff whose two sides are read: the candidate it is of, the base, the file, and the key the view shows them by. */
export interface SidesTarget {
  key: string
  candidate: string
  base: string
  file: DiffFile
}

/**
 * The two sides of the file a Review view shows: `read(target)` reads both (a base-side by the path the base has it at, a rename's old
 * path), each applied only while its read is the latest asked (the late-answer rule, `latest`); a 404 is the side absent, a 415 binary.
 * `again()` reads the target again while a side has not come in, after a stream's reset dropped its reply.
 */
export function useFileSides(r: Pick<ReturnType<typeof useReview>, 'readSide'>) {
  const sides = ref<{ key: string; base: SideText | null; candidate: SideText | null; error: string }>({ key: '', base: null, candidate: null, error: '' })
  const asks = latest()
  let target: SidesTarget | null = null

  async function read(t: SidesTarget | null): Promise<void> {
    const current = asks.ask()
    target = t
    const c = t ? parseCandidate(t.candidate) : null
    if (!t || !c) return
    const f = t.file
    sides.value = { key: t.key, base: null, candidate: null, error: '' }
    if (f.binary) {
      sides.value = { key: t.key, base: 'binary', candidate: 'binary', error: '' }
      return
    }
    const one = async (side: Side, path: string, absent: boolean) => {
      if (absent) return void (sides.value = { ...sides.value, [side]: 'absent' })
      try {
        await r.readSide(c, t.candidate, t.base, path, side, (s) => {
          if (current()) sides.value = { ...sides.value, [side]: s }
        })
      } catch (e) {
        if (!current()) return
        if (e instanceof ApiError && e.status === 404) sides.value = { ...sides.value, [side]: 'absent' }
        else if (e instanceof ApiError && e.status === 415) sides.value = { ...sides.value, [side]: 'binary' }
        else sides.value = { ...sides.value, error: (e as Error).message }
      }
    }
    await Promise.all([one('base', f.from ?? f.path, f.status === 'A'), one('candidate', f.path, f.status === 'D')])
  }

  /** Reads the target again while a side of it has not come in and no read of it failed: after a stream's reset. */
  function again(): Promise<void> | undefined {
    const s = sides.value
    if (!target || s.error || (s.key === target.key && s.base !== null && s.candidate !== null)) return
    return read(target)
  }

  return { sides, read, again }
}

/**
 * The reviews of a ticket a Review view lists: `read()` reads every page of them (useReview.readReviews), a reply applied as the gate
 * orders replies; a failure is said only when its read is the latest asked (the late-answer rule, `latest`), so an older read failing
 * after a newer one answered says nothing, and the reviews last read stay listed beside a failure. A view closed asks nothing: a read it
 * would start then (a send of it answered after it closed) would take the page's `reviews:<ticket>` turn from the view shown.
 */
export function useSentReviews(r: Pick<ReturnType<typeof useReview>, 'readReviews'>, ticket: () => string) {
  const list = ref<ReviewSummary[]>([])
  const error = ref('')
  const asks = latest()
  let closed = false
  if (getCurrentScope()) onScopeDispose(() => (closed = true))

  async function read(): Promise<void> {
    if (closed) return
    const key = ticket()
    const current = asks.ask()
    try {
      await r.readReviews(key, (rs) => {
        if (key !== ticket()) return
        list.value = rs
        error.value = ''
      })
    } catch (e) {
      if (current() && key === ticket()) error.value = (e as Error).message
    }
  }

  return { list, error, read }
}

/**
 * A note's lines shown in its file once the file's sides are in: `show(want, f)` calls `f` while the view shows the file `want` (its
 * sides' key) and they are ready, at once or when they come in; leaving that file first cancels it, as does a later `show` or the view
 * closing, so a note's lines are never revealed in another file (#631, review round 3).
 */
export function useShowWhenReady(o: { key: () => string; ready: () => boolean }) {
  let cancel: (() => void) | null = null
  const end = () => {
    cancel?.()
    cancel = null
  }
  if (getCurrentScope()) onScopeDispose(end)
  function show(want: string, f: () => void): void {
    end()
    if (!want) return
    const here = () => o.key() === want && o.ready()
    if (here()) return f()
    const stop = watch([o.key, o.ready], ([k, r]) => {
      if (k !== want) return end()
      if (!r) return
      end()
      void nextTick(() => {
        if (here()) f()
      })
    })
    cancel = stop
  }
  return { show, cancel: end }
}

/**
 * A failed receipt's repair prepared, as the Review view's panel shows it (ReviewCheckFailure): the request read for the candidate shown
 * when it was asked (`shown`), its failure, and Check again of the candidate the prepared request names. The late-answer rule: a reply
 * is applied only to the request that asked for it, by its generation (a later prepare) and its identity (the candidate it was asked
 * for still shown), checked as it is applied.
 */
export function usePreparedRepair(r: Pick<ReturnType<typeof useReview>, 'prepare' | 'recheck'>, shown: () => string) {
  const open = ref(false)
  const prepared = ref<PreparedRepair | null>(null)
  const error = ref('')
  const loading = ref(false)
  const asks = latest()
  async function prepare(c: CandidateRef, id: string, k: number): Promise<void> {
    const at = shown()
    const current = asks.ask()
    open.value = true
    prepared.value = null
    error.value = ''
    loading.value = true
    try {
      const p = await r.prepare(c, id, k)
      if (!current()) return
      if (shown() === at) prepared.value = p
      else error.value = 'Another candidate is shown now: prepare the repair from its checks.'
    } catch (e) {
      if (current()) error.value = shown() === at ? (e as Error).message : 'Another candidate is shown now: prepare the repair from its checks.'
    } finally {
      if (current()) loading.value = false
    }
  }
  /**
   * Check again of the candidate the prepared request `p` names, as it names it: its receipt's run and task, the attempt and the commit
   * of its `recheck`, at its revision (of the run it goes to); the words of where it was checked.
   */
  async function recheck(p: PreparedRepair): Promise<string> {
    const c = { run: p.finding.check.run, task: p.finding.check.task, attempt: p.recheck.attempt, commit: p.recheck.commit }
    const reply = await r.recheck(c, p.recheck.revision)
    return recheckWords(c, { run: reply.run || p.recheck.run, attempt: reply.attempt })
  }
  return { open, prepared, error, loading, prepare, recheck }
}

/**
 * Check again as one panel of a prepared request asks it: busy while on its way, its words once answered, its failure. A reply is
 * applied only while the panel shows the prepared request it was asked of (by identity) and no reset came since (a request prepared
 * again, another receipt's): a slow Check again of receipt A never writes into receipt B's panel, nor leaves B's button busy.
 */
export function useRecheckAsk(shown: () => PreparedRepair | null) {
  const busy = ref(false)
  const rechecked = ref('')
  const failure = ref('')
  const asks = latest()
  function reset(): void {
    asks.ask()
    busy.value = false
    rechecked.value = ''
    failure.value = ''
  }
  async function again(recheck: (p: PreparedRepair) => Promise<string>): Promise<void> {
    const p = shown()
    if (!p) return
    const current = asks.ask()
    const holds = () => current() && shown() === p
    busy.value = true
    failure.value = ''
    try {
      const words = await recheck(p)
      if (holds()) rechecked.value = words
    } catch (e) {
      if (holds()) failure.value = (e as Error).message
    } finally {
      if (holds()) busy.value = false
    }
  }
  return { busy, rechecked, failure, reset, again }
}

/**
 * The ticket's acceptance criteria as a launch's package froze them (a launch never changes): read for the launch shown, and while no
 * text was taken for it, asked again at each tick the page gives (`ticks`: its reads of the ticket, the stream's resets), so a failed
 * read is said in words (`error`) and asked again rather than left reading. A reply is applied only to the latest ask, for the launch
 * still shown.
 */
export function useLaunchedCriteria(read: (id: string) => Promise<{ package?: unknown }>, launch: () => string | undefined, ticks: () => unknown) {
  const taken = ref<{ id: string; text: string } | null>(null)
  const error = ref('')
  const asks = latest()
  function ask(): void {
    const id = launch()
    if (!id || taken.value?.id === id) return
    const current = asks.ask()
    read(id)
      .then((l) => {
        if (!current() || launch() !== id) return
        const pkg = l.package as { ticket?: { acceptance?: string } } | null | undefined
        taken.value = { id, text: pkg?.ticket?.acceptance ?? '' }
        error.value = ''
      })
      .catch((e) => {
        if (current() && launch() === id) error.value = (e as Error).message || 'the read failed'
      })
  }
  watch(launch, () => (error.value = ''))
  watch([launch, ticks], ask, { immediate: true })
  /** The criteria as launched, null until read for the launch shown. */
  const text = computed(() => (taken.value && taken.value.id === launch() ? taken.value.text : null))
  return { text, error }
}

/**
 * The candidate a link names when the ticket's candidates read do not list it (a task's six newest): read from its run's older
 * candidates (`?task=&before=`), once for each candidate named, after its run's candidates were read (`listed` null before, true when
 * they list it); a failure said, and asked again at the page's next tick. A reply is applied only to the latest ask, for the candidate
 * still named.
 */
export function useOlderCandidate(o: {
  read: (run: string, task: string, before: number) => Promise<{ candidates?: Array<{ task: string; kind: 'work' | 'review'; candidates: RunCandidate[] }> }>
  want: () => CandidateRef | null
  listed: () => boolean | null
  ticks: () => unknown
}) {
  const found = ref<{ key: string; c: RunCandidate } | null>(null)
  const error = ref('')
  const loading = ref(false)
  const asks = latest()
  async function read(): Promise<void> {
    const w = o.want()
    if (!w || o.listed() !== false) return
    const key = candidateRef(w)
    if (found.value?.key === key) return
    const current = asks.ask()
    const holds = () => current() && named() === key
    loading.value = true
    error.value = ''
    try {
      const page = await o.read(w.run, w.task, w.attempt + 1)
      if (!holds()) return
      const c = page.candidates?.find((t) => t.task === w.task)?.candidates.find((x) => x.attempt === w.attempt && x.commit === w.commit)
      if (c) found.value = { key, c }
      else error.value = `attempt ${w.attempt} of ${w.task} in run ${w.run} has no candidate at ${w.commit.slice(0, 12)}`
    } catch (e) {
      if (holds()) error.value = (e as Error).message || 'the read failed'
    } finally {
      if (current()) loading.value = false
    }
  }
  const named = () => (o.want() ? candidateRef(o.want()!) : '')
  watch(named, () => (error.value = ''))
  watch(
    () => [named(), o.listed(), o.ticks()],
    () => void read(),
    { immediate: true },
  )
  /** The candidate named, read: null when it is not the one named now. */
  const candidate = computed(() => {
    const w = o.want()
    return w && found.value?.key === candidateRef(w) ? found.value.c : null
  })
  return { candidate, error, loading }
}
