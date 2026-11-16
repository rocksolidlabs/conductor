import type { TerminalTransport } from '~/utils/transport/types'
import { WebSocketTransport } from '~/utils/transport/ws'
import { WebRTCTransport } from '~/utils/transport/webrtc'
import type { SessionKind } from './useSessions'
import { wsBaseOf } from '~/utils/invite'

export interface TransportSpec {
  sessionId: string
  token: string
  kind: SessionKind
  forceRelay?: boolean
  /** Display name sent in the hello; defaults to the stored identity. */
  name?: string
  /** The server to connect to (`https://host`, utils/invite.ts joinServer) instead of this page's: an invite's switchyard. */
  server?: string
  /** A quiet connection for a run's chat alone (`hello.chatOnly`): no scrollback or output, not a viewer of the session. */
  chatOnly?: boolean
}

/** Builds the right transport for a session kind. Each call creates a fresh connection object. */
export function useTerminalTransport() {
  const { wsBase } = useApiBase()

  const identity = useIdentity()
  const admin = useWorkbenchToken()

  /**
   * A connection made with the workbench token (`token` is the one held as it is made) waits, while the page to connect shows, until
   * a token the server took is held, and opens with the token held THEN: its address is made as it opens (`address`), never with the
   * refused or missing one it was made under. Closed while it waited, it does not open at all; the page gone with no token taken, it
   * closes as a connection that could not open does. A link's token is the caller's own and is never held back.
   */
  function held<T extends TerminalTransport>(token: string, address: (t: string) => string, make: (url: string | (() => string)) => T): T {
    if (token !== admin.token.value) return make(address(token))
    const t = make(() => address(admin.token.value))
    const open = t.connect.bind(t)
    t.connect = async (hello) => {
      if (admin.gated.value) {
        const taken = await admin.connected().catch(() => false)
        if (t.state.value === 'closed') throw new Error('closed while the page to connect was shown')
        if (!taken) {
          t.close()
          throw new Error('not connected to the server')
        }
      }
      return open(hello)
    }
    return t
  }

  function create(spec: TransportSpec): TerminalTransport {
    const base = spec.server ? wsBaseOf(spec.server) : wsBase.value
    const address = (token: string) => `${base}/ws/sessions/${encodeURIComponent(spec.sessionId)}?token=${encodeURIComponent(token)}`
    const name = spec.name ?? identity.name.value
    if (spec.kind === 'hosted') return held(spec.token, address, (url) => new WebRTCTransport(url, { forceRelay: spec.forceRelay, name, chatOnly: spec.chatOnly }))
    return held(spec.token, address, (url) => new WebSocketTransport(url, { name, chatOnly: spec.chatOnly }))
  }

  /**
   * The connection of a run's desk at this server, or at `server` (`GET /ws/runs/{run}/desk`, docs/protocol.md, Decision links): a
   * run decision link's lane, whether or not a member of the run lives. At a switchyard the desk is a hosted session's id there,
   * which `create` reaches as it reaches a member's.
   */
  function desk(runId: string, token: string, server = '', name?: string): TerminalTransport {
    const base = server ? wsBaseOf(server) : wsBase.value
    return held(token, (t) => `${base}/ws/runs/${encodeURIComponent(runId)}/desk?token=${encodeURIComponent(t)}`, (url) => new WebSocketTransport(url, { name: name ?? identity.name.value }))
  }

  return { create, desk }
}
