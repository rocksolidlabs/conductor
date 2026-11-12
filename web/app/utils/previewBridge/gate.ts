import { MAX_PREVIEW_URL, PREVIEW_MARK, parsePreviewMessage, type PreviewToBridge, type PreviewToWorkbench } from '../protocol'

/**
 * The bridge's gate (docs/protocol.md, Preview bridge), the one place a message from another window is taken: only an event the
 * browser delivered (isTrusted), from the page's parent window, at one of the workbench origins the server wrote into the bridge
 * (compared exactly), whose data parsePreviewMessage takes as a message to the bridge, before anything else reads it; after a
 * hello, only from that hello's origin with its nonce. Anything else is dropped: no answer, no state changed. Every answer is a
 * message the bridge's own writer made that the same parser takes as a message to the workbench, posted to the parent window
 * alone, addressed to the origin of the hello it answers, never to "*".
 */
export interface Parent {
  postMessage(message: unknown, targetOrigin: string): void
}

export interface Incoming {
  isTrusted: boolean
  source: unknown
  origin: string
  data: unknown
}

export type Answer = PreviewToWorkbench extends infer M ? (M extends unknown ? Omit<M, 'conductor' | 'nonce'> : never) : never

export interface Gate {
  /** The message when the gate takes it, else null (dropped, unanswered). */
  take(e: Incoming): PreviewToBridge | null
  /** Posts an answer to the parent at the origin of the current hello; false, and nothing posted, before a hello or for a message the parser refuses. */
  answer(m: Answer): boolean
}

/** The origins the server wrote into the bridge: strings of at most MAX_PREVIEW_URL bytes, anything else none at all. */
export function originsOf(v: unknown): string[] {
  if (!Array.isArray(v)) return []
  return v.filter((o): o is string => typeof o === 'string' && o.length > 0 && o.length <= MAX_PREVIEW_URL && o !== '*' && o !== 'null')
}

export function createGate(origins: readonly string[], parent: Parent): Gate {
  const allowed = [...origins]
  let nonce = ''
  let origin = ''
  return {
    take(e) {
      if (e.isTrusted !== true || e.source !== parent || !allowed.includes(e.origin)) return null
      // The parser takes only the workbench's types, and answers null, never an exception, for anything else. A later hello from
      // the parent replaces the first (the workbench reloaded); any other message must be from the hello's origin.
      const m = parsePreviewMessage(e.data, 'bridge', nonce)
      if (!m || (m.t !== 'hello' && e.origin !== origin)) return null
      if (m.t === 'hello') {
        nonce = m.nonce
        origin = e.origin
      }
      return m as PreviewToBridge
    },
    answer(m) {
      if (!nonce || !origin) return false
      const out = { ...m, conductor: PREVIEW_MARK, nonce }
      if (!parsePreviewMessage(out, 'workbench', nonce)) return false
      parent.postMessage(out, origin)
      return true
    },
  }
}
