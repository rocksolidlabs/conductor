import { FrameType, decodeFrame, encodeJSONFrame, parseJSON, type Welcome } from '../protocol'
import { BaseTransport } from './base'

const SIGNAL_TIMEOUT_MS = 10000

type SignalMessage =
  | { t: 'answer'; sdp: string }
  | { t: 'ice'; candidate: RTCIceCandidateInit }
  | { t: 'relay_ok' }
  | { t: 'error'; code: string; message: string }

/**
 * Developer-hosted sessions. The WebSocket to the server carries the initial
 * welcome and WebRTC signaling; terminal frames flow over a data channel to
 * the host. If the channel does not open within the server's relay timeout,
 * the same WebSocket is switched into relay mode.
 */
export class WebRTCTransport extends BaseTransport {
  private ws?: WebSocket
  private pc?: RTCPeerConnection
  private dc?: RTCDataChannel
  private hello?: { cols: number; rows: number }
  private relayMode = false
  private relayRequested = false
  private remoteSet = false
  private pendingIce: RTCIceCandidateInit[] = []
  private relayTimer?: number
  private forceRelay: boolean

  constructor(
    /** The address, or what makes it as the connection opens (a token read then: useTerminalTransport). */
    private readonly url: string | (() => string),
    opts: { forceRelay?: boolean; name?: string; chatOnly?: boolean } = {},
  ) {
    super('webrtc', { name: opts.name })
    this.forceRelay = !!opts.forceRelay
  }

  connect(hello: { cols: number; rows: number }): Promise<Welcome> {
    this.hello = hello
    this.state.value = 'connecting'
    return new Promise<Welcome>((resolve, reject) => {
      this.welcomeResolve = resolve
      this.welcomeReject = reject
      const timer = window.setTimeout(() => {
        if (this.state.value === 'connecting') {
          this.ws?.close()
          this.emitClose(4400, 'no welcome from server')
        }
      }, SIGNAL_TIMEOUT_MS)
      let ws: WebSocket
      try {
        ws = new WebSocket(typeof this.url === 'function' ? this.url() : this.url)
      } catch (e) {
        window.clearTimeout(timer)
        this.emitClose(1006, (e as Error).message)
        return
      }
      ws.binaryType = 'arraybuffer'
      this.ws = ws
      ws.onmessage = (ev) => {
        if (!(ev.data instanceof ArrayBuffer)) return
        const frame = decodeFrame(ev.data)
        if (!frame) return
        if (frame.type === FrameType.Signal) {
          const msg = parseJSON<SignalMessage>(frame.payload)
          if (msg) this.handleSignal(msg)
          return
        }
        if (frame.type === FrameType.Control && !this.relayMode && this.state.value === 'connecting') {
          // The first control frame on the WebSocket is the signaling welcome.
          const msg = parseJSON<Welcome & { t: string }>(frame.payload)
          if (msg?.t === 'welcome') {
            window.clearTimeout(timer)
            this.state.value = 'signaling'
            this.startPeer(msg)
            return
          }
          if (msg?.t === 'error') {
            this.lastError = { code: (msg as unknown as { code: string }).code, message: (msg as unknown as { message: string }).message }
          }
          return
        }
        if (this.relayMode) this.handleFrame(ev.data)
        else if (frame.type === FrameType.Control) {
          // Errors sent over the WebSocket while the data channel is active.
          const msg = parseJSON<{ t: string; code: string; message: string }>(frame.payload)
          if (msg?.t === 'error') this.lastError = { code: msg.code, message: msg.message }
        }
      }
      ws.onclose = (ev) => {
        window.clearTimeout(timer)
        this.teardownPeer()
        this.emitClose(ev.code, ev.reason)
      }
    })
  }

  private startPeer(welcome: Welcome): void {
    if (this.forceRelay || welcome.relayOnly || typeof RTCPeerConnection === 'undefined') {
      this.requestRelay(welcome.relayOnly ? 'host_relay_only' : 'forced')
      return
    }
    const timeout = welcome.relayTimeoutMs ?? 8000
    this.relayTimer = window.setTimeout(() => this.requestRelay('timeout'), timeout)
    try {
      const pc = new RTCPeerConnection({ iceServers: (welcome.iceServers ?? []) as RTCIceServer[] })
      this.pc = pc
      pc.onicecandidate = (ev) => {
        if (ev.candidate) this.sendSignal({ t: 'ice', candidate: ev.candidate.toJSON() })
      }
      pc.onconnectionstatechange = () => {
        if (pc.connectionState === 'failed') this.requestRelay('ice_failed')
        if ((pc.connectionState === 'closed' || pc.connectionState === 'disconnected') && this.kind.value === 'webrtc' && this.state.value === 'open') {
          this.emitClose(1006, 'peer connection lost')
        }
      }
      const dc = pc.createDataChannel('term', { ordered: true })
      dc.binaryType = 'arraybuffer'
      this.dc = dc
      dc.onopen = () => {
        if (this.relayRequested) {
          dc.close()
          return
        }
        window.clearTimeout(this.relayTimer)
        this.kind.value = 'webrtc'
        dc.send(this.helloFrame(this.hello!))
      }
      dc.onmessage = (ev) => {
        if (ev.data instanceof ArrayBuffer) this.handleFrame(ev.data)
      }
      dc.onclose = () => {
        if (this.kind.value === 'webrtc' && this.state.value === 'open') this.emitClose(1006, 'data channel closed')
      }
      pc.createOffer()
        .then((offer) => pc.setLocalDescription(offer).then(() => offer))
        .then((offer) => this.sendSignal({ t: 'offer', sdp: offer.sdp }))
        .catch(() => this.requestRelay('ice_failed'))
    } catch {
      this.requestRelay('ice_failed')
    }
  }

  private handleSignal(msg: SignalMessage): void {
    switch (msg.t) {
      case 'answer':
        if (!this.pc) return
        this.pc
          .setRemoteDescription({ type: 'answer', sdp: msg.sdp })
          .then(() => {
            this.remoteSet = true
            for (const c of this.pendingIce) this.pc?.addIceCandidate(c).catch(() => {})
            this.pendingIce = []
          })
          .catch(() => this.requestRelay('ice_failed'))
        break
      case 'ice':
        if (!this.pc) return
        if (this.remoteSet) this.pc.addIceCandidate(msg.candidate).catch(() => {})
        else if (this.pendingIce.length < 64) this.pendingIce.push(msg.candidate)
        break
      case 'relay_ok':
        this.relayMode = true
        this.kind.value = 'relay'
        this.teardownPeer()
        this.ws?.send(this.helloFrame(this.hello!))
        break
      case 'error':
        this.lastError = { code: msg.code, message: msg.message }
        break
    }
  }

  private requestRelay(reason: string): void {
    if (this.relayRequested || this.state.value === 'open' || this.state.value === 'closed') return
    this.relayRequested = true
    window.clearTimeout(this.relayTimer)
    this.sendSignal({ t: 'relay', reason })
  }

  private sendSignal(msg: Record<string, unknown>): void {
    if (this.ws?.readyState === WebSocket.OPEN) this.ws.send(encodeJSONFrame(FrameType.Signal, msg))
  }

  private teardownPeer(): void {
    window.clearTimeout(this.relayTimer)
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
    this.dc = undefined
    this.pc = undefined
  }

  protected send(frame: Uint8Array<ArrayBuffer>): void {
    if (this.relayMode) {
      if (this.ws?.readyState === WebSocket.OPEN) this.ws.send(frame)
    } else if (this.dc?.readyState === 'open') {
      this.dc.send(frame)
    }
  }

  close(): void {
    this.teardownPeer()
    this.ws?.close(1000, 'bye')
    this.emitClose(1000, 'bye')
  }
}
