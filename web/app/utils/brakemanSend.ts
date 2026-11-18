import type { BrakemanRequest } from './brakeman'
import { utf8Bytes } from './brakemanCards'

// A handoff package sent to a task's next attempt (issue #1012), as the page reads and says it: the preview route's reply
// (GET /api/brakeman/requests/{id}/items/{i}/hand), the record a send or a withdraw answers, and every word the box and its dialog
// draw. The person reads the exact text the server will hand and sends it or not: no editing, no typed text (the owner's decision).
// One send reaches the task's next attempt only. Every reply is parsed here, bounded, and refused whole when it holds a state it
// does not know; a refusal's code it does not know is a newer server's and is drawn as "cannot be sent now".

/** A package's text, in UTF-8 bytes (internal/brakeman MaxHandoffText). */
export const HAND_TEXT_MAX = 16 << 10
/** A record as the routes answer it, without its text (internal/brakeman MaxHandoffRecord). */
export const HAND_RECORD_MAX = 4 << 10
/** The preview's reply as written, encoded without HTML escaping: twice the text, the record, and 2 KiB for the rest. */
export const HAND_PREVIEW_MAX = 2 * HAND_TEXT_MAX + HAND_RECORD_MAX + (2 << 10)
/** The most attempts a record names (internal/brakeman MaxHanded). */
export const HAND_HANDED_MAX = 16
/** A name a send is signed with, in UTF-8 bytes (internal/brakeman MaxBy). */
export const HAND_BY_MAX = 60

export const HAND_STATES = ['waiting', 'handed', 'withdrawn', 'replaced'] as const
export type HandState = (typeof HAND_STATES)[number]
export const TASK_STATES = ['blocked', 'ready', 'running', 'verifying', 'succeeded', 'failed', 'cancelled'] as const
export type HandTaskState = (typeof TASK_STATES)[number]

/** A sent package's record as the routes answer it (never its text). */
export interface SentHandoff {
  id: string
  opId: string
  request: string
  item: number
  run: string
  task: string
  actor: string
  by?: string
  sentAt: string
  bytes: number
  sha256: string
  state: HandState
  handed?: Array<{ run: string; attempt: number; at: string }>
  endedAt?: string
  endedBy?: string
  /** A handed record whose attempt ended without its agent ever starting: it took the package and never read it. */
  notStarted?: boolean
}

/** Where a send would go now: the newest run of the subject's chain, its name, the task, its state and its latest attempt (0: none). */
export interface HandTarget {
  run: string
  runName: string
  task: string
  state: HandTaskState
  attempt: number
}

/** The preview: the exact text, its size and SHA-256, exactly one of where it goes and why it cannot, and the newest record of it. */
export interface HandPreview {
  text: string
  bytes: number
  sha256: string
  target?: HandTarget
  refusal?: { code: string; message: string }
  handoff?: SentHandoff
}

const HEX64 = /^[0-9a-f]{64}$/
const HANDOFF_ID = /^ho-[0-9]{1,18}$/
const BRIEF_ID = /^br-[0-9]{1,18}$/
const CODE = /^[a-z][a-z_]{0,39}$/
/** A short string of the reply (an id, a name, a time): present, at most this many characters. */
const SHORT = 256

const isObj = (x: unknown): x is Record<string, unknown> => typeof x === 'object' && x !== null && !Array.isArray(x)
const short = (x: unknown, empty = false): x is string => typeof x === 'string' && x.length <= SHORT && (empty || x.length > 0)
const count = (x: unknown, min = 0): x is number => typeof x === 'number' && Number.isInteger(x) && x >= min && x <= Number.MAX_SAFE_INTEGER
const optional = <T>(x: unknown, ok: (v: unknown) => v is T): boolean => x === undefined || ok(x)

/** A record of a sent package as a route answers it, or null for one the page cannot take whole (a state it does not know, a bound passed). */
export function asSentHandoff(x: unknown): SentHandoff | null {
  if (!isObj(x) || utf8Bytes(JSON.stringify(x)) > HAND_RECORD_MAX) return null
  const { id, opId, request, item, run, task, actor, by, sentAt, bytes, sha256, state, handed, endedAt, endedBy, notStarted } = x
  if (!short(id) || !HANDOFF_ID.test(id) || !short(opId) || !short(request) || !BRIEF_ID.test(request) || !count(item)) return null
  if (!short(run) || !short(task) || !short(actor, true) || !short(sentAt) || !count(bytes) || typeof sha256 !== 'string' || !HEX64.test(sha256)) return null
  if (typeof state !== 'string' || !(HAND_STATES as readonly string[]).includes(state)) return null
  if (!optional(by, (v): v is string => typeof v === 'string' && utf8Bytes(v) <= HAND_BY_MAX)) return null
  if (!optional(endedAt, (v): v is string => short(v)) || !optional(endedBy, (v): v is string => typeof v === 'string' && utf8Bytes(v) <= HAND_BY_MAX)) return null
  if (!optional(notStarted, (v): v is boolean => typeof v === 'boolean')) return null
  let list: SentHandoff['handed']
  if (handed !== undefined) {
    if (!Array.isArray(handed) || handed.length > HAND_HANDED_MAX) return null
    list = []
    for (const h of handed) {
      if (!isObj(h) || !short(h.run) || !count(h.attempt, 1) || !short(h.at)) return null
      list.push({ run: h.run, attempt: h.attempt, at: h.at })
    }
  }
  const out: SentHandoff = { id, opId, request, item, run, task, actor, sentAt, bytes, sha256, state: state as HandState }
  if (by) out.by = by as string
  if (list?.length) out.handed = list
  if (endedAt) out.endedAt = endedAt as string
  if (endedBy) out.endedBy = endedBy as string
  if (notStarted) out.notStarted = true
  return out
}

/**
 * The preview's reply, `size` the bytes it came in, or null for one the page cannot take whole: past HAND_PREVIEW_MAX as it came, a
 * text past HAND_TEXT_MAX in UTF-8 bytes once parsed or not of the size it says, a task's state or a record's it does not know, a
 * refusal's code that is no code, and both or neither of where it goes and why it cannot.
 */
export function asHandPreview(size: number, x: unknown): HandPreview | null {
  if (size > HAND_PREVIEW_MAX || !isObj(x)) return null
  const { text, bytes, sha256, target, refusal, handoff } = x
  if (typeof text !== 'string' || !count(bytes) || typeof sha256 !== 'string' || !HEX64.test(sha256)) return null
  const textBytes = utf8Bytes(text)
  if (textBytes > HAND_TEXT_MAX) return null
  if ((target === undefined) === (refusal === undefined)) return null
  const out: HandPreview = { text, bytes, sha256 }
  if (target !== undefined) {
    if (!isObj(target) || !short(target.run) || !short(target.runName, true) || !short(target.task) || !count(target.attempt)) return null
    if (typeof target.state !== 'string' || !(TASK_STATES as readonly string[]).includes(target.state)) return null
    out.target = { run: target.run, runName: target.runName, task: target.task, state: target.state as HandTaskState, attempt: target.attempt }
  } else {
    if (!isObj(refusal) || typeof refusal.code !== 'string' || !CODE.test(refusal.code) || typeof refusal.message !== 'string' || refusal.message.length > 1024) return null
    out.refusal = { code: refusal.code, message: refusal.message }
  }
  // The text is the one whose size the reply gives, except a package too large to send, whose text is left out.
  if (text === '' ? bytes !== 0 && out.refusal?.code !== 'package_too_large' : bytes !== textBytes) return null
  if (handoff !== undefined) {
    const h = asSentHandoff(handoff)
    if (!h) return null
    out.handoff = h
  }
  return out
}

/** The words for a reply the page could not take whole. */
export const HAND_MALFORMED = "The server's answer about this handoff package could not be read: reload the page."

/**
 * The preview of request `id`'s item `item` from the reply as it came (its text and its size in bytes), or a throw of HAND_MALFORMED:
 * JSON that does not parse, a reply asHandPreview refuses, or a record of another request or item.
 */
export function parsePreview(text: string, size: number, id: string, item: number): HandPreview {
  let data: unknown
  try {
    data = JSON.parse(text)
  } catch {
    throw new Error(HAND_MALFORMED)
  }
  const p = asHandPreview(size, data)
  if (!p || (p.handoff && (p.handoff.request !== id || p.handoff.item !== item))) throw new Error(HAND_MALFORMED)
  return p
}

/** Whether an error is an older server's: its mux answers 404 with no error code of the server's own, and then no control is drawn. */
export function olderServer(e: { status?: number; code?: string } | null | undefined): boolean {
  return e?.status === 404 && e.code === 'http_error'
}

/** Whether a brief's handoff package may have a send at all, before any read: the page's own brief (`request` and `index` given) asked to prepare a handoff. */
export function sendOffered(request: Pick<BrakemanRequest, 'kind'> | undefined, index: number | undefined): boolean {
  return !!request && index !== undefined && Number.isInteger(index) && index >= 0 && request.kind === 'handoff'
}

/** A character a signed name may not hold (the server's quoted.Plain): a control, a line end, a format character, one that draws nothing. */
const HAND_BY_HIDDEN = /[\p{Cc}\p{Cf}\p{Cs}\p{Co}\p{Cn}\p{Zl}\p{Zp}\p{Default_Ignorable_Code_Point}\p{Variation_Selector}\u0085]/u

/**
 * A name a send can be signed with: the person's name, trimmed, when it is one plain line of at most 60 bytes (the server's rule,
 * crew.HandoffByOK and brakeman.HandoffByOK: no line end of any kind, no tab, no character that draws nothing); "" for none; null for a
 * name the server refuses, which the page refuses before sending, in words (BAD_NAME).
 */
export function handBy(name: string): string | null {
  const n = name.trim()
  if (!n) return ''
  if (utf8Bytes(n) > HAND_BY_MAX || HAND_BY_HIDDEN.test(n)) return null
  return n
}

/** The words for a name the server would refuse: nothing is sent under it. */
export const BAD_NAME = 'Your name in Settings is not one plain line of at most 60 bytes (no line breaks, tabs or hidden characters): change it there to send it.'

// The words a person reads. Each is fixed; the names and numbers are filled in.

/** The control that opens the dialog. */
export const sendLabel = (task: string) => `Send to ${task}'s next attempt…`
/** Where the package goes. */
export const targetLine = (t: Pick<HandTarget, 'task' | 'run' | 'runName'>) => `To ${t.task} of ${t.runName || t.run}.`
export const HAND_STARTS_NOTHING = 'Sending starts nothing.'
export const HAND_AS_REPORT = 'The agent reads it as a quoted report in its task file, not as instructions.'
export const HAND_QUOTES = 'It may quote a terminal screen or a check log.'
/** Who else can read what is sent: the boundary, in the one sentence the dialog always draws. */
export const whoReads = (task: string) => `Whoever can watch ${task}'s session or read its files (a link's holder too) can read what you send, and the agent may repeat it in its result.`
/** Under whose name it goes. */
export const underName = (by: string | null) => (by === null ? BAD_NAME : by ? `It goes under your name, ${by}.` : 'It goes with no name: set your name in Settings to sign it.')

/** The dialog's sentence for the task's state now: which attempt is handed the package. */
export function stateSentence(t: Pick<HandTarget, 'task' | 'state' | 'attempt'>): string {
  const task = t.task
  switch (t.state) {
    case 'blocked':
    case 'ready':
      return t.attempt > 0 ? `${task} waits for attempt ${t.attempt + 1}, which is handed the package.` : `${task} has not started: its first attempt is handed the package.`
    case 'running':
    case 'verifying':
      return `${task} is running attempt ${t.attempt}, which is not told. The package waits for the attempt after it, if there is one.`
    case 'failed':
      return `${task} has failed: the package waits for its next attempt. Retry ${task} on its run to start one.`
    case 'cancelled':
      return `${task} was cancelled: the package waits for its next attempt. Retry ${task} on its run to start one.`
    case 'succeeded':
      return `${task} has a result. The package is handed only if ${task} is opened again, by a review that asks for changes.`
  }
}

/** The attempt a handed record went to: the last it names. */
export const handedTo = (h: Pick<SentHandoff, 'handed'>) => h.handed?.[h.handed.length - 1]

/** The box's line for a sent package's record, the run's name given (the record holds its id). */
export function sentLine(h: SentHandoff, runName: string): string {
  switch (h.state) {
    case 'waiting':
      return `Sent to ${h.task} of ${runName || h.run}${h.by ? ` by ${h.by}` : ''} \u00b7 waits for its next attempt`
    case 'handed': {
      const n = handedTo(h)?.attempt ?? 0
      return h.notStarted ? `Taken for attempt ${n} of ${h.task}, which did not start: it was not read. Send it again.` : `Handed to attempt ${n} of ${h.task}`
    }
    case 'withdrawn':
      return h.endedBy ? `Withdrawn by ${h.endedBy}` : 'Withdrawn'
    case 'replaced':
      return 'Replaced by a newer package'
  }
}

/** The preview's refusals for which the control is absent: the package goes to no task (another kind, a session's, a lane, a run without tasks). */
export const ABSENT_CODES: readonly string[] = ['no_task', 'not_managed', 'comparison']

/** The words for a refusal of the preview or the send, by its code; a code this page does not know is "It cannot be sent now." */
export function handRefusalWords(code: string, ctx: { task?: string; bytes?: number } = {}): string {
  const task = ctx.task || 'the task'
  switch (code) {
    case 'no_task':
      return 'Only a package asked for about one task is sent, to that task.'
    case 'run_not_running':
      return 'The run is not running on this server. Continue it, then send the package: it goes to the continuation.'
    case 'run_continuing':
      return `A run of ${task}'s chain is being continued: send the package once it runs.`
    case 'not_managed':
      return 'The run has no tasks to hand a package to.'
    case 'comparison':
      return "A comparison's lanes are handed alike: a package goes to no lane."
    case 'task_not_found':
      return `The run's crew has no task ${task}: the package cannot go to it.`
    case 'package_too_large':
      return ctx.bytes ? `The package is ${(ctx.bytes / 1024).toFixed(1)} KiB: a handoff package is sent at most 16 KiB.` : 'The package is larger than a handoff package is sent: at most 16 KiB.'
    case 'text_changed':
      return "The package's text is not the one shown: read it again, then send."
    case 'handoffs_full':
      return '20 handoff packages wait for their tasks: withdraw one from its brief, then send this one.'
    case 'op_conflict':
      return 'This send was given an id another package was sent with: close the dialog and send again.'
    case 'not_a_handoff':
      return 'This item is not a handoff package.'
    case 'not_found':
      return 'The brief or its package is not kept on this server any more.'
    case 'pruned':
      return 'The brief was pruned: the server keeps the newest 500 requests that are done.'
    case 'invalid_request':
    case 'bound':
      return 'The server refused the send as written: reload the page and send again.'
    case 'brakeman_not_owner':
      return "Another server owns Brakeman's records: send it from that server."
    case 'store_unavailable':
    case 'store_failed':
      return "Brakeman's records cannot be written now: nothing was sent. Try again later."
    case 'brakeman_recovering':
      return "Brakeman's records are being recovered: send it again in a moment."
    case 'shutting_down':
      return 'The server is shutting down: nothing was sent. Send it once the server is back.'
    case 'switchyard':
      return 'A switchyard runs no Brakeman: a handoff package is sent from the server that runs the task.'
    case 'unauthorized':
      return "The page's token is not the admin's: nothing was sent."
    case 'network':
      return 'The server did not answer: read the package again to see whether it was sent.'
  }
  return 'It cannot be sent now.'
}

/** A waiting record as handoffs_full names it, which the dialog links to its brief; a name the page cannot take is left out. */
export interface HandWaiting {
  id: string
  request: string
  run: string
  task: string
}

/** The waiting records handoffs_full's error names, at most 20. */
export function asWaiting(x: unknown): HandWaiting[] {
  if (!Array.isArray(x)) return []
  const out: HandWaiting[] = []
  for (const w of x.slice(0, 20)) {
    if (isObj(w) && short(w.id) && HANDOFF_ID.test(w.id) && short(w.request) && BRIEF_ID.test(w.request) && short(w.run) && short(w.task)) out.push({ id: w.id, request: w.request, run: w.run, task: w.task })
  }
  return out
}

/** What the box draws beside Copy and Comment, from the preview. */
export type HandControl =
  | { kind: 'absent' }
  | { kind: 'send'; label: string; again: boolean }
  | { kind: 'disabled'; label: string; why: string }
  | { kind: 'waiting' }

/**
 * The box's control: absent for a package that goes to no task; Withdraw's place while one waits; the send, enabled with a target,
 * disabled with its reason otherwise; `again` when the last one was taken by an attempt that never started, so one press sends the
 * same text again.
 */
export function handControl(p: HandPreview | null): HandControl {
  if (!p || (p.refusal && ABSENT_CODES.includes(p.refusal.code))) return { kind: 'absent' }
  if (p.handoff?.state === 'waiting') return { kind: 'waiting' }
  const task = p.target?.task ?? p.handoff?.task ?? ''
  const label = task ? sendLabel(task) : "Send to the task's next attempt…"
  if (p.refusal) return { kind: 'disabled', label, why: handRefusalWords(p.refusal.code, { task, bytes: p.bytes }) }
  const again = !!p.handoff && p.handoff.state === 'handed' && !!p.handoff.notStarted && p.handoff.sha256 === p.sha256
  return { kind: 'send', label, again }
}
