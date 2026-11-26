import { shallowRef, ref, type Ref, type ShallowRef } from 'vue'
import { validWay, type ChatPost, type ChatSend, type ControlMessage, type FileGetExtra, type FileResponse, type FileWriteOptions, type NvimEvent, type NvimSwapChoice, type PermissionAnswer, type Role, type SessionWay, type TranscriptMessage, type TranscriptReply, type TranscriptRequest, type Welcome } from '~/utils/protocol'
import type { CloseInfo, TerminalTransport } from '~/utils/transport/types'
import type { LaneWire } from '~/utils/transport/decide'
import { ReplyError, type QuestionReply } from '~/utils/reply'
import { Transcript } from '~/utils/transcript'

/**
 * One connection to a session, whichever way the session runs (docs/plans/chat-or-terminal.md, design 11): the transport, the
 * control messages it brings and the requests a page makes over it (files, saves, Neovim, the people's chat, answers, a chat's reads
 * and Stop). The views are its consumers: the terminal draws its output (TerminalView), a chat its TRANSCRIPT frames (row 7's
 * ChatView), and a move between ways (the `way` message) changes which one shows on the same connection, never a second one.
 */
export interface SessionConnection {
  /** The transport of the connection made last; undefined before the first connect and after disconnect. */
  readonly transport: ShallowRef<TerminalTransport | undefined>
  /** The role the owner gave this connection, and whether it may read files (the welcome's). */
  readonly role: Ref<Role | ''>
  readonly fileView: Ref<boolean>
  readonly welcome: ShallowRef<Welcome | undefined>
  /** How the session runs now (the welcome's `way`, then each `way` message), the move noted and the sentence that goes with it. */
  readonly way: Ref<SessionWay>
  readonly noted: Ref<SessionWay | ''>
  readonly why: Ref<string>
  /**
   * A chat's transcript as this page holds it (utils/transcript.ts), across the connections made again: the highest mark it holds is
   * the next hello's `transcriptAfter`, so a connection made again is sent only what changed after it. `transcriptVersion` rises with
   * every change, for a view to redraw by.
   */
  readonly transcript: Transcript
  readonly transcriptVersion: Ref<number>
  /** Makes a fresh transport and connects it with hello (the transport's own connect), the one before closed. */
  connect(hello: { cols: number; rows: number }): Promise<Welcome>
  disconnect(): void
  /** Whether the connection is open. */
  open(): boolean
  /** The terminal's output (OUTPUT and SCROLLBACK, replay true for the latter), every control message, a chat's TRANSCRIPT frames, the close. */
  onOutput(cb: (data: Uint8Array, replay: boolean) => void): () => void
  onControl(cb: (msg: ControlMessage) => void): () => void
  onTranscript(cb: (msg: TranscriptMessage) => void): () => void
  onClose(cb: (info: CloseInfo, transport: TerminalTransport) => void): () => void
  requestFile(path: string, stat?: boolean, extra?: FileGetExtra): Promise<FileResponse>
  writeFile(path: string, data: Uint8Array, opts?: FileWriteOptions): Promise<FileResponse>
  nvimOpen(path: string): Promise<NvimEvent>
  nvimInput(id: string, keys: string, seq?: number): void
  nvimSwap(id: string, choice: NvimSwapChoice): void
  nvimClose(id: string, discard?: boolean): void
  /** Keys to the terminal; false when not open. */
  sendInput(data: Uint8Array): boolean
  submit(text: string): boolean
  /** A message with the owner's word on it (a chat's: `submit_ok` once the agent took it, or the owner's refusal in its words). */
  submitAcked(text: string): Promise<boolean>
  answer(reply: QuestionReply, opts?: { sent?: () => void }): Promise<boolean>
  chat(post: ChatPost): boolean
  chatSend(send: ChatSend): boolean
  /** The lane of a decision link's connection, null otherwise. */
  lane(): LaneWire | null
  resize(cols: number, rows: number, take?: boolean): void
  ping(): void
  transcriptGet(req: TranscriptRequest): Promise<TranscriptReply>
  interrupt(): Promise<boolean>
  /** The operator's answer to a chat's permission request (the welcome's `permissionAnswer` says this connection may). */
  permissionAnswer(a: Omit<PermissionAnswer, 't' | 'reqId'>): Promise<boolean>
}

/** The connection of one session's view: what TerminalView holds, split from its terminal. */
export function useSessionConnection(createTransport: () => TerminalTransport): SessionConnection {
  const transport = shallowRef<TerminalTransport>()
  const role = ref<Role | ''>('')
  const fileView = ref(false)
  const welcome = shallowRef<Welcome>()
  const way = ref<SessionWay>('terminal')
  const noted = ref<SessionWay | ''>('')
  const why = ref('')
  const transcript = new Transcript()
  const transcriptVersion = ref(0)
  const outputs = new Set<(data: Uint8Array, replay: boolean) => void>()
  const controls = new Set<(msg: ControlMessage) => void>()
  const transcripts = new Set<(msg: TranscriptMessage) => void>()
  const closes = new Set<(info: CloseInfo, transport: TerminalTransport) => void>()
  const listen = <T>(set: Set<T>, cb: T) => {
    set.add(cb)
    return () => void set.delete(cb)
  }

  function control(t: TerminalTransport, msg: ControlMessage) {
    if (transport.value !== t) return
    if (msg.t === 'welcome') {
      welcome.value = msg
      role.value = msg.role
      fileView.value = msg.fileView
      way.value = msg.way === 'protocol' ? 'protocol' : 'terminal'
      noted.value = msg.noted === 'terminal' || msg.noted === 'protocol' ? msg.noted : ''
      why.value = ''
      transcript.welcome(msg)
      transcriptVersion.value = transcript.version
    } else if (msg.t === 'way') {
      // A way message past its bounds is not acted on.
      if (!validWay(msg)) return
      way.value = msg.way
      noted.value = msg.noted ?? ''
      why.value = msg.why ?? ''
      // The owner sends no entry while the session runs in its terminal: what it added meanwhile (the move's notice) is read back as
      // it comes back to the chat on this connection.
      if (msg.way === 'terminal') transcript.leave()
      else if (transcript.rejoin()) readBack(t)
    }
    for (const cb of controls) cb(msg)
  }

  /** Reads back the gap the transcript holds, on the connection t. */
  function readBack(t: TerminalTransport) {
    void transcript
      .readBack((req) => t.transcriptGet(req))
      .catch(() => undefined)
      .finally(() => (transcriptVersion.value = transcript.version))
  }

  function disconnect() {
    const t = transport.value
    transport.value = undefined
    t?.close()
  }

  async function connect(hello: { cols: number; rows: number }): Promise<Welcome> {
    disconnect()
    const t = createTransport()
    transport.value = t
    role.value = ''
    welcome.value = undefined
    t.onOutput((data, replay) => {
      if (transport.value !== t) return
      for (const cb of outputs) cb(data, replay)
    })
    t.onControl((msg) => control(t, msg))
    t.onTranscript((msg) => {
      if (transport.value !== t) return
      if (transcript.take(msg)) {
        transcriptVersion.value = transcript.version
        // A replay that left changes out is read back before its mark is held (see Chat sessions: `more`).
        if (msg.t === 'page' && msg.more && transcript.gap !== null) readBack(t)
      }
      for (const cb of transcripts) cb(msg)
    })
    // The resume point the page holds goes with the hello of the connection made now.
    t.transcriptFrom(transcript.mark)
    t.onClose((info) => {
      for (const cb of closes) cb(info, t)
    })
    return t.connect(hello)
  }

  const open = () => transport.value?.state.value === 'open'
  const notConnected = () => Promise.reject(new Error('not connected'))
  return {
    transport,
    role,
    fileView,
    welcome,
    way,
    noted,
    why,
    transcript,
    transcriptVersion,
    connect,
    disconnect,
    open,
    onOutput: (cb) => listen(outputs, cb),
    onControl: (cb) => listen(controls, cb),
    onTranscript: (cb) => listen(transcripts, cb),
    onClose: (cb) => listen(closes, cb),
    requestFile: (path, stat = false, extra = {}) => transport.value?.requestFile(path, stat, extra) ?? notConnected(),
    writeFile: (path, data, opts) => transport.value?.writeFile(path, data, opts) ?? notConnected(),
    nvimOpen: (path) => transport.value?.nvimOpen(path) ?? notConnected(),
    nvimInput: (id, keys, seq) => transport.value?.nvimInput(id, keys, seq),
    nvimSwap: (id, choice) => transport.value?.nvimSwap(id, choice),
    nvimClose: (id, discard) => transport.value?.nvimClose(id, discard),
    sendInput(data) {
      if (!open()) return false
      transport.value!.sendInput(data)
      return true
    },
    submit: (text) => open() && transport.value!.submit(text),
    submitAcked: (text) => (open() ? transport.value!.submitAcked(text) : Promise.reject(new ReplyError('not_connected', 'not connected'))),
    answer(reply, opts = {}) {
      if (!open()) return Promise.reject(new ReplyError('not_connected', 'not connected'))
      return transport.value!.answer(reply, opts)
    },
    chat(post) {
      if (!open()) return false
      transport.value!.chat(post)
      return true
    },
    chatSend(send) {
      if (!open()) return false
      transport.value!.chatSend(send)
      return true
    },
    lane: () => (transport.value?.decisions ? transport.value : null),
    resize: (cols, rows, take) => transport.value?.resize(cols, rows, take),
    ping: () => transport.value?.ping(),
    transcriptGet: (req) => transport.value?.transcriptGet(req) ?? Promise.reject(new ReplyError('not_connected', 'not connected')),
    interrupt: () => transport.value?.interrupt() ?? Promise.reject(new ReplyError('not_connected', 'not connected')),
    permissionAnswer: (a) => transport.value?.permissionAnswer(a) ?? Promise.reject(new ReplyError('not_connected', 'not connected')),
  }
}
