import type { CaptureAnswer, CaptureDocument } from '~/utils/capture'
import type { CaptureSendView, SendKind, SendForm } from '~/utils/captureSend'
import { sendBody } from '~/utils/captureSend'
import { mintCommandId } from '~/utils/tasks'
import { sendTwice } from './useBrakeman'

/**
 * A capture's sends over the server's routes (docs/protocol.md, "Captures"): a send to a ticket or to Brakeman, a send's cancel, a
 * send with the document it froze, and a ticket's captures. Every write mints its opId once and sends it again once when the reply is
 * lost (sendTwice), so a resend is answered by its receipt.
 */

/** A capture's document and a capture as the routes answer them: utils/capture.ts's. */
export type CaptureDocumentView = CaptureDocument
export type CaptureView = CaptureAnswer

/** A send's reply: its receipt, the send, and for a refused first try the desk's words. */
export interface SendReply {
  receipt: { id: string; status: string; repeat?: boolean }
  send: CaptureSendView
}

/** A cancel's reply. */
export interface CancelReply extends SendReply {
  cancel: { asked: boolean; done: boolean; words?: string }
}

const enc = encodeURIComponent

export function useCaptureSends() {
  const { request } = useApi()
  return {
    /** The capture whole, with its sends. */
    capture: (id: string) => request<CaptureView>(`/api/captures/${enc(id)}`),
    /** The live captures a send was sent to the ticket of, newest first (their first annotation alone). */
    ticketCaptures: (key: string) => request<{ captures: CaptureView[]; next?: string }>(`/api/captures?ticket=${enc(key)}`).then((r) => r.captures ?? []),
    /** A send of the capture with the document it froze. */
    frozen: (capture: string, send: string) => request<{ send: CaptureSendView; document: CaptureDocumentView }>(`/api/captures/${enc(capture)}/sends/${enc(send)}`),
    /**
     * Sends the capture at rev to the destination of kind (sendBody): the reply, or the server's refusal (an ApiError whose body holds
     * the receipt and, for the desk's refusal of a first try, the send refused).
     */
    send: (capture: string, rev: number, kind: SendKind, form: SendForm, byName?: string) => {
      const b = sendBody(kind, form)
      if ('problem' in b) return Promise.reject(new Error(b.problem))
      const body = { opId: mintCommandId(), rev, to: b.to, ...(byName ? { byName } : {}) }
      return sendTwice((x: typeof body) => request<SendReply>(`/api/captures/${enc(capture)}/send`, { method: 'POST', body: x }), body)
    },
    /** Cancels a send to Brakeman. */
    cancel: (capture: string, send: string) => {
      const body = { opId: mintCommandId() }
      return sendTwice((x: typeof body) => request<CancelReply>(`/api/captures/${enc(capture)}/sends/${enc(send)}/cancel`, { method: 'POST', body: x }), body)
    },
  }
}
