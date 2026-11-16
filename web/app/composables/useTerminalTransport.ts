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
   * A connection made with the workbench token waits, while the page to connect shows, until a token is taken: a terminal hidden
   * under that page asks nothing with the missing or refused token. A link's token is the caller's own and is never held back.
   */
  function gated<T extends TerminalTransport>(t: T, token: string): T {
    if (token !== admin.token.value) return t
    const open = t.connect.bind(t)
    t.connect = async (hello) => {
      await admin.connected()
      return open(hello)
    }
    return t
  }

  function create(spec: TransportSpec): TerminalTransport {
    const base = spec.server ? wsBaseOf(spec.server) : wsBase.value
    const url = `${base}/ws/sessions/${encodeURIComponent(spec.sessionId)}?token=${encodeURIComponent(spec.token)}`
    const name = spec.name ?? identity.name.value
    if (spec.kind === 'hosted') return gated(new WebRTCTransport(url, { forceRelay: spec.forceRelay, name, chatOnly: spec.chatOnly }), spec.token)
    return gated(new WebSocketTransport(url, { name, chatOnly: spec.chatOnly }), spec.token)
  }

  /**
   * The connection of a run's desk at this server, or at `server` (`GET /ws/runs/{run}/desk`, docs/protocol.md, Decision links): a
   * run decision link's lane, whether or not a member of the run lives. At a switchyard the desk is a hosted session's id there,
   * which `create` reaches as it reaches a member's.
   */
  function desk(runId: string, token: string, server = '', name?: string): TerminalTransport {
    const base = server ? wsBaseOf(server) : wsBase.value
    return gated(new WebSocketTransport(`${base}/ws/runs/${encodeURIComponent(runId)}/desk?token=${encodeURIComponent(token)}`, { name: name ?? identity.name.value }), token)
  }

  return { create, desk }
}
