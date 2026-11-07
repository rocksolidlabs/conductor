import { ref, type Ref } from 'vue'
import {
  ChunkAssembler,
  FrameType,
  ProtoVersion,
  decodeFile,
  decodeFrame,
  encodeControl,
  encodeFileWrite,
  encodeInput,
  encodeText,
  MAX_WRITE_BYTES,
  MAX_WRITE_HEADER,
  parseJSON,
  writeParts,
  type ChatPost,
  type ChatSend,
  type ControlMessage,
  type FileResponse,
  type FileWriteOptions,
  type NvimSwapChoice,
  type TransportKind,
  type Welcome,
} from '../protocol'
import { ANSWER_WAIT_SECONDS, answerMessage, NO_QUESTION, ReplyError, type QuestionReply } from '../reply'
import { rttFromPong } from './rtt'
import type { CloseInfo, TerminalTransport, TransportState } from './types'

const FILE_TIMEOUT_MS = 20000
/** How long an nvim_open may take: Neovim loading the person's config. */
const NVIM_OPEN_TIMEOUT_MS = 20_000
/**
 * How long an answer waits for the owner's word: past the owner's own waits (a keystroke's 10 s, a line's 10 s, on a host from its
 * arrival), so that an answer queued behind another write is not given up while the owner still has it; past it, the page says
 * it was not confirmed.
 */
export const ANSWER_TIMEOUT_MS = ANSWER_WAIT_SECONDS * 1000

/**
 * Shared frame handling for both transports: listener registration, the
 * welcome handshake, file request correlation and chunk reassembly.
 * Subclasses provide the wire (WebSocket or data channel).
 */
export abstract class BaseTransport implements TerminalTransport {
  readonly kind: Ref<TransportKind>
  readonly state: Ref<TransportState> = ref('idle')
  readonly rtt: Ref<number | null> = ref(null)

  protected outputCbs: Array<(data: Uint8Array, replay: boolean) => void> = []
  protected controlCbs: Array<(msg: ControlMessage) => void> = []
  protected closeCbs: Array<(info: CloseInfo) => void> = []
  protected pendingFiles = new Map<string, { resolve: (r: FileResponse) => void; reject: (e: Error) => void; timer: number }>()
  /** nvim_open requests waiting for their `opened` or `error` event, by reqId (design round 12, F8). */
  protected nvimOpens = new Map<string, { resolve: (ev: NvimEvent) => void; reject: (e: Error) => void; timer: number }>()
  /** Answers waiting for the owner's word (`answer_ok`, or an `error` naming them), by reqId. */
  protected answers = new Map<string, { resolve: (taken: boolean) => void; reject: (e: Error) => void; timer: number }>()
  /** The owner's welcome said `answer`: it takes replies that name their question. */
  protected takesAnswers = false
  /** The owner's welcome said `submitAck`: it answers a submit that names its request (#322). */
  protected acksSubmits = false
  protected chunks = new ChunkAssembler()
  protected lastError?: { code: string; message: string }
  protected welcomeResolve?: (w: Welcome) => void
  protected welcomeReject?: (e: Error) => void
  protected closed = false
  private reqCounter = 0

  /** Display name sent in the hello; a label other viewers see, not authentication. */
  protected name = ''
  /** A quiet connection, for a run's chat alone (`hello.chatOnly`): no scrollback or output, not a viewer of the session. */
  protected chatOnly = false

  constructor(kind: TransportKind, opts: { name?: string; chatOnly?: boolean } = {}) {
    this.kind = ref(kind)
    this.name = (opts.name ?? '').trim().slice(0, 40)
    this.chatOnly = !!opts.chatOnly
  }

  abstract connect(hello: { cols: number; rows: number }): Promise<Welcome>
  protected abstract send(frame: Uint8Array<ArrayBuffer>): void
  abstract close(): void

  sendInput(data: Uint8Array): void {
    if (this.state.value !== 'open') return
    this.send(encodeInput(data))
  }

  submit(text: string): void {
    if (this.state.value !== 'open') return
    this.send(encodeControl({ t: 'submit', text }))
  }

  /**
   * A line typed with the owner's word on it (#322): to an owner whose welcome said `submitAck`, a `submit` naming a request,
   * settled true on its `submit_ok`, or with a ReplyError carrying the owner's code (`not_sent`: `unlessWaiting` held it back from
   * a session that asks, nothing typed; `input_failed`: typed and not entered), the connection's end, or no word in
   * ANSWER_TIMEOUT_MS. To an older owner it goes as a plain `submit` and settles false at once.
   */
  submitAcked(text: string, opts: { unlessWaiting?: boolean } = {}): Promise<boolean> {
    if (this.state.value !== 'open') return Promise.reject(new ReplyError('not_connected', 'not connected'))
    if (!this.acksSubmits) {
      this.submit(text)
      return Promise.resolve(false)
    }
    const reqId = `s${(++this.reqCounter).toString(36)}`
    return new Promise((resolve, reject) => {
      const timer = window.setTimeout(() => {
        this.answers.delete(reqId)
        reject(new ReplyError('no_word', 'the session did not say whether it typed the line'))
      }, ANSWER_TIMEOUT_MS)
      this.answers.set(reqId, { resolve, reject, timer })
      this.send(encodeControl({ t: 'submit', text, reqId, ...(opts.unlessWaiting ? { unlessWaiting: true } : {}) }))
    })
  }

  /**
   * Replies to an agent's question. To an owner whose welcome said `answer`, a reply naming its question goes as `answer` and settles
   * with the owner's word: true once taken, a ReplyError with the owner's code when refused (`question_answered`: the question no
   * longer waits, nothing was sent), the connection's end, or no word in ANSWER_TIMEOUT_MS; one that names no question it could
   * take is not sent (`no_question`): unguarded keys could land on another question. To an older owner it goes as keys or a submit,
   * as before, and settles false at once: nothing will say whether it was taken.
   */
  answer(reply: QuestionReply): Promise<boolean> {
    if (this.state.value !== 'open') return Promise.reject(new ReplyError('not_connected', 'not connected'))
    if (!this.takesAnswers) {
      if ('input' in reply) this.sendInput(encodeText(reply.input))
      else this.submit(reply.text)
      return Promise.resolve(false)
    }
    const reqId = reply.reqId && !this.answers.has(reply.reqId) ? reply.reqId : `a${(++this.reqCounter).toString(36)}`
    const msg = answerMessage(reply, reqId)
    if (!msg) return Promise.reject(new ReplyError(NO_QUESTION, 'the page does not know which question this reply is for'))
    return new Promise((resolve, reject) => {
      const timer = window.setTimeout(() => {
        this.answers.delete(reqId)
        reject(new ReplyError('no_word', 'the session did not say whether it took the reply'))
      }, ANSWER_TIMEOUT_MS)
      this.answers.set(reqId, { resolve, reject, timer })
      this.send(encodeControl({ ...msg }))
    })
  }

  chat(post: ChatPost): void {
    if (this.state.value !== 'open') return
    this.send(encodeControl({ ...post, t: 'chat' }))
  }

  chatSend(send: ChatSend): void {
    if (this.state.value !== 'open') return
    this.send(encodeControl({ ...send, t: 'chat_send' }))
  }

  resize(cols: number, rows: number, take?: boolean): void {
    if (this.state.value !== 'open') return
    this.send(encodeControl(take ? { t: 'resize', cols, rows, take: true } : { t: 'resize', cols, rows }))
  }

  ping(): void {
    if (this.state.value !== 'open') return
    this.send(encodeControl({ t: 'ping', ts: Date.now() }))
  }

  nvimOpen(path: string): Promise<NvimEvent> {
    if (this.state.value !== 'open') return Promise.reject(new Error('not connected'))
    const reqId = `n${(++this.reqCounter).toString(36)}`
    return new Promise((resolve, reject) => {
      const timer = window.setTimeout(() => {
        this.nvimOpens.delete(reqId)
        reject(new Error('neovim did not answer'))
      }, NVIM_OPEN_TIMEOUT_MS)
      this.nvimOpens.set(reqId, { resolve, reject, timer })
      this.send(encodeControl({ t: 'nvim_open', reqId, path }))
    })
  }
  nvimInput(id: string, keys: string, seq?: number): void {
    if (this.state.value !== 'open') return
    this.send(encodeControl(seq ? { t: 'nvim_input', id, keys, seq } : { t: 'nvim_input', id, keys }))
  }
  nvimSwap(id: string, choice: NvimSwapChoice): void {
    if (this.state.value !== 'open') return
    this.send(encodeControl({ t: 'nvim_swap', id, choice }))
  }
  nvimClose(id: string, discard?: boolean): void {
    if (this.state.value !== 'open') return
    this.send(encodeControl(discard ? { t: 'nvim_close', id, discard: true } : { t: 'nvim_close', id }))
  }

  writeFile(path: string, data: Uint8Array, opts: FileWriteOptions = {}): Promise<FileResponse> {
    if (this.state.value !== 'open') return Promise.reject(new Error('not connected'))
    if (data.length > MAX_WRITE_BYTES) return Promise.reject(new Error('a file saved from the editor is at most 1 MiB'))
    const reqId = `w${(++this.reqCounter).toString(36)}`
    // A part the owner cannot read is not answered, and a save is waited for while the connection lives: one whose header would pass
    // MAX_WRITE_HEADER (a very long path) is refused here, before anything is sent. The last part's offset is the longest.
    const longest = new TextEncoder().encode(JSON.stringify({ reqId, path, offset: data.length, total: data.length, baseSha256: opts.baseSha256, force: opts.force })).length
    if (longest > MAX_WRITE_HEADER) return Promise.reject(new Error('the path is too long to save from the editor'))
    return new Promise((resolve, reject) => {
      // A save is not given up on while the connection lives: the owner answers every one, written or refused, and one given up on
      // could still be written, its answer lost and the tab left on the old hash. After FILE_TIMEOUT_MS the page hears it is slow;
      // a connection that closes rejects it (emitClose).
      const timer = window.setTimeout(() => opts.onSlow?.(), FILE_TIMEOUT_MS)
      this.pendingFiles.set(reqId, { resolve, reject, timer })
      for (const [start, end] of writeParts(data.length)) {
        this.send(encodeFileWrite({ reqId, path, offset: start, total: data.length, baseSha256: opts.baseSha256, force: opts.force }, data.subarray(start, end)))
      }
    })
  }

  requestFile(path: string, stat = false, extra: FileGetExtra = {}): Promise<FileResponse> {
    if (this.state.value !== 'open') return Promise.reject(new Error('not connected'))
    const reqId = `f${(++this.reqCounter).toString(36)}`
    return new Promise((resolve, reject) => {
      const timer = window.setTimeout(() => {
        this.pendingFiles.delete(reqId)
        reject(new Error('file request timed out'))
      }, FILE_TIMEOUT_MS)
      this.pendingFiles.set(reqId, { resolve, reject, timer })
      this.send(encodeControl({ t: 'file_get', reqId, path, stat, ...extra }))
    })
  }

  onOutput(cb: (data: Uint8Array, replay: boolean) => void): void {
    this.outputCbs.push(cb)
  }

  onControl(cb: (msg: ControlMessage) => void): void {
    this.controlCbs.push(cb)
  }

  onClose(cb: (info: CloseInfo) => void): void {
    this.closeCbs.push(cb)
  }

  protected helloFrame(hello: { cols: number; rows: number }): Uint8Array<ArrayBuffer> {
    return encodeControl({ t: 'hello', proto: ProtoVersion, cols: hello.cols, rows: hello.rows, client: 'web/1', ...(this.name ? { name: this.name } : {}), ...(this.chatOnly ? { chatOnly: true } : {}) })
  }

  /** Dispatches a terminal-stream frame. Returns false for unknown types. */
  protected handleFrame(data: ArrayBuffer | Uint8Array): boolean {
    const frame = decodeFrame(data)
    if (!frame) return false
    switch (frame.type) {
      case FrameType.Output:
        for (const cb of this.outputCbs) cb(frame.payload, false)
        return true
      case FrameType.Scrollback:
        for (const cb of this.outputCbs) cb(frame.payload, true)
        return true
      case FrameType.Control: {
        const msg = parseJSON<ControlMessage>(frame.payload)
        if (!msg) return true
        this.handleControl(msg)
        return true
      }
      case FrameType.File: {
        const res = decodeFile(frame.payload)
        if (!res) return true
        const pending = this.pendingFiles.get(res.header.reqId)
        if (pending) {
          window.clearTimeout(pending.timer)
          this.pendingFiles.delete(res.header.reqId)
          pending.resolve({ header: res.header, body: res.body.slice() })
        }
        return true
      }
      case FrameType.Chunk: {
        const whole = this.chunks.push(frame.payload)
        if (whole) this.handleFrame(whole)
        return true
      }
    }
    return false
  }

  protected handleControl(msg: ControlMessage): void {
    // The owner's word on an answer or a submit is that request's own, not the page's: it settles the reply and goes no further.
    if ((msg.t === 'answer_ok' || msg.t === 'submit_ok' || msg.t === 'error') && msg.requestId) {
      const waiting = this.answers.get(msg.requestId)
      if (waiting) {
        this.answers.delete(msg.requestId)
        window.clearTimeout(waiting.timer)
        if (msg.t === 'answer_ok' || msg.t === 'submit_ok') waiting.resolve(true)
        else waiting.reject(new ReplyError(msg.code, msg.message))
        return
      }
    }
    if (msg.t === 'welcome') {
      this.state.value = 'open'
      this.takesAnswers = !!msg.answer
      this.acksSubmits = !!msg.submitAck
      this.welcomeResolve?.(msg)
      this.welcomeResolve = undefined
      this.welcomeReject = undefined
    } else if (msg.t === 'error') {
      this.lastError = { code: msg.code, message: msg.message }
    } else if (msg.t === 'pong') {
      const r = rttFromPong(msg.ts, Date.now())
      if (r !== null) this.rtt.value = r
    } else if (msg.t === 'nvim_event' && msg.reqId && (msg.kind === 'opened' || msg.kind === 'error')) {
      const waiting = this.nvimOpens.get(msg.reqId)
      if (waiting) {
        this.nvimOpens.delete(msg.reqId)
        window.clearTimeout(waiting.timer)
        if (msg.kind === 'opened') waiting.resolve(msg)
        else waiting.reject(Object.assign(new Error(msg.message || msg.code || 'refused'), { code: msg.code }))
      }
    }
    for (const cb of this.controlCbs) cb(msg)
  }

  protected emitClose(code: number, reason: string): void {
    if (this.closed) return
    this.closed = true
    this.state.value = 'closed'
    const info: CloseInfo = { code, reason, error: this.lastError }
    this.welcomeReject?.(new Error(this.lastError?.message || reason || `closed (${code})`))
    this.welcomeReject = undefined
    this.welcomeResolve = undefined
    for (const p of this.pendingFiles.values()) {
      window.clearTimeout(p.timer)
      p.reject(new Error('connection closed'))
    }
    this.pendingFiles.clear()
    for (const a of this.answers.values()) {
      window.clearTimeout(a.timer)
      a.reject(new ReplyError('closed', 'the connection closed before the session said whether it took the reply'))
    }
    this.answers.clear()
    for (const cb of this.closeCbs) cb(info)
  }
}
