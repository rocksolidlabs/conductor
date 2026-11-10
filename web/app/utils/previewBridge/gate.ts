import { MAX_PREVIEW_URL, PREVIEW_MARK, parsePreviewMessage, type PreviewToBridge, type PreviewToWorkbench } from '../protocol'

/**
 * The bridge's gate (docs/protocol.md, Preview bridge): a message is taken only when its source is the page's parent window, its
 * origin one of the workbench origins the server wrote into the bridge, and it parses by its schema within MAX_PREVIEW_TO_BRIDGE;
 * after a hello, it must carry the nonce that hello gave. Anything else is dropped with no answer. Every answer is posted to the
 * parent window alone, addressed to the origin of the hello it answers, never to "*".
 */
export interface Parent {
  postMessage(message: unknown, targetOrigin: string): void
}

export interface Incoming {
  source: unknown
  origin: string
  data: unknown
}

export type Answer = PreviewToWorkbench extends infer M ? (M extends unknown ? Omit<M, 'conductor' | 'nonce'> : never) : never

export interface Gate {
  /** The message when the gate takes it, else null (dropped, unanswered). */
  take(e: Incoming): PreviewToBridge | null
  /** Posts an answer to the parent at the origin of the current hello; nothing before a hello. */
  answer(m: Answer): void
}

/** The origins the server wrote into the bridge: strings of at most MAX_PREVIEW_URL bytes, anything else none at all. */
export function originsOf(v: unknown): string[] {
  if (!Array.isArray(v)) return []
  return v.filter((o): o is string => typeof o === 'string' && o.length > 0 && o.length <= MAX_PREVIEW_URL && o !== '*' && o !== 'null')
}

export function createGate(origins: readonly string[], parent: Parent): Gate {
  let nonce = ''
  let origin = ''
  return {
    take(e) {
      if (e.source !== parent || !origins.includes(e.origin)) return null
      // A later hello from the parent replaces the first (the workbench reloaded); any other must be from the hello's origin.
      let m: ReturnType<typeof parsePreviewMessage>
      try {
        m = parsePreviewMessage(e.data, nonce)
      } catch {
        return null
      }
      if (!m || !['hello', 'select', 'describe', 'look', 'cancel'].includes(m.t)) return null
      if (m.t === 'hello') {
        nonce = m.nonce
        origin = e.origin
      } else if (e.origin !== origin) {
        return null
      }
      return m as PreviewToBridge
    },
    answer(m) {
      if (!nonce || !origin) return
      parent.postMessage({ ...m, conductor: PREVIEW_MARK, nonce }, origin)
    },
  }
}
