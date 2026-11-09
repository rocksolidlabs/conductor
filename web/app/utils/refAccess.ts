import type { InjectionKey } from 'vue'
import type { TicketComment } from '~/composables/useDispatch'
import { parseRef, receiptPath, refPage, type RefResolution } from './brakeman'
import type { LaneEvidence } from './transport/decide'

// How an evidence chip (BrakemanRefChip.vue) and the record it reads (BrakemanReceipt.vue) reach what a ref names
// (docs/plans/decision-inbox.md, PR-4, "DR1's card on a link"): the place the chip opens and the read of its record, injected
// (`provide(REF_ACCESS, …)`), so DR1's card (DecisionCard.vue, not edited) draws its chips on a decision link's join page, where the
// workbench's routes and its token are not the holder's. With nothing injected the access is today's: the workbench's page for the
// ref (refPage) and its record read over HTTP with the token (receiptPath, useApi). Pure but for the reads it is given.

/** Where a chip's ref opens: a page of this app, an action of the page that drew it (a terminal tab), or nowhere, with the words why. */
export type RefTarget = { to: string } | { open: () => void } | { words: string }

/**
 * A ref's record as read: a text (a check's log, a receipt as JSON) with the SHA-256 the server sent beside it, or a ticket's comment.
 * A log read through a link may be part of the log, not the log its SHA-256 is of: `cut`, its last 4 KiB of `bytes`; `dropped`, a log
 * not kept, read as the end its receipt keeps (docs/protocol.md, Decision links, Evidence).
 */
export type RefRead = { kind: 'text'; text: string; sha256?: string; cut?: boolean; bytes?: number; dropped?: boolean } | { kind: 'comment'; comment: TicketComment | null }

/**
 * What a read's text is, when it is not the whole record its SHA-256 is of ('' when it is): a log not kept, shown as the end its
 * receipt keeps; a log cut to its last 4 KiB. The receipt says it above the text, and names the SHA-256 the whole log's.
 */
export function refReadShown(r: RefRead): string {
  if (r.kind !== 'text') return ''
  if (r.dropped) return "The log was not kept: the run's logs were at their bound. Shown is the end of its output its receipt keeps, not the log."
  if (r.cut) return `Shown is the log's last 4 KiB${r.bytes ? ` of its ${r.bytes.toLocaleString('en-US')} bytes` : ''}: the whole log is read on the desktop or the LAN.`
  return ''
}

/** What a chip and its record read through. */
export interface RefAccess {
  /** Where the chip opens: null for nowhere (the chip shows its label alone). */
  route(ref: string, resolution?: RefResolution): RefTarget | null
  /** The read of the ref's record, when the chip offers one (null when it does not): called as the person opens it. */
  read(ref: string, resolution?: RefResolution): (() => Promise<RefRead>) | null
  /**
   * Whether the run that holds a durable decision runs here now, for DR1's card (useDecisionRun): absent for today's (the live store,
   * or the run read through its route). A decision link's page has no route to read it by, and answers `undefined` (not known).
   */
  runHeld?: (run: string) => boolean | undefined
}

/** The injection key: the join page's Decide provides the link's access to what it draws. */
export const REF_ACCESS: InjectionKey<RefAccess> = Symbol('refAccess')

/** What today's access reads through: useApi's two calls. */
export interface RefApi {
  request: <T>(path: string) => Promise<T>
  requestText: (path: string) => Promise<{ text: string; headers: Headers }>
}

/** Today's access: the workbench's page for the ref, and its record read over HTTP with the token (BrakemanReceipt's reads, as they were). */
export function defaultRefAccess(api: RefApi): RefAccess {
  return {
    route(ref, resolution) {
      const to = refPage(ref, resolution)
      return to ? { to } : null
    },
    read(ref, resolution) {
      const path = receiptPath(ref, resolution)
      if (!path) return null
      const kind = parseRef(ref)?.kind ?? ''
      return async () => {
        if (kind === 'check') {
          const r = await api.requestText(path)
          return { kind: 'text', text: r.text, sha256: r.headers.get('X-Conductor-Log-SHA256') ?? '' }
        }
        if (kind === 'comment') {
          const r = await api.request<{ comments?: TicketComment[] }>(path)
          return { kind: 'comment', comment: r.comments?.[0] ?? null }
        }
        return { kind: 'text', text: JSON.stringify(await api.request<unknown>(path), null, 2) }
      }
    },
  }
}

/** The words of where a link's chip cannot open: the workbench's pages are not a link holder's. */
export const LINK_ELSEWHERE = 'open it on the desktop or the LAN'

/**
 * A decision link's access (the join page's Decide), for the evidence of the item `item()` names: a session's chip opens that member's
 * terminal tab (`openSession`, which reads the entry through the lane and says so when the session is not in the link's scope); a
 * check's chip reads its log's end through the lane (`decision_evidence`), checked to be the entry asked, with whether it is cut or
 * the log was dropped (refReadShown); anything else shows its label and the words where it is opened. Nothing is read over HTTP, and
 * nothing goes to a workbench page.
 */
export function linkRefAccess(o: { evidence: (ref: string) => Promise<LaneEvidence>; openSession: (ref: string) => void }): RefAccess {
  return {
    route(ref) {
      const kind = parseRef(ref)?.kind
      if (kind === 'session') return { open: () => o.openSession(ref) }
      if (kind === 'check') return null
      return { words: LINK_ELSEWHERE }
    },
    read(ref) {
      if (parseRef(ref)?.kind !== 'check') return null
      return async () => {
        const e = await o.evidence(ref)
        if (e.kind !== 'log' || e.ref !== ref) throw new Error('The link answered for another entry.')
        return { kind: 'text', text: e.tail, sha256: e.sha256 ?? '', ...(e.cut ? { cut: true } : {}), ...(e.bytes !== undefined ? { bytes: e.bytes } : {}), ...(e.dropped ? { dropped: true } : {}) }
      }
    },
    runHeld: () => undefined,
  }
}

/** The words of a record's read that failed (BrakemanReceipt's): a log not kept, a record gone, or the server's or the lane's own. */
export function refReadError(e: unknown): string {
  const err = e as { status?: number; code?: string; message?: string }
  if (err.status === 410 || err.code === 'log_dropped') return "The log was not kept: the run's logs were at their bound. Its SHA-256 stays on the receipt."
  if (err.status === 404 || err.code === 'not_found') return 'The server holds no such record now.'
  return err.message || 'The record could not be read.'
}
