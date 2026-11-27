import type { Welcome } from '../protocol'
import { encodePasteBlob, decodePasteBlob } from '../paste'
import { BaseTransport } from './base'

const GATHER_MS = 5000
const OPEN_TIMEOUT_MS = 30000

/**
 * A paste invite's viewer side: no server at all. `offer()` makes the peer
 * connection and the data channel, gathers every ICE candidate (no trickle)
 * and gives the offer as a blob to send through any messenger; `accept()`
 * takes the session's answer blob back; `connect()` then resolves once the
 * data channel is open and the session's welcome has come over it, as over
 * a host's data channel.
 */
export class PasteTransport extends BaseTransport {
  private pc?: RTCPeerConnection
  private dc?: RTCDataChannel
  private hello?: { cols: number; rows: number }
  private opened = false
  private openCbs: Array<() => void> = []

  constructor(
    private readonly stun: string[],
    opts: { name?: string } = {},
  ) {
    super('webrtc', { name: opts.name })
  }

  /** The offer blob, once every candidate is gathered (or GATHER_MS has passed). */
  async offer(): Promise<string> {
    if (typeof RTCPeerConnection === 'undefined') throw new Error('WebRTC is not available here')
    const pc = new RTCPeerConnection({ iceServers: this.stun.length ? [{ urls: this.stun }] : [] })
    this.pc = pc
    const dc = pc.createDataChannel('term', { ordered: true })
    dc.binaryType = 'arraybuffer'
    this.dc = dc
    dc.onopen = () => {
      this.opened = true
      if (this.hello) dc.send(this.helloFrame(this.hello))
      for (const cb of this.openCbs.splice(0)) cb()
    }
    dc.onmessage = (ev) => {
      if (ev.data instanceof ArrayBuffer) this.handleFrame(ev.data)
    }
    dc.onclose = () => {
      if (this.state.value === 'open') this.emitClose(1006, 'data channel closed')
    }
    pc.onconnectionstatechange = () => {
      if (pc.connectionState === 'failed') this.emitClose(1006, 'the peers could not connect')
      if ((pc.connectionState === 'closed' || pc.connectionState === 'disconnected') && this.state.value === 'open') this.emitClose(1006, 'peer connection lost')
    }
    const gathered = new Promise<void>((resolve) => {
      const done = () => resolve()
      if (pc.iceGatheringState === 'complete') done()
      pc.onicegatheringstatechange = () => {
        if (pc.iceGatheringState === 'complete') done()
      }
      window.setTimeout(done, GATHER_MS)
    })
    const offer = await pc.createOffer()
    await pc.setLocalDescription(offer)
    await gathered
    const sdp = pc.localDescription?.sdp
    if (!sdp) throw new Error('no offer')
    return encodePasteBlob('offer', sdp)
  }

  /** The session's answer, pasted back. */
  async accept(answerBlob: string): Promise<void> {
    if (!this.pc) throw new Error('make the invite first')
    const sdp = await decodePasteBlob(answerBlob, 'answer')
    await this.pc.setRemoteDescription({ type: 'answer', sdp })
  }

  connect(hello: { cols: number; rows: number }): Promise<Welcome> {
    this.hello = hello
    this.state.value = this.opened ? 'signaling' : 'connecting'
    return new Promise<Welcome>((resolve, reject) => {
      this.welcomeResolve = resolve
      this.welcomeReject = reject
      const timer = window.setTimeout(() => {
        if (this.state.value !== 'open') this.emitClose(4400, 'the session did not answer')
      }, OPEN_TIMEOUT_MS)
      const onOpen = () => {
        this.state.value = 'signaling'
      }
      if (this.opened) {
        onOpen()
        this.dc?.send(this.helloFrame(hello))
      } else this.openCbs.push(onOpen)
      const stop = this.onCloseOnce(() => window.clearTimeout(timer))
      void stop
    })
  }

  private onCloseOnce(cb: () => void): () => void {
    const wrapped = () => {
      cb()
      this.closeCbs = this.closeCbs.filter((c) => c !== wrapped)
    }
    this.closeCbs.push(wrapped)
    return () => {
      this.closeCbs = this.closeCbs.filter((c) => c !== wrapped)
    }
  }

  protected send(frame: Uint8Array<ArrayBuffer>): void {
    if (this.dc?.readyState === 'open') this.dc.send(frame)
  }

  close(): void {
    try {
      this.dc?.close()
    } catch {
      /* ignore */
    }
    try {
      this.pc?.close()
    } catch {
      /* ignore */
    }
    this.emitClose(1000, 'bye')
  }
}
