import { ApiError } from './useApi'
import { sendTwice } from './useBrakeman'
import { HAND_MALFORMED, type HandPreview, type SentHandoff, asSentHandoff, olderServer, parsePreview } from '~/utils/brakemanSend'
import { mintCommandId } from '~/utils/tasks'

/**
 * A handoff package's three routes (issue #1012), as the admin: the preview, the send and the withdraw. Each reply is parsed by
 * utils/brakemanSend.ts and refused whole when it holds what the page does not know. A send carries the page's own op id and the
 * SHA-256 of the text the person read, and is asked again once with the same id only when its reply was lost to the network.
 */
export function useBrakemanSend() {
  const { request, requestText } = useApi()
  const path = (id: string, item: number) => `/api/brakeman/requests/${encodeURIComponent(id)}/items/${item}/hand`

  /**
   * The preview of a request's item: null where the server has no such route (an older server's mux answers 404 with no error code
   * of its own), so that no control is drawn. A record for another request or item is refused as a malformed reply.
   */
  async function preview(id: string, item: number): Promise<HandPreview | null> {
    let got: { text: string; bytes: Uint8Array }
    try {
      got = await requestText(path(id, item))
    } catch (e) {
      if (e instanceof ApiError && olderServer(e)) return null
      throw e
    }
    return parsePreview(got.text, got.bytes.length, id, item)
  }

  /** The record a send or a withdraw answered, or the malformed reply's refusal. */
  function record(out: { handoff?: unknown }): SentHandoff {
    const h = asSentHandoff(out?.handoff)
    if (!h) throw new Error(HAND_MALFORMED)
    return h
  }

  /** Sends the item's package, as the text whose SHA-256 the preview showed, signed `by` when given. */
  async function send(id: string, item: number, sha256: string, by: string): Promise<SentHandoff> {
    const body = { opId: mintCommandId(), sha256, ...(by ? { by } : {}) }
    const out = await sendTwice((b: typeof body) => request<{ handoff?: unknown }>(path(id, item), { method: 'POST', body: b }), body)
    const h = record(out)
    if (h.request !== id || h.item !== item) throw new Error(HAND_MALFORMED)
    return h
  }

  /** Withdraws a waiting package; one no longer waiting is answered as it stands. */
  async function withdraw(hid: string, by: string): Promise<SentHandoff> {
    const out = await request<{ handoff?: unknown }>(`/api/brakeman/handoffs/${encodeURIComponent(hid)}/withdraw`, { method: 'POST', body: by ? { by } : {} })
    const h = record(out)
    if (h.id !== hid) throw new Error(HAND_MALFORMED)
    return h
  }

  return { preview, send, withdraw }
}
