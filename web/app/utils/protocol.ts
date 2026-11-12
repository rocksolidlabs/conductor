// Mirror of internal/proto (see docs/protocol.md). Keep both in sync.

import type { NvimHolds } from './nvimHold'

export const FrameType = {
  Output: 0x01,
  Input: 0x02,
  Control: 0x03,
  Scrollback: 0x04,
  Signal: 0x05,
  File: 0x06,
  Chunk: 0x07,
  /** client -> owner: one part of a file save (design round 12, F6). */
  FileWrite: 0x08,
  /** owner -> client: a decision link's lane reply (see Decision links, below). */
  Decision: 0x09,
  /** client -> owner: one action on a decision, to an owner whose welcome says `decisions`. */
  Decide: 0x0a,
  Relay: 0x10,
} as const

export const ProtoVersion = 1

/**
 * A hello of 0 × 0 follows the session's size (proto.HelloSize): a scaled tile or a quick reply never resizes a
 * session. Any other size a controller sends sets it, latest controller wins (docs/protocol.md, Resize policy).
 */
export const FOLLOW_SIZE = { cols: 0, rows: 0 } as const

/** The longest line a `submit` control message carries, in bytes (proto.MaxSubmit). */
export const MAX_SUBMIT = 4096

/** A session's name, in bytes (proto.MaxSessionName): a launch's and a rename's (PATCH /api/sessions/{id}); the server cuts one longer. */
export const MAX_SESSION_NAME = 120

/** An answer's bounds (proto.MaxQuestionID, MaxAnswerReqID, MaxAnswerInput): the question's id, the request's, a choice's keys, in bytes. */
export const MAX_QUESTION_ID = 32
export const MAX_ANSWER_REQ_ID = 32
export const MAX_ANSWER_INPUT = 48

/** The error code of an answer whose question is no longer the one waiting: answered by another reply first, or gone. Nothing was sent. */
export const QUESTION_ANSWERED = 'question_answered'

/**
 * The error code of an answer whose question mirrors a durable decision (the attention's `decision`): it is answered as that
 * decision, never as the session's question, and the message names it. Nothing was sent.
 */
export const ANSWER_DECISION = 'decision'

/** A durable decision's id where the protocol carries it (`dr-<n>`), in bytes (proto.MaxDecisionRef). */
export const MAX_DECISION_REF = 16

/**
 * A reply to the question a person was shown (`answer`, client → owner, controllers only; only to an owner whose welcome says
 * `answer`): `question` is that attention's `id`; exactly one of `input` (a choice's keys, written as INPUT is) and `text` (a line,
 * submitted as `submit` is). The owner writes nothing unless the question still waits, and answers `reqId` with `answer_ok` or an
 * `error` naming it (`question_answered` once the question no longer waits).
 */
export interface AnswerMsg {
  t: 'answer'
  reqId?: string
  question: string
  input?: string
  text?: string
}

/**
 * A line to type (`submit`, client → owner, controllers only), as a reply box does. With `reqId` (at most 32 bytes of printable
 * ASCII, to an owner whose welcome said `submitAck`, #322) the owner answers it with `submit_ok` once the line is entered, or an
 * `error` naming it (`not_sent` when `unlessWaiting` held it back from a session that asks, nothing typed; `input_failed` when the
 * line was typed and not entered). Without `reqId` nothing is answered.
 */
export interface SubmitMsg {
  t: 'submit'
  text: string
  reqId?: string
  unlessWaiting?: boolean
}

/** A chat message's text, in bytes after cleaning (proto.MaxChatText); the composer counts from CHAT_COUNTER_FROM. */
export const MAX_CHAT_TEXT = 2048
export const CHAT_COUNTER_FROM = 1536
/** One connection may post this many a second, this many at once (proto.ChatRatePerSecond, ChatBurst). */
export const CHAT_RATE_PER_SECOND = 10
export const CHAT_BURST = 20
/** The `to` of a post that is also typed into this session's agent. */
export const CHAT_TO_AGENT = 'agent'

export type ChatScope = 'session' | 'run'
export type ChatKind = 'message' | 'system' | 'sent_to_agent' | 'question'
/** Who a chat message is from: the viewer's subscription, as the roster names it. */
export interface ChatBy {
  id: string
  name: string
  /** `agent` on a question: the agent's, no viewer's. */
  role: Role | 'agent'
}
/** One message of a chat (`chat`, owner → client): a message, a join or leave line about `by`, or a marker that `by` typed `ref` into the agent (`to` names a run's member). */
export interface ChatMessage {
  t: 'chat'
  id: string
  at: string
  scope: ChatScope
  kind: ChatKind
  by: ChatBy
  text?: string
  ref?: string
  to?: string
  on?: string
  /** `answered` names, in `ref`, the question `by` answered, `text` the label of the choice picked when it was one (never a line typed: it may be a credential; #364); `withdrawn` one that left without an answer through Conductor, `text` saying why: `terminal` (keys typed there answered it) or `agent` (it moved on). */
  event?: 'join' | 'leave' | 'answered' | 'withdrawn'
  /** The sender's own id of the post, echoed. */
  nonce?: string
  /** A question's choices (at most 6), typed as the attention's are. */
  options?: AttentionOption[]
  /** Lines of a file the message is about (F7). */
  quote?: ChatQuote
}
/** A viewer's post (`chat`, client → owner). */
export interface ChatPost {
  t: 'chat'
  nonce?: string
  scope?: ChatScope
  text: string
  on?: string
  to?: string
  /** Lines of a file the message is about (design round 12, F7). */
  quote?: ChatQuote
}
/** Types a kept message into the agent (`chat_send`, client → owner, controllers only). */
export interface ChatSend {
  t: 'chat_send'
  ref: string
  scope?: ChatScope
  to?: string
}
/** The kept chat replayed to a new viewer, oldest first; `more` says another frame follows. */
export interface ChatHistory {
  t: 'chat_history'
  scope: ChatScope
  messages: ChatMessage[]
  more?: boolean
}
/** One person on a run's chat: a viewer of any member, one row per name; `on` is the member they look at, when one. */
export interface ChatPerson {
  id: string
  name: string
  role: Role
  on?: string
}
/** Who is on a run's chat (`chat_roster`, owner → client), sent to every member's viewers as any member's roster changes; `count` counts everyone, `list` the first 32. */
export interface ChatRoster {
  t: 'chat_roster'
  scope: 'run'
  count: number
  list: ChatPerson[]
}

export const CloseCode = {
  Normal: 1000,
  GoingAway: 1001,
  ProtocolError: 4400,
  Unauthorized: 4401,
  Forbidden: 4403,
  NotFound: 4404,
  TooManyViewers: 4409,
  SessionEnded: 4410,
} as const

export type Role = 'view' | 'control'
export type AttentionState = '' | 'working' | 'needs_input' | 'done'

export type AttentionKind = '' | 'permission' | 'prompt' | 'done'

/** A quick-reply choice; `input` is exactly what a client sends as INPUT. */
export interface AttentionOption {
  label: string
  input: string
  /** The choice changes the session's permission mode (a dialog's "switch to auto mode"): no number key, never offered first (#288). */
  mode?: boolean
}

export interface Attention {
  state: AttentionState
  message?: string
  source?: string
  since?: string
  kind?: AttentionKind
  options?: AttentionOption[]
  /** The question's id, new with each change (absent with the empty state and from an older owner): a reply to it names it (`answer`). */
  id?: string
  /**
   * `terminal` for a question that keys typed in the session's terminal have answered, shown still while the program's dialog is
   * on the screen: no page offers it (`liveAttention`, #258).
   */
  answered?: 'terminal'
  /**
   * The path of the plan the question asks approval for (Claude Code's ExitPlanMode, #289), which the session reads and saves as the
   * folder's files: a page offers to open it. Absent from any other question, and from an older owner.
   */
  plan?: string
  /**
   * The durable decision the question mirrors (`dr-<n>`, at most MAX_DECISION_REF bytes; Program item DR1): it is answered only as
   * that decision, so a page offers it no reply, choice or number key (`decisionOf`), and an `answer` naming it is refused
   * (`decision`). Absent from every other question, and from an older owner.
   */
  decision?: string
}
export type TransportKind = 'ws' | 'webrtc' | 'relay'

/** One attached client, as listed in the `viewers` roster. */
export interface ViewerInfo {
  id: string
  name: string
  role: Role
  /** Label of the share link the viewer joined through, if any. */
  link?: string
  since: string
  lastInputAt?: string
  /** A connection for a run's chat alone (`hello.chatOnly`): left out of a session's `viewers`, on the run's roster. */
  quiet?: boolean
}

/** One line of a session's activity log (`activity` control message). */
export interface ActivityEntry {
  at: string
  type:
    | 'attention' | 'input' | 'join' | 'leave' | 'link' | 'status'
    | 'progress' | 'artifact' | 'handoff' | 'tool_use' | 'tool_denied' | 'error' | 'file'
  by?: string
  byName?: string
  message?: string
  /** `artifact`: where the result lives (≤ 2048 bytes). Stored as sent: link it only when it is http(s). */
  url?: string
  /** `handoff`: who the work goes to (≤ 40 characters). */
  to?: string
  /** `tool_use`, `tool_denied`, `error`, `file`: the tool involved (≤ 100 bytes). */
  tool?: string
  /** `file`: what the agent did to the file, and its path as the agent named it (≤ 1024 bytes). `input`: `typed` for a line Conductor typed into the session (a crew's prompt, a broadcast), absent for a reply to a question. */
  op?: 'read' | 'edit' | 'write' | 'delete' | 'typed'
  path?: string
  /** GET /api/events only: for an attention entry, the state it records. Absent from a session's own replay and from an older host's entries. */
  state?: 'needs_input' | 'working' | 'done'
}

/** Data of an `activity` event on GET /api/events: an activity entry and the session it belongs to. */
export type SessionActivity = ActivityEntry & { sessionId: string }

/** A browser an agent's links open in (#436): the catalog's `browser` (internal/catalog/browser.go). */
export interface AgentBrowser {
  /** chrome, edge, firefox, brave or safari; absent is the computer's default browser. */
  name?: 'chrome' | 'edge' | 'firefox' | 'brave' | 'safari'
  /** A Chromium profile directory (`Profile 2`), for chrome, edge and brave alone; at most 100 characters. */
  profile?: string
}

/**
 * Data of an `open` event on GET /api/events (#436, docs/protocol.md "Opening a link"): a link an agent opened (≤ 2048 bytes), the
 * browser its settings name, and whether it waits for a desktop app to claim it (`pending`) or is offered in the workbench because
 * none did (`offered`). At most 15,200 bytes of data.
 */
export interface OpenEvent {
  id: string
  sessionId: string
  url: string
  browser?: AgentBrowser
  state: 'pending' | 'offered'
}

/**
 * Data of a `decision` event on GET /api/events (Program item DR1, docs/protocol.md "Decisions"): a durable decision changed
 * (opened, answered, applied, superseded, cancelled, commented), sent after each commit, for a page to read it again with
 * `GET /api/decisions/{id}`. `task` is the asking task's, absent for a plan's gate. At most 256 bytes of data
 * (`MAX_DECISION_EVENT`).
 */
export interface DecisionEvent {
  id: string
  run: string
  task?: string
  state: 'open' | 'answered' | 'applied' | 'superseded' | 'cancelled' | 'expired'
  rev: number
}

/** The bound of a `decision` event's data, in bytes (internal/api, maxDecisionEvent). */
export const MAX_DECISION_EVENT = 256

/**
 * The decide grant (docs/protocol.md "Decisions", docs/plans/decide-grant.md; internal/proto/decide.go): a grant an identity
 * provider may give a principal. Yardmaster's admission judges what such a principal may read and answer of a decision given to
 * the grant; the routes that admit it, whose replies the types below are, are #847's, and no route serves them yet.
 */
export const GRANT_DECIDE = 'decide'

/** `GET /api/me`: who the credential is and the grants it holds here; at most MAX_ME_REPLY bytes. */
export interface MeReply {
  subject?: string
  name?: string
  grants: string[]
}
export const MAX_ME_REPLY = 1024

/** `GET /api/me/decisions`: the open decisions that admit the caller, newest first; `cut` when more may await it. */
export interface DecideList {
  decisions: DecideEntry[]
  cut?: boolean
}
export interface DecideEntry {
  id: string
  rev: number
}
export const MAX_DECIDE_LIST = 16 * 1024
export const MAX_DECIDE_LIST_ENTRIES = 200

/** One of a decision's options as a decide view gives it, at the index a response's `choice` names. */
export interface DecideOption {
  label: string
  tradeoffs?: string
}

/** One of a decide principal's own responses: never its words or its actor. */
export interface DecideReceipt {
  k: number
  responseId: string
  status: 'accepted' | 'refused'
  code?: string
  rev: number
  at: string
  choice?: number
}

/** The outcome of a decide principal's own answer. */
export type DecideOutcome = 'applied' | 'waiting' | 'not_used' | 'refused'

/**
 * A decide principal's read of a decision (`GET /api/decisions/{did}`): every field while it is open; once answered or final,
 * `id`, `rev`, `own` and `outcome` alone. At most MAX_DECIDE_VIEW bytes.
 */
export interface DecideView {
  id: string
  rev: number
  state?: 'open'
  type?: 'clarification' | 'approval' | 'assignment'
  question?: string
  context?: string
  options?: DecideOption[]
  recommendation?: number
  uncertainty?: string
  authorizes?: string
  createdAt?: string
  own: DecideReceipt[]
  outcome?: DecideOutcome
  /** The newest own response's code, with `outcome: 'refused'`. */
  outcomeCode?: string
}
export const MAX_DECIDE_VIEW = 32 * 1024

/** The respond route's reply to a decide principal, and its refusal (the server's fixed words of the code). */
export interface DecideReply {
  receipt: DecideReceipt
  decision?: DecideView
}
export interface DecideRefusal {
  error: { code: string; message: string }
  receipt?: DecideReceipt
}
export const MAX_DECIDE_REPLY = MAX_DECIDE_VIEW + 1024
export const MAX_DECIDE_REFUSAL_WORDS = 200

/**
 * Data of a `capacity` event on GET /api/events (Program item AC1, docs/protocol.md "Capacity"): a reading of an agent's usage on
 * an account was kept, for a page to read its view again with `GET /api/capacity?agent=&account=`. It names the key, the reading's
 * state and when it was read, never a label, a folder or a figure. At most 512 bytes of data (`MAX_CAPACITY_EVENT`).
 */
export interface CapacityEvent {
  agent: string
  account: string
  state: 'ok' | 'signed_out' | 'key_paid' | 'unqualified' | 'error' | 'never_read'
  readAt: string
}

/** The bound of a `capacity` event's data, in bytes (internal/api, maxCapacityEvent). */
export const MAX_CAPACITY_EVENT = 512

export interface ICEServer {
  urls: string[]
  username?: string
  credential?: string
}

export interface Welcome {
  t: 'welcome'
  proto: number
  sessionId: string
  role: Role
  subscriberId?: string
  viewerId?: string
  cols: number
  rows: number
  /** The owner sizes the session by one viewer (round 14): `sizedBy` is that viewer's subscriber id ("" or absent: nobody holds it). */
  sizer?: boolean
  sizedBy?: string
  status: string
  scrollbackBytes: number
  transport: TransportKind
  fileView: boolean
  /** This connection may edit files (control, on a session whose `fileEdit` allows it), and the machine has `nvim`: `nvim_open` may be sent (design round 12, F8). */
  fileEdit?: boolean
  nvim?: boolean
  /** This connection may edit but not through Neovim, which is kept for the owner's own windows: a link's controller edits with the page's own keys and saves (`nvim` is then absent). */
  nvimOwnerOnly?: boolean
  /** The owner's Neovim says when keys are done (`settled`) and names refused keys; absent from an older owner, whose acknowledgement stands for it. */
  nvimSettled?: boolean
  /** The owner takes `chat` and `chat_send`; absent from an older owner, which must be sent neither. */
  chat?: boolean
  /** The session is a run's member with a run chat: scope `run` posts and sends, and `chat_roster`. */
  runChat?: boolean
  /** The owner takes `answer` and refuses one whose question no longer waits; absent from an older owner, which is sent none (replies go as INPUT and `submit`). */
  answer?: boolean
  /** The owner answers a `submit` naming a `reqId` with `submit_ok` or an `error` naming it, and takes `unlessWaiting` (#322); absent from an older owner, which types the line and says nothing. */
  submitAck?: boolean
  /** This connection is a decision link's: it takes the lane's reads and `Decide` frames (see Decision links). Absent otherwise and from an older owner, which is sent none. */
  decisions?: boolean
  /** The session's working directory, from the owner itself, to a role that may read files (at most 4096 bytes; absent from an older owner): a guest's Files root. */
  cwd?: string
  iceServers?: ICEServer[]
  relayTimeoutMs?: number
  relayOnly?: boolean
}

export type ControlMessage =
  | Welcome
  | { t: 'ready' }
  /** `by` names the viewer that sizes the session now (absent: nobody holds the size) when the welcome said `sizer` (round 14). */
  | { t: 'resize'; cols: number; rows: number; by?: string }
  | { t: 'status'; status: string; exitCode?: number }
  | { t: 'attention'; state: AttentionState; message?: string; source?: string; kind?: AttentionKind; options?: AttentionOption[]; id?: string; answered?: 'terminal'; plan?: string; decision?: string }
  /** The owner took the answer `requestId`: its keys are written, or its line accepted. */
  | { t: 'answer_ok'; requestId: string }
  /** The owner entered the line of the submit `requestId` (#322): its text typed and its Enter written. */
  | { t: 'submit_ok'; requestId: string }
  | { t: 'viewers'; count: number; list?: ViewerInfo[] }
  | { t: 'activity'; at: string; type: ActivityEntry['type']; by?: string; byName?: string; message?: string; url?: string; to?: string; tool?: string; op?: ActivityEntry['op']; path?: string }
  | { t: 'error'; code: string; message: string; requestId?: string }
  | NvimEvent
  | { t: 'pong'; ts: number }
  | ChatMessage
  | ChatHistory
  | ChatRoster
  | DecideOkMsg
  | DecideRefusedMsg
  | DecisionsChangedMsg

export interface FileEntry {
  name: string
  dir: boolean
  size: number
}

/** One changed file of a `status` reply: the path from the tree's top, M A D R or ? (untracked), its lines against the base. */
export interface FileChange {
  path: string
  /** A rename's old path, in a `commit` reply. */
  from?: string
  status: 'M' | 'A' | 'D' | 'R' | '?'
  added?: number
  removed?: number
  binary?: boolean
}

/** One commit of a `log` or `commit` reply (design 4e). `at` is the committer's time, RFC 3339; `body` comes with a `commit` reply only. */
export interface CommitInfo {
  sha: string
  short: string
  subject: string
  body?: string
  author?: string
  at: string
  parent?: string
  parents?: number
}

/**
 * What a file request may ask for beyond a read: git `status` against `base` (HEAD when empty), `show` of the path at `rev`, `log` (the
 * commits on HEAD after `base`, or since the session started), `commit` (commit `rev`'s files against its first parent), `find`,
 * `touched`, or `plans` (the agent's plans outside the working directory, #289: the path ignored, answered with kind `plans`).
 */
export interface FileGetExtra {
  op?: 'status' | 'show' | 'log' | 'commit' | 'find' | 'touched' | 'plans'
  base?: string
  rev?: string
}

/** How a page reads a file (or asks git) over its terminal's connection. */
export type FileRequester = (path: string, stat?: boolean, extra?: FileGetExtra) => Promise<FileResponse>

export interface FileHeader {
  reqId: string
  path: string
  kind: 'file' | 'dir' | 'error' | 'status' | 'show' | 'log' | 'commit' | 'written' | 'find' | 'touched' | 'plans'
  /** A `find` reply: the files under `path` whose name holds the words, as paths from it (at most 200). */
  matches?: string[]
  /** A `touched` reply: the files the agent touched since the session started, one per file, the most recently touched first. */
  touched?: TouchedFile[]
  /**
   * A `file` reply's sha256 (hex) and mtime: what a save sends back to tell a file changed on disk since (F6); absent when the read was
   * cut. A `written` reply carries the saved file's; an `error` of code `changed_on_disk` the file's now, with the last file event on it.
   */
  sha256?: string
  mtime?: string
  by?: string
  tool?: string
  at?: string
  /** A `file` reply the owner takes no save of (one in a repository's .git): the editor shows it read only, Neovim does not open it. */
  readOnly?: boolean
  /** A `log` reply: the commits newest first, and the time they start from when no base was asked for. A `commit` reply: the commit (its files in `changes`). */
  commits?: CommitInfo[]
  since?: string
  commit?: CommitInfo
  /** A `status` reply: the branch checked out, the base's short id (empty when it does not resolve), the changes and their totals. */
  branch?: string
  base?: string
  rev?: string
  changes?: FileChange[]
  added?: number
  removed?: number
  size?: number
  truncated?: boolean
  binary?: boolean
  mime?: string
  exists: boolean
  entries?: FileEntry[]
  error?: { code: string; message: string }
}

/** One file of a `touched` reply (round 13, G1): the latest op, every op seen, how many touches, the latest's tool and agent, the first and latest times. */
export interface TouchedFile {
  path: string
  op: 'read' | 'edit' | 'write' | 'delete'
  ops?: Array<'read' | 'edit' | 'write' | 'delete'>
  count: number
  tool?: string
  by?: string
  first: string
  last: string
}

export interface FileResponse {
  header: FileHeader
  body: Uint8Array
}

const encoder = new TextEncoder()
const decoder = new TextDecoder()

export function encodeFrame(type: number, payload: Uint8Array): Uint8Array<ArrayBuffer> {
  const out = new Uint8Array(1 + payload.length)
  out[0] = type
  out.set(payload, 1)
  return out
}

export function encodeJSONFrame(type: number, value: unknown): Uint8Array<ArrayBuffer> {
  return encodeFrame(type, encoder.encode(JSON.stringify(value)))
}

export function encodeControl(value: Record<string, unknown>): Uint8Array<ArrayBuffer> {
  return encodeJSONFrame(FrameType.Control, value)
}

export function encodeInput(data: Uint8Array): Uint8Array<ArrayBuffer> {
  return encodeFrame(FrameType.Input, data)
}

export function encodeText(text: string): Uint8Array {
  return encoder.encode(text)
}

export interface Frame {
  type: number
  payload: Uint8Array
}

export function decodeFrame(data: ArrayBuffer | Uint8Array): Frame | null {
  const bytes = data instanceof Uint8Array ? data : new Uint8Array(data)
  if (bytes.length === 0) return null
  return { type: bytes[0]!, payload: bytes.subarray(1) }
}

export function parseJSON<T = Record<string, unknown>>(payload: Uint8Array): T | null {
  try {
    return JSON.parse(decoder.decode(payload)) as T
  } catch {
    return null
  }
}

export function decodeText(payload: Uint8Array): string {
  return decoder.decode(payload)
}

/** A save from the editor (design round 12, F6): the parts are at most MAX_WRITE_PART bytes, the file at most 1 MiB. */
export const MAX_WRITE_PART = 32 * 1024
export const MAX_WRITE_BYTES = 1024 * 1024
/** A part's JSON header is at most this many bytes (proto.MaxWriteHeader): past it the owner cannot read the part, nor answer the save. */
export const MAX_WRITE_HEADER = 4 * 1024
export interface FileWriteOptions {
  /** The sha256 of the file as it was read: the owner refuses with `changed_on_disk` when the file differs now. */
  baseSha256?: string
  /** Save anyway, over a file changed on disk. */
  force?: boolean
  /**
   * Called once when the save has had no answer for 20 s. It goes on waiting while the connection lives (the owner answers every
   * save, and a relayed one may be slowed on its way), so the page can say it is still on its way.
   */
  onSlow?: () => void
}
/** How a page saves a file over its terminal's connection: the reply is the FILE frame, kind `written` or `error`. */
export type FileWriter = (path: string, data: Uint8Array, opts?: FileWriteOptions) => Promise<FileResponse>

/** The parts of a save: [offset, end) ranges of at most size bytes, one (empty) part for an empty file. */
export function writeParts(length: number, size = MAX_WRITE_PART): Array<[number, number]> {
  const out: Array<[number, number]> = []
  for (let off = 0; off < length || off === 0; off += size) {
    out.push([off, Math.min(off + size, length)])
    if (off + size >= length) break
  }
  return out
}

/** One part of a save: [type][8-byte reqId][uint32 headerLen][JSON header][bytes], the FILE frame's layout. */
export function encodeFileWrite(header: { reqId: string; path: string; offset: number; total: number; baseSha256?: string; force?: boolean }, part: Uint8Array): Uint8Array<ArrayBuffer> {
  const hb = encoder.encode(JSON.stringify(header))
  const out = new Uint8Array(1 + 8 + 4 + hb.length + part.length)
  out[0] = FrameType.FileWrite
  out.set(encoder.encode(header.reqId).subarray(0, 8), 1)
  new DataView(out.buffer).setUint32(9, hb.length)
  out.set(hb, 13)
  out.set(part, 13 + hb.length)
  return out
}

// FILE payload: [8-byte reqId][uint32 headerLen][JSON header][bytes]
export function decodeFile(payload: Uint8Array): FileResponse | null {
  if (payload.length < 12) return null
  const view = new DataView(payload.buffer, payload.byteOffset, payload.byteLength)
  const headerLen = view.getUint32(8)
  if (12 + headerLen > payload.length) return null
  const header = parseJSON<FileHeader>(payload.subarray(12, 12 + headerLen))
  if (!header) return null
  return { header, body: payload.subarray(12 + headerLen) }
}

// Reassembles Chunk frames ([uint16 msgId][uint32 total][uint32 offset][data])
// into the original frame bytes.
export class ChunkAssembler {
  private buffers = new Map<number, { data: Uint8Array; received: number }>()

  push(payload: Uint8Array): Uint8Array | null {
    if (payload.length < 10) return null
    const view = new DataView(payload.buffer, payload.byteOffset, payload.byteLength)
    const id = view.getUint16(0)
    const total = view.getUint32(2)
    const offset = view.getUint32(6)
    const data = payload.subarray(10)
    if (offset + data.length > total || total > 4 * 1024 * 1024) return null
    let entry = this.buffers.get(id)
    if (!entry) {
      entry = { data: new Uint8Array(total), received: 0 }
      this.buffers.set(id, entry)
    }
    entry.data.set(data, offset)
    entry.received += data.length
    if (entry.received >= total) {
      this.buffers.delete(id)
      return entry.data
    }
    return null
  }
}

/**
 * Whether a close says the session ended: 4410 `session ended`, after its `session_ended` error, which a viewer attached through a
 * link is closed with as the session ends (its links end with it), its last output first, on a server session and a hosted one
 * alike; not a host that went away (4410 `host disconnected`), nor a link revoked or expired (4403).
 */
export function closedByTheEnd(code: number, reason: string): boolean {
  return code === CloseCode.SessionEnded && reason === 'session ended'
}

export function closeReason(code: number, reason: string): string {
  switch (code) {
    case CloseCode.Normal:
      return 'Connection closed'
    case CloseCode.GoingAway:
      return 'Server is restarting'
    case CloseCode.Unauthorized:
      return 'Not authorized for this session'
    case CloseCode.Forbidden:
      // A link that reached its expiry is closed with the same code as a revoked one; the reason (and the `expired` error before it) tells them apart.
      // An identity provider's principal the provider no longer admits is closed so too, with no link to name (`access revoked`).
      if (reason === 'access revoked') return 'Your access to this session was revoked'
      return reason === 'link expired' ? 'This share link expired' : 'This share link was revoked'
    case CloseCode.NotFound:
      return 'Session not found'
    case CloseCode.TooManyViewers:
      return 'Too many viewers on this session'
    case CloseCode.SessionEnded:
      return reason || 'Session ended'
    case CloseCode.ProtocolError:
      return 'Protocol error'
    case 1006:
      return 'Connection lost'
  }
  return reason || `Connection closed (${code})`
}

/**
 * Host control connection (`/ws/host`, docs/protocol.md): a host asks the
 * server for a share link to its session and the server answers, or refuses
 * with an `error` naming the request. The web never sends these; they are
 * mirrored here with their limits, as every message is.
 */
export interface HostLinkMessage {
  t: 'link'
  /** ≤ 32 bytes. */
  requestId: string
  role: 'view' | 'control'
  /** ≤ 86400 (a day). */
  ttlSeconds?: number
  /** ≤ 120 bytes. */
  label?: string
}

export interface HostLinkCreatedMessage {
  t: 'link_created'
  requestId: string
  url: string
  /** `conductor://<host>/join/<token>`, which the desktop app opens itself. */
  invite: string
  linkId: string
  role: 'view' | 'control'
  label?: string
  expiresAt?: string
}

export interface HostLinkRevokeMessage {
  t: 'link_revoke'
  /** ≤ 32 bytes. */
  requestId: string
  /** ≤ 64 bytes: a linkId from link_created. */
  linkId: string
}

export interface HostLinkRevokedMessage {
  t: 'link_revoked'
  requestId: string
  linkId: string
}

/**
 * server → host: a link no longer opens what the connection carries. ended (a switchyard's open admission past its bound) says
 * the link itself opens nothing any more, so the publisher stops listing it; absent from an older switchyard and every other end.
 */
export interface HostLinkGoneMessage {
  t: 'link_gone'
  /** ≤ 64 bytes. */
  linkId: string
  ended?: boolean
}

/** A host connection may ask for this many links a minute (a revoke is never counted). */
export const HOST_LINK_REQUESTS_PER_MINUTE = 5
/** A link id in a revoke is at most this long. */
export const HOST_MAX_LINK_ID = 64

/** What a host's register says of the host and its session that fixes the session's id across a server restart (docs/protocol.md). */
export interface HostIdentity {
  /** A secret of the host process: with the session's local id, the session's id on the server. Never shown or logged. */
  instance?: string
  /** The session's id on the host. */
  localId?: string
}
/** The bounds of HostIdentity's fields. */
export const HOST_MAX_INSTANCE = 64
export const HOST_MAX_LOCAL_ID = 64

/**
 * Routed answers (the decision seam, docs/protocol.md, Host control connection): what a host's
 * register says when it takes `answer_as` and sends `kept`. `conductor host` says it; a server
 * publishing to a switchyard never does; an older host leaves it out.
 */
export interface HostAnswerAsInfo {
  answerAs?: boolean
}

/** host → server, on `attention`, from a host that said answerAs: the questions its session keeps without showing them. */
export interface HostAttentionKept {
  /** At most HOST_MAX_KEPT question ids (1–32 bytes of printable ASCII each); a message past that is dropped whole. */
  kept?: string[]
}
export const HOST_MAX_KEPT = 2

/**
 * server → host, only to a host whose register said answerAs, never from a switchyard: an answer
 * a decision router or a provider holds for the question the host's session shows, applied there
 * under the question's claim, the first answer winning. The web never sends it.
 */
export interface HostAnswerAsMessage {
  t: 'answer_as'
  /** The question's id: 1–32 bytes of printable ASCII. */
  question: string
  /** Exactly one of input (a choice's keys, at most 48 bytes, one of the question's own) and text (a line, at most 4096 bytes). */
  input?: string
  text?: string
  /** Who answered: 1–40 characters, one line. */
  byName: string
  /** At most HOST_MAX_ANSWER_AS_SUBJECT bytes of printable ASCII, no space. */
  bySubject?: string
}
export const HOST_MAX_ANSWER_AS_SUBJECT = 48

/**
 * What a host reports of its session for its record (docs/protocol.md, Host control connection; HS1 PR-12), beside the register's
 * session fields: its Runtime (`where`, ext.Where's slug), its agent's version, and, read before its process starts and only on a
 * native Runtime, its working tree's top, its repository's common git directory and its start commit. Each optional: an older host
 * sends none, and its record says "not reported by its host". A register past a bound is refused. The web never sends it.
 */
export interface HostReport {
  where?: string
  agentVersion?: string
  repo?: string
  repoCommon?: string
  startCommit?: string
}
export const HOST_MAX_REPORT_WHERE = 32
export const HOST_MAX_AGENT_VERSION = 64
export const HOST_MAX_REPORT_PATH = 4096
/** A session_info message (below) is at most this many bytes. */
export const HOST_MAX_SESSION_INFO = 2048

/** A full commit id: 40 (SHA-1) or 64 (SHA-256) lowercase hex digits. */
export function validCommit(s: string): boolean {
  return /^(?:[0-9a-f]{40}|[0-9a-f]{64})$/.test(s)
}

/** Whether a HostReport is within its bounds, as the server's hub holds it (proto.HostReport.Check): lengths in UTF-8 bytes. */
export function hostReportValid(r: HostReport): boolean {
  const bytes = (s: string) => new TextEncoder().encode(s).length
  // Made of what a reported field may hold (proto's oneLine): letters, numbers, punctuation, symbols and the space, each followed by
  // any combining marks, so none first; nothing a renderer ignores by default (a variation selector, the grapheme joiner). The
  // server's verdict is the one that counts: this mirror is for the tests, and where its Unicode tables and Go's differ, Go's rule.
  const oneLine = (s: string) =>
    /^(?:[\p{L}\p{N}\p{P}\p{S} ]\p{M}*)*$/u.test(s) && !/[\p{Default_Ignorable_Code_Point}\p{Variation_Selector}]/u.test(s)
  const path = (p?: string) => !p || (bytes(p) <= HOST_MAX_REPORT_PATH && oneLine(p) && /^(?:\/|\\\\|[A-Za-z]:[\\/])/.test(p))
  if (r.where && !new RegExp(`^[a-z][a-z0-9-]{0,${HOST_MAX_REPORT_WHERE - 1}}$`).test(r.where)) return false
  if (r.agentVersion && (bytes(r.agentVersion) > HOST_MAX_AGENT_VERSION || !oneLine(r.agentVersion))) return false
  return path(r.repo) && path(r.repoCommon) && (!r.startCommit || validCommit(r.startCommit))
}

/**
 * host → server, only to a server whose registered said sessionInfo, never to a switchyard: what the host read of its session once
 * its process ended (`endCommit`, a full commit id, or none), sent before the status that ends it; at most HOST_MAX_SESSION_INFO
 * bytes, else dropped. The web never sends it.
 */
export interface HostSessionInfoMessage {
  t: 'session_info'
  sessionId?: string
  endCommit?: string
}

/** The part of registered that says the server takes session_info; absent from an older server, a switchyard and a desk's. */
export interface HostRegisteredSessionInfo {
  sessionInfo?: boolean
}

/** The part of registered that says which links the server still holds for the session; absent from an older server. */
export interface HostRegisteredLinks {
  links?: string[]
}

/**
 * The part of registered that says the switchyard carries a decision link's lane (docs/protocol.md, Decision links, Through
 * a switchyard): it relays DECISION and DECIDE, keeps run desks and serves the join page's Decide tab. A publisher mints a
 * decision link only at one that says so; absent from an older switchyard. The web never sees it.
 */
export interface HostRegisteredDecisions {
  decisions?: boolean
}

/**
 * The part of registered that says the switchyard rechecks the connection's credential and takes host_token on it
 * (docs/protocol.md, Host control connection): only on a connection admitted through an identity provider's principal;
 * absent from an older switchyard. The web never sees it.
 */
export interface HostRegisteredRenews {
  renews?: boolean
}

/**
 * host → switchyard, after a registered that said renews: the credential that replaces the one the connection was admitted
 * with, at most one per 30 s step and only when it changed. The token is 1 to HOST_MAX_TOKEN bytes of token68 (A-Z a-z 0-9
 * - . _ ~ + / =), the message at most HOST_MAX_TOKEN_MESSAGE bytes; anything but its one success closes the connection 4403
 * (access revoked). The web never sends it.
 */
export interface HostTokenMsg {
  t: 'host_token'
  token: string
}
export const HOST_MAX_TOKEN = 2048
export const HOST_MAX_TOKEN_MESSAGE = 2560

/**
 * host → switchyard, the first message of a run's desk's connection in place of register (docs/protocol.md, A run's desk):
 * a connection with no session that carries a run decision link's lane, so the run's decisions are reached with no member
 * alive. host.instance is required; localId is hostDeskLocalId(run.id); the whole message is at most 64 KiB (a host
 * message's bound). The web never sends it.
 */
export interface HostRegisterDeskMessage {
  t: 'register_desk'
  proto: number
  host: { name: string; version: string; instance: string }
  localId: string
  run: { id: string; name: string }
  resume?: { sessionId: string; secret: string }
}
/** A desk's id where a message carries it (a run group's desk, a resume): a hosted session's id. */
export const HOST_MAX_DESK_ID = 64
/** The local id of the desk of the run runId. */
export function hostDeskLocalId(runId: string): string {
  return `desk:${runId}`
}

/**
 * The part of registered that says what the hosted session watches on its screen (#293): the catalog's recipe for the agent the
 * host registered, as a server session of it watches. Absent when the catalog has no such agent or nothing to watch, from an older
 * server, past HOST_MAX_RECIPE bytes encoded, and when registered with it would be past a host message (64 KiB); a host refuses one
 * past the bounds below. confirmSubmit is set on the hosted session, but no submission on a host asks for the second Enter yet.
 * The workbench never sees it.
 */
export interface HostRegisteredRecipe {
  recipe?: HostRecipe
}
export interface HostRecipe {
  trustPrompt?: string
  trustAnswers?: HostRecipeAnswer[]
  questions?: HostRecipeQuestion[]
  workingTitle?: string
  confirmSubmit?: boolean
}
export interface HostRecipeAnswer {
  label: string
  input: string
}
export interface HostRecipeQuestion {
  prompt: string
  words?: string
  anytime?: boolean
  answers?: HostRecipeAnswer[]
  answersFromScreen?: boolean
  modeAnswers?: string
  typedAnswers?: string
}
/** The recipe's bounds: questions, answers to each (labels in characters once trimmed, as the catalog counts; keys in bytes), a pattern's bytes, the encoding's bytes. */
export const HOST_MAX_RECIPE_QUESTIONS = 8
export const HOST_MAX_RECIPE_ANSWERS = 6
export const HOST_MAX_RECIPE_ANSWER_LABEL = 60
export const HOST_MAX_RECIPE_ANSWER_INPUT = 48
export const HOST_MAX_RECIPE_PATTERN = 200
export const HOST_MAX_RECIPE = 24 << 10

/**
 * A crew run as a switchyard shows it: what a host sends with link_run and link_run_update. desk is the run's desk there
 * (at most HOST_MAX_DESK_ID bytes), which the run's links open for its decisions; with one, members may be empty.
 */
export interface HostRunGroup {
  id: string
  name: string
  members: Array<{ name: string; sessionId?: string; agentId: string; status: 'pending' | 'starting' | 'running' | 'ended' }>
  desk?: string
}
/** host → server: one link to the sessions of a run's members; answered by link_created with runId. */
export interface HostRunLinkMessage {
  t: 'link_run'
  requestId: string
  role: 'view' | 'control'
  ttlSeconds?: number
  label?: string
  run: HostRunGroup
}
/** host → server: the run's members now; answered by link_run_updated, or error not_found. */
export interface HostRunLinkUpdateMessage {
  t: 'link_run_update'
  requestId: string
  run: HostRunGroup
}
/** server → host: how many of the run's links follow the new members. */
export interface HostRunLinkUpdatedMessage {
  t: 'link_run_updated'
  requestId: string
  runId: string
  links: number
}
/** The bounds of a run group, and how many updates a connection may send a minute. */
export const HOST_MAX_RUN_LINK_MEMBERS = 32
export const HOST_MAX_RUN_ID = 64
export const HOST_MAX_RUN_NAME = 120
export const HOST_MAX_RUN_MEMBER_NAME = 40
export const HOST_RUN_LINK_UPDATES_PER_MINUTE = 30

/**
 * The editor's Neovim as a page offers it to the editor (design round 12, F8):
 * what the welcome allows here, the calls, and the events by editor id.
 */
export interface NvimBridge {
  /** The welcome arrived, the machine has nvim, and this connection may edit. `ownerOnly` when editing is allowed but Neovim is kept for the owner's own window. */
  offer: { welcome: boolean; nvim: boolean; fileEdit: boolean; ownerOnly?: boolean; machine?: string; settled?: boolean; connected?: boolean }
  open(path: string): Promise<NvimEvent>
  /** Keys for editor id; seq, when given, comes back as the `ack` of the cursor once Neovim has handled them (round 13, G5). */
  input(id: string, keys: string, seq?: number): void
  /** Ends editor id; `discard` drops its changes not written and its swap file with them (the person said Don't save). */
  close(id: string, discard?: boolean): void
  /** Answers the swap file the editor found (round 13, G3). */
  swap(id: string, choice: NvimSwapChoice): void
  subscribe(cb: (ev: NvimEvent) => void): () => void
  /** The Neovims of tabs not in front that hold changes not written, kept for when their tab is in front again. */
  holds: NvimHolds
}

/** The editor's Neovim bridge (design round 12, F8): what a viewer sends. */
export const MAX_NVIM_KEYS = 256
export interface NvimOpen {
  t: 'nvim_open'
  reqId: string
  path: string
}
export interface NvimInput {
  t: 'nvim_input'
  id: string
  keys: string
  /** The page's number for these keys, rising (round 13, G5). */
  seq?: number
}
export interface NvimClose {
  t: 'nvim_close'
  id: string
  /** Drop the changes not written, and the swap file with them (else it stays for recovery, as when the connection ends). */
  discard?: boolean
}
/** The answers to a swap file (round 13, G3): edit anyway; recover its text; delete it and edit (the last two once its writer is gone). */
export type NvimSwapChoice = 'edit' | 'recover' | 'delete'
export interface NvimSwap {
  t: 'nvim_swap'
  id: string
  choice: NvimSwapChoice
}
/** Another editor's swap file for the file opened: the file opened read-only. */
export interface NvimSwapInfo {
  file: string
  pid?: number
  /** The process that wrote it still runs on the session's machine. */
  running: boolean
  user?: string
  host?: string
  /** It holds changes not written to the file. */
  modified?: boolean
  mtime?: string
}
export type NvimEventKind = 'opened' | 'lines' | 'cursor' | 'mode' | 'cmdline' | 'message' | 'written' | 'closed' | 'error' | 'swap' | 'modified' | 'settled'
/**
 * What the editor reports. `kind` says which fields are set: `opened` (reqId on the first, path), `lines` (the buffer lines [first, last) become
 * `lines`, last -1 to the end, `truncated` when a line was cut), `cursor` (1-based line and col, the mode, the other end of a visual selection),
 * `mode`, `cmdline` (show, content, pos, prompt), `message` (text, messageKind), `written` (path), `closed` (reason), `error` (reqId, code, message),
 * `modified` (modified: the buffer holds changes not written, or no longer does), `settled` (ack: Neovim has done the keys numbered up to it, nothing
 * pending, every change they made sent; a cursor's ack can come before those changes).
 */
export interface NvimEvent {
  t: 'nvim_event'
  id?: string
  kind: NvimEventKind
  reqId?: string
  path?: string
  first?: number
  last?: number
  lines?: string[]
  truncated?: boolean
  line?: number
  col?: number
  mode?: string
  visualLine?: number
  visualCol?: number
  /** A `swap` event's swap file. */
  swap?: NvimSwapInfo
  /** On a `cursor` event: Neovim has handled the keys of this seq and every one before, their line changes sent already (round 13, G5). */
  ack?: number
  /** On a `modified` event: the buffer holds changes not written. */
  modified?: boolean
  show?: boolean
  content?: string
  pos?: number
  prompt?: string
  text?: string
  messageKind?: string
  reason?: string
  code?: string
  message?: string
}

/**
 * Lines of a file a chat message is about (design round 12, F7): the path (relative to the working directory when inside it), the
 * 1-based range, the lines as the person saw them (at most MAX_QUOTE_LINES of at most MAX_QUOTE_LINE bytes), `cut` when some were left out.
 */
export interface ChatQuote {
  path: string
  from: number
  to: number
  lines: string[]
  cut?: boolean
}
export const MAX_QUOTE_LINES = 12
export const MAX_QUOTE_LINE = 200
export const MAX_QUOTE_PATH = 512

/*
 * Decision links (docs/protocol.md, Decision links; internal/proto/decisions.go): over the connection a decision link opens (a
 * session's viewer, or a run's desk, `/ws/runs/{run}/desk`), the holder lists the decisions in the link's scope, reads one, unfolds
 * its evidence, reads the four steps of what it did and acts on one. Reads are controls; an action is a `Decide` frame. Every read and
 * every decide is answered by exactly one reply naming its `reqId`: a `Decision` frame, `decide_ok` or `decide_refused`.
 */

/** A `Decision` frame's payload and a `Decide` frame's, in bytes (proto.MaxDecision, MaxDecide). */
export const MAX_DECISION = 128 * 1024
export const MAX_DECIDE = 32 * 1024
/** The lane's bounds, in bytes (proto.MaxDecisionReqID, …): ids, refs, cursors, receipts, an action's name and revision, a rationale encoded. */
export const MAX_DECISION_REQ_ID = 32
export const MAX_LANE_REF = 512
export const MAX_DECISION_CURSOR = 600
export const MAX_DECISION_RECEIPT = 512
export const MAX_DECISION_RECORD_REV = 20
export const MAX_DECISION_RECORD_AFTER = 64
export const MAX_DECIDE_ACTION = 32
export const MAX_DECIDE_REVISION = 64
export const MAX_RATIONALE = 2048
export const MAX_DECIDE_CHOICE = 5
/** How many summaries a page holds, and how many decides and reads one connection may have under way. */
export const DECISION_PAGE = 25
export const MAX_DECIDE_IN_FLIGHT = 4
export const MAX_DECISION_READS_IN_FLIGHT = 4

/** The codes a `decide_refused` carries; an operation may give one of its own besides. */
export const DecideCode = {
  StaleRevision: 'stale_revision',
  NotInScope: 'not_in_scope',
  NotDecisionLink: 'not_decision_link',
  NotFound: 'not_found',
  QuestionAnswered: 'question_answered',
  TooManyRequests: 'too_many_requests',
  Unsupported: 'unsupported',
  Invalid: 'invalid',
  RecordMoved: 'record_moved',
  Revoked: 'revoked',
  Expired: 'expired',
  /** The opId was taken before for another request (another item, action or argument): nothing was done. */
  OpConflict: 'op_conflict',
} as const

export interface DecisionsListMsg { t: 'decisions_list'; reqId: string; after?: string }
export interface DecisionReadMsg { t: 'decision_read'; reqId: string; ref: string }
/** The four steps of an action taken through this link: `attempt` is its identity (its decide's `opId`), apart from `reqId`. */
export interface DecisionTrackMsg { t: 'decision_track'; reqId: string; ref: string; receipt: string; attempt: string }
/** One part of a DR1 record; answered `unsupported` until DR1's records are items. */
export interface DecisionRecordMsg { t: 'decision_record'; reqId: string; ref: string; part: 'head' | 'responses' | 'history'; rev?: string; after?: string }
/** A bounded read of one evidence entry of the item `ref`, named by the entry's own ref. */
export interface DecisionEvidenceMsg { t: 'decision_evidence'; reqId: string; ref: string; evidence: string }
export type DecisionRequest = DecisionsListMsg | DecisionReadMsg | DecisionTrackMsg | DecisionRecordMsg | DecisionEvidenceMsg

/** The scope's items changed: list again, and track again what was acted on. */
export interface DecisionsChangedMsg { t: 'decisions_changed'; seq: number }
/** The owner took a decide: its steps are read by `ref` and `receipt`; `repeat` when its `opId` was taken before. */
export interface DecideOkMsg { t: 'decide_ok'; reqId: string; ref: string; receipt: string; repeat?: boolean }
/** A refused read or decide: `rev` a record's new revision with `record_moved`; `ref` and `receipt` a refusal kept as a receipt. */
export interface DecideRefusedMsg { t: 'decide_refused'; reqId: string; code: string; message: string; rev?: string; ref?: string; receipt?: string }

/** One action on an item (a `Decide` frame), `revision` the action's `expect`; the actor is the link, never a field. */
export interface DecideMsg {
  reqId: string
  ref: string
  action: string
  revision: string
  ticketRev?: number
  input?: string
  choice?: number
  text?: string
  rationale?: string
  opId: string
}

/** A `Decision` frame's payload: the reply to the read `reqId` names, one of its fields set. */
export interface DecisionReply {
  reqId: string
  list?: unknown
  item?: unknown
  track?: unknown
  evidence?: unknown
  record?: unknown
}

const OP_ID = /^[A-Za-z0-9._~-]{1,32}$/

/** UTF-8 bytes of s. */
function byteLength(s: string): number {
  return encoder.encode(s).length
}

/** Printable ASCII, no space (proto's `printable`). */
function printableASCII(s: string): boolean {
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i)
    if (c <= 0x20 || c >= 0x7f) return false
  }
  return true
}

/** How many bytes s takes as Go's encoder writes it in JSON, quotes left out: `<`, `>`, `&`, a control character (but `\\b`, `\\f`, `\\n`, `\\r`, `\\t`), U+2028 and U+2029 in six. */
export function goJSONLength(s: string): number {
  let n = 0
  for (const ch of s) {
    const c = ch.codePointAt(0)!
    if (ch === '"' || ch === '\\' || ch === '\n' || ch === '\r' || ch === '\t' || ch === '\b' || ch === '\f') n += 2
    else if (c < 0x20 || ch === '<' || ch === '>' || ch === '&' || c === 0x2028 || c === 0x2029) n += 6
    else n += byteLength(ch)
  }
  return n
}

function validReqId(id: string): boolean {
  return id.length > 0 && byteLength(id) <= MAX_DECISION_REQ_ID && printableASCII(id)
}

function validRef(ref: string): boolean {
  return ref.length > 0 && byteLength(ref) <= MAX_LANE_REF
}

/** Whether a lane request is within its bounds, as the owner checks it (proto's Valid). */
export function decisionRequestValid(m: DecisionRequest): boolean {
  if (!validReqId(m.reqId)) return false
  switch (m.t) {
    case 'decisions_list':
      return byteLength(m.after ?? '') <= MAX_DECISION_CURSOR
    case 'decision_read':
      return validRef(m.ref)
    case 'decision_track':
      return validRef(m.ref) && m.receipt.length > 0 && byteLength(m.receipt) <= MAX_DECISION_RECEIPT && OP_ID.test(m.attempt)
    case 'decision_record': {
      const rev = m.rev ?? ''
      if (!validRef(m.ref) || byteLength(m.after ?? '') > MAX_DECISION_RECORD_AFTER || !printableASCII(m.after ?? '')) return false
      if (!['head', 'responses', 'history'].includes(m.part) || (m.part !== 'head' && rev === '')) return false
      return rev.length <= MAX_DECISION_RECORD_REV && /^[0-9]*$/.test(rev)
    }
    case 'decision_evidence':
      return validRef(m.ref) && validRef(m.evidence)
  }
  return false
}

/** Whether a decide is within its bounds, as the owner checks it (proto.Decide.Valid): which fields an action takes is the owner's. */
export function decideValid(m: DecideMsg): boolean {
  if (!validReqId(m.reqId) || !OP_ID.test(m.opId) || !validRef(m.ref)) return false
  if (m.action.length === 0 || byteLength(m.action) > MAX_DECIDE_ACTION || !printableASCII(m.action)) return false
  if (byteLength(m.revision) > MAX_DECIDE_REVISION || (m.ticketRev ?? 0) < 0) return false
  if (byteLength(m.input ?? '') > MAX_ANSWER_INPUT || byteLength(m.text ?? '') > MAX_SUBMIT) return false
  if (m.choice !== undefined && (!Number.isInteger(m.choice) || m.choice < 0 || m.choice > MAX_DECIDE_CHOICE)) return false
  return goJSONLength(m.rationale ?? '') <= MAX_RATIONALE
}

/** A `Decide` frame for m; null for one past its bounds or past MAX_DECIDE encoded. */
export function encodeDecide(m: DecideMsg): Uint8Array<ArrayBuffer> | null {
  if (!decideValid(m)) return null
  const frame = encodeJSONFrame(FrameType.Decide, m)
  return frame.length - 1 > MAX_DECIDE ? null : frame
}

/** A `Decision` frame's reply; null for one that does not parse, names no request or passes MAX_DECISION. */
export function decodeDecision(payload: Uint8Array): DecisionReply | null {
  if (payload.length > MAX_DECISION) return null
  const d = parseJSON<DecisionReply>(payload)
  return d && typeof d.reqId === 'string' && d.reqId !== '' ? d : null
}

// The preview bridge's messages (internal/proto/preview.go; docs/protocol.md, Preview bridge): what the workbench and the bridge,
// a script in a previewed page, say through postMessage. Both parsers are held to internal/proto/testdata/preview-messages.json.

/** Every message's `conductor` field, and the bridge's version a `ready` names. */
export const PREVIEW_MARK = 'preview/1'
export const PREVIEW_VERSION = 1

/** A message to the bridge, one to the workbench and the element report one carries, as previewMeasure counts them. */
export const MAX_PREVIEW_TO_BRIDGE = 4 * 1024
export const MAX_PREVIEW_TO_WORKBENCH = 64 * 1024
export const MAX_ELEMENT_REPORT = 32 * 1024
/** A describe's selector, a page's URL, its title (in characters), the user agent and the revision a page declares. */
export const MAX_PREVIEW_SELECTOR = 1024
export const MAX_PREVIEW_URL = 2048
export const MAX_PREVIEW_TITLE = 200
export const MAX_PREVIEW_USER_AGENT = 512
export const MAX_PREVIEW_REVISION = 128

/** The element report's fields, each cut to its bound by the bridge (proto.MaxElementName and the rest), in measured bytes. */
export const ELEMENT_BOUNDS = {
  name: 1024,
  text: 1024,
  selector: 1024,
  attributes: 32,
  attributeName: 64,
  attributeValue: 512,
  html: 12 * 1024,
  neighbourhood: 24 * 1024,
  ancestors: 12,
  ancestor: 512,
  styles: 64,
  styleName: 64,
  styleValue: 256,
  hints: 8,
  hintFile: 1024,
  hintComponent: 256,
} as const

/** How a page says what drew an element: a data-conductor-source attribute, Vue's, Svelte's, React's dev hooks. */
export const PREVIEW_HINT_KINDS = ['attribute', 'vue', 'svelte', 'react'] as const
export type PreviewHintKind = (typeof PREVIEW_HINT_KINDS)[number]

/** The steps the bridge cuts a report by to fit MAX_ELEMENT_REPORT, in their order: a report's cut is always a prefix of them. */
export const ELEMENT_CUTS = ['neighbourhood', 'html', 'styles', 'attributes', 'tag', 'hints', 'ancestors', 'attribute-count', 'names', 'style-count'] as const
export type ElementCut = (typeof ELEMENT_CUTS)[number]

const PREVIEW_NONCE = /^[A-Za-z0-9_-]{16,64}$/
const PREVIEW_REVISION = /^[0-9a-f]{4,64}(?:\+[0-9a-f]{4,64})?$/
const MAX_PREVIEW_INT = Number.MAX_SAFE_INTEGER

export interface PreviewViewport {
  width: number
  height: number
  dpr: number
  scrollX: number
  scrollY: number
  colorScheme: 'light' | 'dark'
  userAgent: string
}

export interface ElementReport {
  tag: string
  id: string
  classes: string
  role: string
  name: string
  text: string
  selector: string
  box: { x: number; y: number; width: number; height: number }
  attributes: Array<[string, string]>
  state: { disabled: boolean; hidden: boolean; ariaDisabled: string; ariaExpanded: string; pointerEvents: string; inert: boolean; hovered: boolean; motion: number }
  hit: { self: boolean; selector: string; tag: string }
  html: string
  neighbourhood: string
  ancestors: Array<{ tag: string; id: string; classes: string; role: string }>
  styles: Array<[string, string]>
  hints: Array<{ how: PreviewHintKind; file: string; line: number; column: number; component: string }>
  cut: ElementCut[]
}

/** What every answer carries of the page, read at that moment. */
export interface PreviewPage {
  url: string
  title: string
  viewport: PreviewViewport
  epoch: number
  revision: string
}

interface Marked {
  conductor: typeof PREVIEW_MARK
  nonce: string
}

export type PreviewToBridge = Marked & ({ t: 'hello' | 'select' | 'look' | 'cancel' } | { t: 'describe'; selector: string })
export type PreviewToWorkbench = Marked &
  (
    | ({ t: 'ready'; version: number; hints: PreviewHintKind[] } & PreviewPage)
    | ({ t: 'selected'; element: ElementReport } & PreviewPage)
    | ({ t: 'described'; element?: ElementReport; missing?: true } & PreviewPage)
    | ({ t: 'looked' } & PreviewPage)
    | { t: 'cancelled' }
  )
export type PreviewMessage = PreviewToBridge | PreviewToWorkbench
/** The way a message travels: to the bridge (the workbench's) or to the workbench (the bridge's answers). */
export type PreviewDirection = 'bridge' | 'workbench'

const LONE_SURROGATE = /[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/
const LONE_SURROGATES = new RegExp(LONE_SURROGATE.source, 'g')

/** s with every lone surrogate made U+FFFD, as Go's encoder writes invalid UTF-8 (String.prototype.toWellFormed). */
export function wellFormed(s: string): string {
  return s.replace(LONE_SURROGATES, '�')
}

/** How deep a message's values go: the message, its element, a list of the element's, an item of that list. */
const PREVIEW_DEPTH = 4
/** The most members an object of a message has: an element report's. */
const PREVIEW_MEMBERS = 17

/**
 * How many own keys v has, counted as they are met and no further than one past most: the parser inspects at most most + 1 of them.
 * Enumerating them costs the engine time in proportion to v, which the browser has already spent, several times over, deserialising it.
 */
function keyCount(v: object, most: number): number {
  let n = 0
  for (const k in v) if (Object.hasOwn(v, k) && ++n > most) break
  return n
}

type Obj = Record<string, unknown>

/** Whether v is a plain object: not an array, a date, a map or any other kind a structured clone carries. */
function plain(v: unknown): v is Obj {
  if (typeof v !== 'object' || v === null || Array.isArray(v)) return false
  const p = Object.getPrototypeOf(v)
  return p === Object.prototype || p === null
}

/** Whether v is an array with every index present and no member beside them, its length judged before its keys are counted. */
function dense(v: unknown): v is unknown[] {
  return Array.isArray(v) && Object.getPrototypeOf(v) === Array.prototype && keyCount(v, v.length) === v.length
}

/**
 * v's size as the bridge and the server count it (proto.PreviewMeasure): the UTF-8 length of its JSON as Go writes it with HTML
 * escaping off, U+2028 and U+2029 in six bytes each, a negative zero as 0. A string's measure counts its quotes. It is one walk of v
 * that stops past most: Infinity for a value past most and for one a message does not hold (a big integer, a symbol, a function,
 * undefined, a number not finite, a string with a lone surrogate, an object of another kind than a plain object or an array, an
 * object of more members than PREVIEW_MEMBERS, an array with a hole or a named member, nesting past a message's depth), so nothing
 * is written out to be measured, at most PREVIEW_MEMBERS + 1 of an object's keys are inspected, a list's length is judged before any
 * of it is read, and nothing throws. Enumerating a value's keys costs the engine time in proportion to the value (keyCount).
 */
export function previewMeasure(v: unknown, most = Infinity): number {
  let n = 0
  const add = (k: number) => (n += k) <= most
  const walk = (x: unknown, depth: number): boolean => {
    switch (typeof x) {
      case 'string':
        // Every code unit is a byte at least: a string longer than what is left is past it unread.
        return x.length <= most - n && !LONE_SURROGATE.test(x) && add(measuredBytes(x) + 2)
      case 'number':
        return Number.isFinite(x) && add(String(x).length) // String(-0) is "0", as JSON.stringify writes it
      case 'boolean':
        return add(x ? 4 : 5)
      case 'object':
        break
      default:
        return false
    }
    if (x === null) return add(4)
    if (depth >= PREVIEW_DEPTH) return false
    if (Array.isArray(x)) {
      if (x.length > most - n || !dense(x) || !add(2 + Math.max(0, x.length - 1))) return false
      for (let i = 0; i < x.length; i++) if (!walk(x[i], depth + 1)) return false
      return true
    }
    if (!plain(x) || !add(2)) return false
    let members = 0
    for (const k in x) {
      if (!Object.hasOwn(x, k)) continue
      if (++members > PREVIEW_MEMBERS || k.length > most - n || !add(measuredBytes(k) + (members > 1 ? 4 : 3)) || !walk(x[k], depth + 1)) return false
    }
    return true
  }
  return walk(v, 0) ? n : Infinity
}

/** A string's measure in a message, its quotes left out: U+2028, U+2029 and control characters but \b, \f, \n, \r and \t in six bytes. */
export function measuredBytes(s: string): number {
  let n = 0
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i)
    if (c < 0x20) n += c === 8 || c === 9 || c === 10 || c === 12 || c === 13 ? 2 : 6
    else if (c === 0x22 || c === 0x5c) n += 2
    else if (c < 0x80) n += 1
    else if (c < 0x800) n += 2
    else if (c === 0x2028 || c === 0x2029) n += 6
    else if (c >= 0xd800 && c <= 0xdbff && (s.charCodeAt(i + 1) & 0xfc00) === 0xdc00) {
      n += 4
      i++
    } else n += 3 // the rest of the plane, and a lone surrogate as U+FFFD
  }
  return n
}

function exactKeys(m: Obj, keys: string[]): boolean {
  return keyCount(m, keys.length) === keys.length && keys.every((k) => Object.hasOwn(m, k))
}

function object(v: unknown, ...keys: string[]): Obj | null {
  return plain(v) && exactKeys(v, keys) ? v : null
}

const str = (v: unknown, most: number) => typeof v === 'string' && v.length <= most && measuredBytes(v) <= most
const num = (v: unknown, min: number) => typeof v === 'number' && Number.isFinite(v) && v >= min
const integer = (v: unknown) => Number.isInteger(v) && (v as number) >= 0 && (v as number) <= MAX_PREVIEW_INT
const bool = (v: unknown) => typeof v === 'boolean'

/** Whether v is a list of at most most items, every index judged (a hole is no item). */
function list(v: unknown, most: number, item: (x: unknown, i: number) => boolean): boolean {
  if (!Array.isArray(v) || v.length > most || !dense(v)) return false
  for (let i = 0; i < v.length; i++) if (!item(v[i], i)) return false
  return true
}

const pair = (name: number, value: number) => (v: unknown) => Array.isArray(v) && v.length === 2 && dense(v) && str(v[0], name) && str(v[1], value)

function pageFields(m: Obj): boolean {
  if (typeof m.title !== 'string' || [...m.title].length > MAX_PREVIEW_TITLE || !str(m.url, MAX_PREVIEW_URL) || !integer(m.epoch)) return false
  if (typeof m.revision !== 'string' || (m.revision !== '' && (m.revision.length > MAX_PREVIEW_REVISION || !PREVIEW_REVISION.test(m.revision)))) return false
  const vp = object(m.viewport, 'width', 'height', 'dpr', 'scrollX', 'scrollY', 'colorScheme', 'userAgent')
  if (!vp || !num(vp.width, 0) || !num(vp.height, 0) || !num(vp.dpr, Number.MIN_VALUE) || !num(vp.scrollX, -Infinity) || !num(vp.scrollY, -Infinity)) return false
  return str(vp.userAgent, MAX_PREVIEW_USER_AGENT) && (vp.colorScheme === 'light' || vp.colorScheme === 'dark')
}

function hintKinds(v: unknown): boolean {
  return list(v, PREVIEW_HINT_KINDS.length, (x, i) => (PREVIEW_HINT_KINDS as readonly unknown[]).includes(x) && (v as unknown[]).indexOf(x) === i)
}

/** Whether v is an element report by its schema, each field within its bound and the whole within MAX_ELEMENT_REPORT. */
export function validElement(v: unknown): v is ElementReport {
  const B = ELEMENT_BOUNDS
  const e = object(v, 'tag', 'id', 'classes', 'role', 'name', 'text', 'selector', 'box', 'attributes', 'state', 'hit', 'html', 'neighbourhood', 'ancestors', 'styles', 'hints', 'cut')
  if (!e || previewMeasure(e, MAX_ELEMENT_REPORT) > MAX_ELEMENT_REPORT) return false
  if (!['tag', 'id', 'classes', 'role', 'name'].every((k) => str(e[k], B.name))) return false
  if (!str(e.text, B.text) || !str(e.selector, B.selector) || !str(e.html, B.html) || !str(e.neighbourhood, B.neighbourhood)) return false
  const box = object(e.box, 'x', 'y', 'width', 'height')
  if (!box || !num(box.x, -Infinity) || !num(box.y, -Infinity) || !num(box.width, 0) || !num(box.height, 0)) return false
  const st = object(e.state, 'disabled', 'hidden', 'ariaDisabled', 'ariaExpanded', 'pointerEvents', 'inert', 'hovered', 'motion')
  if (!st || !bool(st.disabled) || !bool(st.hidden) || !bool(st.inert) || !bool(st.hovered) || !integer(st.motion)) return false
  if (!str(st.ariaDisabled, B.name) || !str(st.ariaExpanded, B.name) || !str(st.pointerEvents, B.name)) return false
  const hit = object(e.hit, 'self', 'selector', 'tag')
  if (!hit || !bool(hit.self) || !str(hit.selector, B.selector) || !str(hit.tag, B.name)) return false
  const ancestor = (x: unknown) => {
    const a = object(x, 'tag', 'id', 'classes', 'role')
    return !!a && ['tag', 'id', 'classes', 'role'].every((k) => str(a[k], B.ancestor)) && previewMeasure(a, B.ancestor) <= B.ancestor
  }
  const hint = (x: unknown) => {
    const h = object(x, 'how', 'file', 'line', 'column', 'component')
    return !!h && (PREVIEW_HINT_KINDS as readonly unknown[]).includes(h.how) && str(h.file, B.hintFile) && integer(h.line) && integer(h.column) && str(h.component, B.hintComponent)
  }
  return (
    list(e.attributes, B.attributes, pair(B.attributeName, B.attributeValue)) &&
    list(e.ancestors, B.ancestors, ancestor) &&
    list(e.styles, B.styles, pair(B.styleName, B.styleValue)) &&
    list(e.hints, B.hints, hint) &&
    // The cut is a prefix of ELEMENT_CUTS: its first steps, in their order.
    list(e.cut, ELEMENT_CUTS.length, (x, i) => x === ELEMENT_CUTS[i])
  )
}

const PREVIEW_FIELDS: Record<string, string[]> = {
  hello: [],
  select: [],
  look: [],
  cancel: [],
  cancelled: [],
  describe: ['selector'],
  ready: ['version', 'url', 'title', 'viewport', 'epoch', 'revision', 'hints'],
  selected: ['element', 'url', 'title', 'viewport', 'epoch', 'revision'],
  described: ['url', 'title', 'viewport', 'epoch', 'revision'],
  looked: ['url', 'title', 'viewport', 'epoch', 'revision'],
}
const TO_BRIDGE = new Set(['hello', 'select', 'describe', 'look', 'cancel'])

/**
 * v, a message travelling to, as one of the bridge's messages held to its type's schema (proto.ParsePreviewMessage): a type of to's
 * way, every field the type carries and no other, each within its bound, the whole within to's bound and an element within
 * MAX_ELEMENT_REPORT. A hello gives the nonce (any well-formed one passes); every other message must carry `nonce`, the one the
 * hello gave. v is a value a page made (a structured clone), so it is judged in one walk that stops at the bound before anything else reads it
 * whole (previewMeasure), the type's keys first: null for anything else, never an exception.
 */
export function parsePreviewMessage(v: unknown, to: PreviewDirection, nonce: string): PreviewMessage | null {
  if (!plain(v)) return null
  const m = v
  const t = typeof m.t === 'string' ? m.t : ''
  const fields = Object.hasOwn(PREVIEW_FIELDS, t) ? PREVIEW_FIELDS[t]! : null
  if (!fields || m.conductor !== PREVIEW_MARK || TO_BRIDGE.has(t) !== (to === 'bridge')) return null
  const keys = ['conductor', 't', 'nonce', ...fields]
  if (t === 'described') keys.push(Object.hasOwn(m, 'element') ? 'element' : 'missing')
  if (!exactKeys(m, keys)) return null
  const most = to === 'bridge' ? MAX_PREVIEW_TO_BRIDGE : MAX_PREVIEW_TO_WORKBENCH
  if (previewMeasure(m, most) > most) return null
  if (typeof m.nonce !== 'string' || !PREVIEW_NONCE.test(m.nonce) || (t !== 'hello' && m.nonce !== nonce)) return null
  if (t === 'describe' && (typeof m.selector !== 'string' || m.selector === '' || measuredBytes(m.selector) > MAX_PREVIEW_SELECTOR)) return null
  if (['ready', 'selected', 'described', 'looked'].includes(t) && !pageFields(m)) return null
  if (t === 'ready' && (m.version !== PREVIEW_VERSION || !hintKinds(m.hints))) return null
  if (t === 'selected' && !validElement(m.element)) return null
  if (t === 'described' && (Object.hasOwn(m, 'element') ? !validElement(m.element) : m.missing !== true)) return null
  return m as unknown as PreviewMessage
}
