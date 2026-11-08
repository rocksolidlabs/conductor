import type { InjectionKey } from 'vue'
import { ApiError } from './useApi'
import type { DispatchReceipt, OpBody } from './useDispatch'
import { through, type DispatchOrder } from '~/utils/dispatchOrder'
import { mintCommandId, sendCommand } from '~/utils/tasks'
import { allPages, parseCandidate, type CandidateRef } from '~/utils/dispatch'
import { latest, type Located, type NewFinding, type ReviewApplication, type ReviewBody, type Side } from '~/utils/review'

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
    prepareRepair: (c: CandidateRef, step: string, k: number) => request<unknown>(`${attemptPath(c)}/steps/${enc(step)}/${k}/repair`),
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

/** The open findings of a run's task among a ticket's reviews: each review counts its own by state (its findings' latest marks), as the store does. */
export function openFindingsOf(reviews: readonly ReviewSummary[], run: string, task: string): number {
  let n = 0
  for (const r of reviews) {
    const c = parseCandidate(r.candidate)
    if (c?.run === run && c.task === task) n += r.counts?.open ?? 0
  }
  return n
}

/** What the ticket page gives the candidates it lists (DispatchCandidates): the open findings of a run's task, and the Review view's link. */
export interface ReviewEntry {
  openFindings: (run: string, task: string) => number
  href: (candidate: string) => { query: Record<string, string> }
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
 * after a newer one answered says nothing, and the reviews last read stay listed beside a failure.
 */
export function useSentReviews(r: Pick<ReturnType<typeof useReview>, 'readReviews'>, ticket: () => string) {
  const list = ref<ReviewSummary[]>([])
  const error = ref('')
  const asks = latest()

  async function read(): Promise<void> {
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
