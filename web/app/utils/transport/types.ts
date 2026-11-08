import type { Ref } from 'vue'
import type { ChatPost, ChatSend, ControlMessage, FileGetExtra, FileResponse, FileWriteOptions, NvimEvent, NvimSwapChoice, TransportKind, Welcome } from '../protocol'
import type { QuestionReply } from '../reply'
import type { LaneWire } from './decide'

export type TransportState = 'idle' | 'connecting' | 'signaling' | 'open' | 'closed'

export interface CloseInfo {
  code: number
  reason: string
  /** Last error control message received before the close, if any. */
  error?: { code: string; message: string }
}

export interface TerminalTransport extends LaneWire {
  readonly kind: Ref<TransportKind>
  readonly state: Ref<TransportState>
  /** Last measured ping/pong round trip in ms; null until the first pong. */
  readonly rtt: Ref<number | null>
  /** `hello` 0 × 0 (FOLLOW_SIZE) follows the session's size; a controller's other size sets it. */
  connect(hello: { cols: number; rows: number }): Promise<Welcome>
  sendInput(data: Uint8Array): void
  /**
   * Submits a line as a reply box does: the owner types it (as a paste when the program asks for one) and presses
   * Enter 250 ms later, so that a TUI takes the Enter as Enter. Controllers only; at most MAX_SUBMIT bytes.
   */
  submit(text: string): void
  /**
   * Types a line as `submit` does and settles with the owner's word (#322), to an owner whose welcome said `submitAck`: true once
   * the line was entered; a ReplyError with the owner's code when it was not (`not_sent`: the session was asking and `unlessWaiting`
   * held the line back, nothing typed; `input_failed`: typed and not entered), the connection's end, or no word in time. To an
   * older owner it goes as a plain `submit` and settles false at once: nothing will say whether it landed.
   */
  submitAcked(text: string, opts?: { unlessWaiting?: boolean }): Promise<boolean>
  /**
   * Replies to an agent's question, naming it (the attention's `id`) to an owner whose welcome said `answer`: true once the owner
   * took it, a ReplyError when it did not (`question_answered`: the question no longer waits, nothing was sent). To an older owner,
   * or without a question's id, it goes as keys or a submit and settles false at once.
   */
  answer(reply: QuestionReply): Promise<boolean>
  /** Posts to the session's chat (every role); only once the owner's welcome said `chat`. */
  chat(post: ChatPost): void
  /** Types a kept chat message into the agent (controllers only). */
  chatSend(send: ChatSend): void
  /** take: Fit to my window, this window takes the session's size (round 14); without it only the viewer that sizes it resizes it. */
  resize(cols: number, rows: number, take?: boolean): void
  ping(): void
  /** The owner's last welcome said `decisions`, and the connection is open: the lane (laneRequest, decide) may be asked. */
  readonly decisions: boolean
  requestFile(path: string, stat?: boolean, extra?: FileGetExtra): Promise<FileResponse>
  /** Saves a file in parts (F6); the FILE reply (kind `written` or `error`) settles it. */
  writeFile(path: string, data: Uint8Array, opts?: FileWriteOptions): Promise<FileResponse>
  /** The editor's Neovim (F8): open a file on the session's machine (the `opened` event, or the `error` one, settles it), send keys, close. */
  nvimOpen(path: string): Promise<NvimEvent>
  nvimInput(id: string, keys: string, seq?: number): void
  nvimClose(id: string, discard?: boolean): void
  nvimSwap(id: string, choice: NvimSwapChoice): void
  onOutput(cb: (data: Uint8Array, replay: boolean) => void): void
  onControl(cb: (msg: ControlMessage) => void): void
  onClose(cb: (info: CloseInfo) => void): void
  close(): void
}
