import { MAX_TRANSCRIPT_ENTRY, MAX_TRANSCRIPT_PAGE, type TranscriptEntry, type TranscriptMessage, type TranscriptReply, type TranscriptRequest, type Welcome } from './protocol'
import type { MarkView } from './status'

// A chat's transcript as the page keeps it (docs/plans/chat-or-terminal.md, designs 5, 6 and 11): the entries the owner sent, each
// kept at its latest revision by `seq`, the highest `mark` held (the next hello's `transcriptAfter`, so a connection made again is
// sent only what changed after it), and the chat's view of them: turns, their steps folded into one line, a file change's line,
// a permission question, a notice. What the wire bounds, the parser refuses past its bound; what the page keeps is bounded too.

/** An entry's bounds, as the session cuts them (internal/session: MaxEntryText, MaxEntryRef, MaxEntrySummary, MaxEventTool, ...). */
export const MAX_ENTRY_TEXT = 4 * 1024
export const MAX_ENTRY_REF = 128
export const MAX_ENTRY_SUMMARY = 1024
export const MAX_ENTRY_TOOL = 100
export const MAX_ENTRY_PATH = 1024
export const MAX_ENTRY_WORDS = 500
export const MAX_ENTRY_BY_NAME = 40
export const MAX_ENTRY_QUESTION = 32
/** What the page keeps, as the session's ring does (MaxTranscriptEntries, MaxTranscriptBytes): past either, the oldest go first. */
export const MAX_KEPT_ENTRIES = 1000
export const MAX_KEPT_BYTES = 2 * 1024 * 1024
/** How many pages a replay that left changes out is read back over at most: the ring's whole (1,000 entries, 100 a page). */
export const MAX_PAGES_BACK = 10

const KINDS = new Set(['prompt', 'text', 'tool', 'file', 'permission', 'turn', 'notice'])
const enc = new TextEncoder()
const bytes = (s: string) => enc.encode(s).length
const uint = (v: unknown, max = Number.MAX_SAFE_INTEGER) => typeof v === 'number' && Number.isInteger(v) && v >= 0 && v <= max
const within = (v: unknown, max: number) => v === undefined || (typeof v === 'string' && bytes(v) <= max)

/** Whether e is an entry within the wire's bounds: what the page takes. Anything past a bound is refused whole, never cut here. */
export function entryValid(e: unknown): e is TranscriptEntry {
  if (!e || typeof e !== 'object') return false
  const x = e as TranscriptEntry
  if (!uint(x.seq) || x.seq < 1 || !uint(x.mark) || !uint(x.rev, 0xffffffff) || !uint(x.turn, 0xffffffff)) return false
  if (!KINDS.has(x.kind) || typeof x.at !== 'string') return false
  if (!within(x.text, MAX_ENTRY_TEXT) || !within(x.ref, MAX_ENTRY_REF) || !within(x.parent, MAX_ENTRY_REF) || !within(x.call, MAX_ENTRY_REF)) return false
  if (!within(x.summary, MAX_ENTRY_SUMMARY) || !within(x.tool, MAX_ENTRY_TOOL) || !within(x.path, MAX_ENTRY_PATH) || !within(x.cwd, MAX_ENTRY_PATH)) return false
  if (!within(x.words, MAX_ENTRY_WORDS) || !within(x.why, MAX_ENTRY_WORDS) || !within(x.question, MAX_ENTRY_QUESTION)) return false
  if (x.byName !== undefined && (typeof x.byName !== 'string' || Array.from(x.byName).length > MAX_ENTRY_BY_NAME)) return false
  if (x.inputSha256 !== undefined && !/^[0-9a-f]{64}$/.test(x.inputSha256)) return false
  return bytes(JSON.stringify(x)) <= MAX_TRANSCRIPT_ENTRY
}

/** Whether a page is within its bounds: at most a page's entries, each one valid. */
export function pageValid(entries: unknown): entries is TranscriptEntry[] {
  return Array.isArray(entries) && entries.length <= MAX_TRANSCRIPT_PAGE && entries.every(entryValid)
}

/** Whether a kept entry outranks one come in for the same seq: a later revision, or the same one marked later. */
function keeps(kept: TranscriptEntry, e: TranscriptEntry): boolean {
  return kept.rev > e.rev || (kept.rev === e.rev && kept.mark >= e.mark)
}

const openRequest = (e: TranscriptEntry) => e.kind === 'permission' && e.state === 'open'

/**
 * The page's transcript of one session: fed the TRANSCRIPT frames its connection hears (`take`), told of each welcome (`welcome`),
 * read back over a gap a replay left (`readBack`). `mark` is the highest mark of a run of changes it holds whole: what the next
 * hello carries. `version` rises with every change, for a view to redraw by.
 */
export class Transcript {
  readonly entries = new Map<number, TranscriptEntry>()
  /** The resume point: the highest mark held with nothing missing below it. */
  mark = 0
  version = 0
  /** A replay that left changes out (`more`): the seq below which it is read back, and the mark held once it has been. */
  private back: { before: number; mark: number } | null = null
  private size = 0

  /** The entries in seq order. */
  list(): TranscriptEntry[] {
    return [...this.entries.values()].sort((a, b) => a.seq - b.seq)
  }

  /** Whether a replay left changes out that the page has not read back yet. */
  get gap(): number | null {
    return this.back?.before ?? null
  }

  /** A welcome: a transcript whose mark is below the one held is a new one (the session's life began again), and the page starts afresh. */
  welcome(w: Pick<Welcome, 'way' | 'transcript'>): void {
    if (w.way !== 'protocol' || !w.transcript) return
    if (w.transcript.mark < this.mark) this.clear()
  }

  clear(): void {
    this.entries.clear()
    this.mark = 0
    this.size = 0
    this.back = null
    this.version++
  }

  /** One TRANSCRIPT message the connection heard (an entry, or the replay's page; a reply to a read is the read's). False when refused. */
  take(msg: TranscriptMessage): boolean {
    if (msg.t === 'entry') {
      if (!entryValid(msg.entry)) return false
      this.put(msg.entry)
      if (!this.back) this.mark = Math.max(this.mark, msg.entry.mark)
      else this.back.mark = Math.max(this.back.mark, msg.entry.mark)
      this.bound()
      this.version++
      return true
    }
    if (msg.t !== 'page' || msg.reqId) return false
    if (!pageValid(msg.entries)) return false
    for (const e of msg.entries) this.put(e)
    const top = msg.entries.reduce((m, e) => Math.max(m, e.mark), 0)
    if (msg.more && msg.entries.length) {
      const low = msg.entries.reduce((m, e) => Math.min(m, e.seq), Number.MAX_SAFE_INTEGER)
      this.back = { before: Math.min(low, this.back?.before ?? low), mark: Math.max(top, this.back?.mark ?? 0, this.mark) }
    } else if (this.back) this.back.mark = Math.max(this.back.mark, top)
    else this.mark = Math.max(this.mark, top)
    this.bound()
    this.version++
    return true
  }

  /**
   * Reads back what a replay left out (`gap`), a page at a time with `transcript_get before`, at most MAX_PAGES_BACK pages, then holds
   * the replay's mark: the next connection is sent only what changes after it. A read refused leaves the gap for the next connection.
   */
  async readBack(get: (req: TranscriptRequest) => Promise<TranscriptReply>): Promise<void> {
    for (let n = 0; this.back && n < MAX_PAGES_BACK; n++) {
      const r = await get({ before: this.back.before, limit: MAX_TRANSCRIPT_PAGE })
      if (r.t !== 'page' || !pageValid(r.entries)) return
      for (const e of r.entries) this.put(e)
      this.version++
      const low = r.entries.reduce((m, e) => Math.min(m, e.seq), Number.MAX_SAFE_INTEGER)
      if (!r.more || !r.entries.length || low >= this.back.before) break
      this.back.before = low
    }
    if (this.back) {
      this.mark = Math.max(this.mark, this.back.mark)
      this.back = null
    }
    this.bound()
    this.version++
  }

  private put(e: TranscriptEntry): void {
    const kept = this.entries.get(e.seq)
    if (kept && keeps(kept, e)) return
    if (kept) this.size -= bytes(JSON.stringify(kept))
    this.entries.set(e.seq, e)
    this.size += bytes(JSON.stringify(e))
  }

  /** Past MAX_KEPT_ENTRIES or MAX_KEPT_BYTES the oldest entries go, an open request's never. */
  private bound(): void {
    if (this.entries.size <= MAX_KEPT_ENTRIES && this.size <= MAX_KEPT_BYTES) return
    for (const e of this.list()) {
      if (this.entries.size <= MAX_KEPT_ENTRIES && this.size <= MAX_KEPT_BYTES) return
      if (openRequest(e)) continue
      this.entries.delete(e.seq)
      this.size -= bytes(JSON.stringify(e))
    }
  }
}

/** A message's text as the chat shows it: none for one that answers a question (#364: only its sender's browser shows what they typed). */
export function promptText(e: TranscriptEntry): string {
  return e.answers ? '' : (e.text ?? '')
}

/** The line under a message the agent has not taken yet (design 13): queued during a turn, or sent and not acknowledged. */
export function promptLine(e: TranscriptEntry): string {
  if (e.state === 'queued') return 'Sends when the turn ends.'
  if (e.state === 'unconfirmed') return 'Sent, not yet confirmed.'
  return ''
}

/** One thing of a turn's the agent wrote, changed or asked, in the order its stream gave it. */
export type TurnItem =
  | { kind: 'text'; seq: number; ref: string; text: string; final: boolean }
  | { kind: 'file'; entry: TranscriptEntry }
  | { kind: 'permission'; entry: TranscriptEntry }

/** A turn: the messages written for it, what the agent wrote and did in it, its tool steps folded apart, and how it ended. */
export interface ChatTurn {
  kind: 'turn'
  key: string
  turn: number
  prompts: TranscriptEntry[]
  items: TurnItem[]
  steps: TranscriptEntry[]
  running: boolean
  /** The turn's end (`done`, `interrupted`, `error`, `limit`), '' while it runs or before its end is kept. */
  outcome: string
}

export interface ChatNotice {
  kind: 'notice'
  key: string
  entry: TranscriptEntry
}

export type ChatBlock = ChatTurn | ChatNotice

/**
 * The chat's blocks from the entries in seq order: each turn (a new one wherever the turn's number goes back, as after a move to the
 * terminal and back), its messages, the agent's words joined by `ref` with its file changes and permission questions where they came,
 * and its tool steps apart (the one folded line); a notice as a block of its own.
 */
export function chatBlocks(entries: readonly TranscriptEntry[]): ChatBlock[] {
  const blocks: ChatBlock[] = []
  const turns = new Map<string, ChatTurn>()
  const started = new Set<string>()
  let epoch = 0
  let top = 0
  const turnOf = (n: number): ChatTurn => {
    const key = `${epoch}:${n}`
    let t = turns.get(key)
    if (!t) {
      t = { kind: 'turn', key, turn: n, prompts: [], items: [], steps: [], running: false, outcome: '' }
      turns.set(key, t)
      blocks.push(t)
    }
    return t
  }
  for (const e of entries) {
    if (e.kind === 'notice') {
      blocks.push({ kind: 'notice', key: `n${e.seq}`, entry: e })
      continue
    }
    // A turn's start where its number was already begun (or a lower one): the session's count began again (a move back to the chat).
    if (e.kind === 'turn' && e.phase === 'start' && (e.turn < top || started.has(`${epoch}:${e.turn}`))) {
      epoch++
      top = 0
    }
    top = Math.max(top, e.turn)
    const t = turnOf(e.turn)
    switch (e.kind) {
      case 'turn':
        if (e.phase === 'start') {
          started.add(t.key)
          t.running = !t.outcome
        }        else {
          t.running = false
          t.outcome = e.outcome || 'done'
        }
        break
      case 'tool':
        t.steps.push(e)
        break
      case 'text': {
        const last = t.items[t.items.length - 1]
        if (last?.kind === 'text' && e.ref && last.ref === e.ref) {
          last.text += e.text ?? ''
          last.final = !!e.final
        } else t.items.push({ kind: 'text', seq: e.seq, ref: e.ref ?? '', text: e.text ?? '', final: !!e.final })
        break
      }
      case 'prompt':
        t.prompts.push(e)
        break
      case 'file':
        t.items.push({ kind: 'file', entry: e })
        break
      case 'permission':
        t.items.push({ kind: 'permission', entry: e })
        break
    }
  }
  return blocks
}

/** The folded steps' line (design 11): "Working." while the turn runs, else how many steps it took; none for a turn with none. */
export function stepsLine(t: Pick<ChatTurn, 'running' | 'steps'>): string {
  if (t.running) return 'Working.'
  if (!t.steps.length) return ''
  return t.steps.length === 1 ? '1 step' : `${t.steps.length} steps`
}

/** A tool step's state as the one status language draws it, by the entry's status. */
export function toolStepMark(status: TranscriptEntry['status']): MarkView {
  switch (status) {
    case 'running':
      return { key: 'working', word: 'running', legend: 'running: the step has not ended yet' }
    case 'error':
      return { key: 'failed', word: 'failed', legend: 'failed: the step ended with an error' }
    case 'denied':
      return { key: 'cancelled', word: 'denied', legend: 'denied: the step was not allowed to run' }
    case 'interrupted':
      return { key: 'cancelled', word: 'stopped', legend: 'stopped: the turn was stopped while the step ran' }
  }
  return { key: 'done', word: 'done', legend: 'done: the step ended' }
}

/** A file change's one line (design 11): "`main.go` changed." (the path's last part), or its whole path's when it has no folder. */
export function fileLine(e: Pick<TranscriptEntry, 'path' | 'op' | 'outside'>): { name: string; words: string } {
  const name = e.outside || !e.path ? 'A file outside the folder' : e.path.split('/').pop() || e.path
  const words = e.op === 'create' ? 'created.' : e.op === 'delete' ? 'deleted.' : 'changed.'
  return { name, words }
}

/** The lines of a change's diff the chat draws at most (design 11); the rest is in the editor's diff tab. */
export const INLINE_DIFF_LINES = 40

/** What a permission question says once it is no longer open: who answered, or why it ended (the entry's fixed words). */
export function permissionEnd(e: Pick<TranscriptEntry, 'state' | 'byName' | 'why'>): string {
  switch (e.state) {
    case 'allowed':
      return e.byName ? `Allowed once by ${e.byName}.` : 'Allowed once.'
    case 'denied':
      return e.why || (e.byName ? `Denied by ${e.byName}.` : 'Denied.')
    case 'withdrawn':
      return e.why || 'The request was lost.'
  }
  return ''
}

/** The last line of the agent's words, for a tile that shows a chat (design 11): the newest text entry's last line with any words. */
export function lastWords(entries: readonly TranscriptEntry[]): string {
  for (let i = entries.length - 1; i >= 0; i--) {
    const e = entries[i]!
    if (e.kind !== 'text' || !e.text) continue
    const lines = e.text.split('\n').map((l) => l.trim()).filter(Boolean)
    if (lines.length) return lines[lines.length - 1]!
  }
  return ''
}

/** A part read whole: its bytes as kept, its whole size, and whether the session kept less than the whole (`cut`). */
export interface ReadPart {
  bytes: Uint8Array
  total: number
  cut: boolean
}

/** A part's most the page reads (the session's MaxTranscriptPart): past it, the session keeps none of it either. */
export const MAX_PART_READ = 256 * 1024

/**
 * A part of an entry read whole, a piece at a time (each `transcript_get part` at most MAX_TRANSCRIPT_DATA), up to what the session
 * keeps of it and at most MAX_PART_READ bytes. A reply that names another part, or a piece that does not follow on, is refused.
 */
export async function readPart(get: (req: TranscriptRequest) => Promise<TranscriptReply>, seq: number, name: 'input' | 'output' | 'diff'): Promise<ReadPart> {
  const chunks: Uint8Array[] = []
  let offset = 0
  let total = 0
  let cut = false
  for (;;) {
    const r = await get({ part: { seq, name, offset } })
    if (r.t !== 'part' || r.seq !== seq || r.name !== name || r.offset !== offset) throw new Error('the owner answered another part')
    total = r.total
    cut = !!r.cut || r.kept < r.total
    chunks.push(r.bytes)
    offset += r.bytes.length
    if (!r.bytes.length || offset >= r.kept || offset >= MAX_PART_READ) break
  }
  const out = new Uint8Array(offset)
  let at = 0
  for (const c of chunks) {
    out.set(c, at)
    at += c.length
  }
  return { bytes: out.subarray(0, Math.min(out.length, MAX_PART_READ)), total, cut }
}

/**
 * get with at most n of its reads on their way at once (the owner's MAX_TRANSCRIPT_IN_FLIGHT a connection): a read past them waits
 * its turn, in order, and goes when one before it has settled, rather than being refused. A chat that draws many questions, steps
 * and diffs at once reads them all.
 */
export function limitReads<Q, R>(get: (req: Q) => Promise<R>, n: number): (req: Q) => Promise<R> {
  let going = 0
  const waiting: Array<() => void> = []
  const next = () => {
    going--
    waiting.shift()?.()
  }
  return (req) =>
    new Promise<R>((resolve, reject) => {
      const go = () => {
        going++
        get(req).then(resolve, reject).finally(next)
      }
      if (going < n) go()
      else waiting.push(go)
    })
}

/** How near its end a chat's scroll may be and still be at its end, in pixels. */
export const CHAT_END_SLACK = 48

/** Whether a chat's scroll shows its end now: what a new entry follows, measured before the entry is drawn. */
export function atChatEnd(el: Pick<HTMLElement, 'scrollHeight' | 'scrollTop' | 'clientHeight'>): boolean {
  return el.scrollHeight - el.scrollTop - el.clientHeight < CHAT_END_SLACK
}
