import type { CaptureSendAnswer } from './capture'
import type { MarkView, TaskMarkKey } from './status'
import { markLook } from './status'

/**
 * A capture's sends (Program item PV1, docs/plans/browser-evidence.md, "Sending"): where a capture goes from the editor's Send
 * (CaptureSend.vue), the body each destination posts (POST /api/captures/{id}/send, docs/protocol.md "Captures"), and a send's state
 * in words and in the one status language's marks. Pure: CaptureSend and DispatchCaptures draw it, vitest proves it.
 */

/** A destination the editor offers. review, review-draft and decision are added by the rows that own reviews and decisions. */
export type SendKind = 'ticket-new' | 'ticket' | 'brakeman' | 'review' | 'review-draft' | 'decision'

/** What the editor's Send form holds, every destination's fields. */
export interface SendForm {
  /** A new ticket's project's key and title. */
  project?: string
  title?: string
  /** An existing ticket's key. */
  key?: string
  /** The question to Brakeman. */
  question?: string
  /** A pending decision of the capture's run, its dr-<n>. */
  decision?: string
}

/** A send's state as the store gives it. */
export type SendStateName = 'pending' | 'sent' | 'refused' | 'cancelled'

/** A send as the routes answer it (dispatch.CaptureSend, with the desk's words a pending one waits on): utils/capture.ts's. */
export type CaptureSendView = CaptureSendAnswer

/** The kinds the editor offers today, in its order, with their labels; a row that owns another destination adds its own entry. */
export const SEND_KINDS: ReadonlyArray<{ kind: SendKind; label: string; icon: string }> = [
  { kind: 'ticket-new', label: 'Create a ticket', icon: 'i-lucide-ticket-plus' },
  { kind: 'ticket', label: 'Attach to a ticket', icon: 'i-lucide-paperclip' },
  { kind: 'brakeman', label: 'Ask Brakeman', icon: 'i-lucide-notebook-pen' },
  { kind: 'decision', label: 'Attach to a decision', icon: 'i-lucide-message-square-plus' },
  // A review of the candidate a capture shows (PR-10): CaptureSendReview.vue, offered on a candidate's capture alone (Decision 9).
  { kind: 'review', label: 'Request a change', icon: 'i-lucide-git-pull-request-arrow' },
  { kind: 'review-draft', label: 'Add to a review', icon: 'i-lucide-list-plus' },
]

/**
 * A question's bound, in characters, as the route holds it. The route's body bound (32 KiB, dispatch.MaxSendRequest) holds 4,000
 * characters in any script as the body encodes them, so what the form takes the route takes.
 */
export const MAX_SEND_QUESTION = 4000
/** A new ticket's title's bound, in characters, as Dispatch holds it. */
export const MAX_TICKET_TITLE = 200

/** A ticket's key: its project's key, a dash and its number. */
const KEY = /^[A-Z][A-Z0-9]{1,5}-[1-9][0-9]{0,17}$/
/** A project's key. */
const PROJECT = /^[A-Z][A-Z0-9]{1,5}$/
/** A decision record's id. */
const DECISION = /^dr-[1-9][0-9]{0,17}$/

/**
 * The `to` of a send of kind, from the form, or why it cannot be sent yet: a new ticket names a project and a title of 1 to 200
 * characters, an existing one its key, Brakeman a question of 1 to 4,000 characters. Text is sent as typed, trimmed at its ends.
 */
export function sendBody(kind: SendKind, form: SendForm): { to: Record<string, string> } | { problem: string } {
  const title = (form.title ?? '').trim()
  const question = (form.question ?? '').trim()
  switch (kind) {
    case 'ticket-new':
      if (!PROJECT.test(form.project ?? '')) return { problem: 'Choose a project' }
      if (!title) return { problem: 'Give the ticket a title' }
      if ([...title].length > MAX_TICKET_TITLE) return { problem: `A title is at most ${MAX_TICKET_TITLE} characters` }
      return { to: { kind: 'ticket', project: form.project!, title } }
    case 'ticket':
      if (!KEY.test(form.key ?? '')) return { problem: 'Choose a ticket' }
      return { to: { kind: 'ticket', key: form.key! } }
    case 'brakeman':
      if (!question) return { problem: 'Ask a question' }
      if ([...question].length > MAX_SEND_QUESTION) return { problem: `A question is at most ${MAX_SEND_QUESTION} characters` }
      return { to: { kind: 'brakeman', question } }
    case 'decision':
      if (!DECISION.test(form.decision ?? '')) return { problem: 'Choose a decision' }
      return { to: { kind: 'decision', decision: form.decision! } }
  }
  return { problem: 'This destination is not offered here' }
}

/** A new ticket's title from the capture's first note: its first line, cut to the title's bound on a word, with "…". */
export function titleFrom(note: string): string {
  const line = (note.split('\n').find((l) => l.trim()) ?? '').trim().replace(/^[#>*\-\s]+/, '')
  const chars = [...line]
  if (chars.length <= MAX_TICKET_TITLE) return line
  const cut = chars.slice(0, MAX_TICKET_TITLE - 1).join('')
  const space = cut.lastIndexOf(' ')
  return `${space > MAX_TICKET_TITLE / 2 ? cut.slice(0, space) : cut}…`
}

/** Where a send went, in words: the ticket's key, Brakeman and its request, or the decision. */
export function sendTarget(s: Pick<CaptureSendView, 'kind' | 'target'>): string {
  if (s.kind === 'brakeman') return s.target ? `Brakeman (${s.target})` : 'Brakeman'
  if (s.kind === 'decision') return s.target ? `decision ${s.target}` : 'a decision'
  // A send to a review: Add to a review's goes to the person's draft, Request a change's to the review it made.
  if (s.kind === 'review') return s.target === 'draft' ? 'your review draft' : 'a review requesting changes'
  return s.target ?? 'a ticket'
}

/** A send's state as the one status language draws it: its mark (a task's own), its word and its tone, words only. */
export interface SendStateView {
  word: string
  tone: 'neutral' | 'error'
  mark: MarkView
}

const SEND_MARK: Record<string, TaskMarkKey> = { pending: 'waiting', sent: 'done', refused: 'failed', cancelled: 'cancelled' }

/**
 * A send's state in words: "sent as r2 to CND-14"; "sent as r1 to Brakeman (br-3) · cancel asked" once a person cancelled it (a
 * cancel of a sent send closes its request at the desk, and the send stays sent); "waiting for Brakeman: <the desk's words>" (pending,
 * the words of its last try); "sending: <words>" for a send to a decision whose comment waits; "refused: <why>"; "cancelled". Its mark
 * is a task's own (waiting, done, failed, cancelled), so no surface keeps a colour of its own.
 */
export function sendState(s: Pick<CaptureSendView, 'state' | 'rev' | 'kind' | 'target' | 'why' | 'waiting'> & { cancels?: readonly unknown[] }): SendStateView {
  const key = SEND_MARK[s.state] ?? 'waiting'
  const look = markLook(key)
  let word: string
  switch (s.state) {
    case 'sent':
      word = `sent as r${s.rev} to ${sendTarget(s)}${s.kind === 'brakeman' && s.cancels?.length ? ' · cancel asked' : ''}`
      break
    case 'refused':
      word = s.why ? `refused: ${s.why}` : 'refused'
      break
    case 'cancelled':
      word = 'cancelled'
      break
    default:
      if (s.kind === 'brakeman') word = `${s.waiting ? `waiting for Brakeman: ${s.waiting}` : 'waiting for Brakeman'}${s.cancels?.length ? ' · cancel asked' : ''}`
      else word = s.waiting ? `sending: ${s.waiting}` : 'sending'
  }
  return { word, tone: s.state === 'refused' ? 'error' : 'neutral', mark: { key, word, legend: look.legend } }
}

/**
 * Whether a send can be cancelled: a pending or a sent one to Brakeman whose request may still run, and no cancel asked yet (the
 * settlement finishes a cancel asked, so a second is never needed).
 */
export function cancellable(s: Pick<CaptureSendView, 'kind' | 'state'> & { cancels?: readonly unknown[] }): boolean {
  return s.kind === 'brakeman' && (s.state === 'pending' || s.state === 'sent') && !s.cancels?.length
}

/** What a cancel's reply says to the person: done, the request finished first, or asked and finished later. */
export function cancelWords(c: { done: boolean; words?: string }): string {
  if (c.words) return `${c.words.charAt(0).toUpperCase()}${c.words.slice(1)}.`
  return c.done ? 'Cancelled.' : 'Cancel asked: it is finished once Brakeman holds its records again.'
}

/** A ticket attachment a send of a capture made: `capture-<id>-r<rev>.json` or `.png`. */
const CAPTURE_FILE = /^capture-[0-9a-f]{16}-r[1-9][0-9]*\.(json|png)$/

/**
 * The attachments a new plan revision hands over at first (DispatchPlanPropose, a revision based on none): the ticket's live ones a
 * capture's send made, newest first, at most `most` and at most `bytes` in all (a file that would pass it is left, and the older ones
 * after it are still tried); `left` is how many such files were left unticked for the bytes.
 */
export function captureAttachments(
  attachments: ReadonlyArray<{ id: string; name: string; size?: number; createdAt?: string; deletedAt?: string }>,
  most: number,
  bytes = Infinity,
): { ids: string[]; left: number } {
  const ids: string[] = []
  let total = 0
  let left = 0
  const files = attachments.filter((a) => !a.deletedAt && CAPTURE_FILE.test(a.name)).sort((a, b) => (b.createdAt ?? '').localeCompare(a.createdAt ?? ''))
  for (const a of files) {
    if (ids.length >= most) break
    if (total + (a.size ?? 0) > bytes) {
      left++
      continue
    }
    total += a.size ?? 0
    ids.push(a.id)
  }
  return { ids, left }
}
