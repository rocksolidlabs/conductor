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
  | { t: 'attention'; state: AttentionState; message?: string; source?: string; kind?: AttentionKind; options?: AttentionOption[]; id?: string; answered?: 'terminal'; plan?: string }
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

/** The part of registered that says which links the server still holds for the session; absent from an older server. */
export interface HostRegisteredLinks {
  links?: string[]
}

/** A crew run as a switchyard shows it: what a host sends with link_run and link_run_update. */
export interface HostRunGroup {
  id: string
  name: string
  members: Array<{ name: string; sessionId?: string; agentId: string; status: 'pending' | 'starting' | 'running' | 'ended' }>
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
