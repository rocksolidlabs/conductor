import type { TransportKind } from '../protocol'
import type { TransportState } from './types'

/** How a connection badge draws a transport: its icon, its words, its colour, and whether the words show beside the icon. */
export interface TransportLook {
  icon: string
  words: string
  color: 'error' | 'warning' | 'neutral'
  /** False only for a compact badge on a healthy connection: the icon alone, the words its label and tooltip. */
  showWords: boolean
}

/** The transport's name as a person reads it. */
export function transportName(kind: TransportKind): string {
  switch (kind) {
    case 'ws':
      return 'WebSocket'
    case 'webrtc':
      return 'WebRTC direct'
    case 'relay':
      return 'Relay'
  }
  return kind
}

/**
 * The connection badge's look: words for every state ("connecting", "disconnected"), and for an open connection its name and round
 * trip ("WebSocket · 12 ms"). A compact badge (the session page's) draws an open connection as its icon alone, the words left to its
 * label and tooltip; any other state keeps its words in view, since that is when a person needs to read them.
 */
export function transportLook(kind: TransportKind, state: TransportState, rtt: number | null | undefined, compact = true): TransportLook {
  const name = transportName(kind)
  const words = state === 'connecting' ? 'connecting' : state === 'signaling' ? 'negotiating WebRTC' : state === 'closed' ? 'disconnected' : state === 'idle' ? 'idle' : rtt != null ? `${name} · ${rtt} ms` : name
  // A direct connection is neutral too: green is for a live agent (docs/design/system.md, Status).
  const color = state === 'closed' ? 'error' : state !== 'open' ? 'warning' : 'neutral'
  const icon = state !== 'open' ? 'i-lucide-loader-circle' : kind === 'webrtc' ? 'i-lucide-radio' : kind === 'relay' ? 'i-lucide-route' : 'i-lucide-cable'
  return { icon, words, color, showWords: !(compact && state === 'open') }
}
