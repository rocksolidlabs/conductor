import type { AnnotationEdit, CaptureAnswer, CaptureCreateBody, CaptureFrom, CaptureReceipt, CaptureRect, NewAnnotation } from '~/utils/capture'
import type { Draft } from '~/composables/usePreviewBridge'
import { mintCommandId, sendCommand } from '~/utils/tasks'

/**
 * The capture routes (docs/protocol.md, Captures): a create (multipart: the capture's JSON, then its screenshot), an edit, a delete,
 * a capture's read and its screenshot, fetched with the bearer header and shown through an object URL. Each write mints its `opId`
 * and, when its reply is lost to the network, is sent once more with the same body, so the same id, which the server answers with
 * the first's receipt (sendCommand, as the task commands do); a refusal is a CommandRefused with the server's words.
 */

/** A region's or an element's rect scaled by factor and kept inside the image (rounding never takes it past an edge). */
function scaled(r: CaptureRect, factor: number, image?: { width: number; height: number }): CaptureRect {
  const x = r.x * factor
  const y = r.y * factor
  let width = r.width * factor
  let height = r.height * factor
  if (!image) return { x, y, width, height }
  const cx = Math.min(Math.max(0, x), image.width)
  const cy = Math.min(Math.max(0, y), image.height)
  width = Math.max(0, Math.min(x + width, image.width) - cx)
  height = Math.max(0, Math.min(y + height, image.height) - cy)
  return { x: cx, y: cy, width, height }
}

/**
 * A draft's create body: its moment as the capture's, each annotation in the order the person left it, every rect in the saved
 * screenshot's pixels (an element's box beside it in the page's CSS pixels, its report as the bridge gave it). `shot` is the
 * screenshot as saved (its factor from the raw pixels), or none with why. The label's revision is sent for an attempt's capture alone.
 */
export function captureBody(d: Draft, o: { opId: string; from: CaptureFrom; via: 'web' | 'desktop'; shot: { scale: number; width: number; height: number } | { why: string }; retakeOf?: string }): CaptureCreateBody {
  const m = d.moment
  const image = 'scale' in o.shot ? o.shot : null
  const link = 'url' in o.from
  const annotations: NewAnnotation[] = d.annotations.map((a) => {
    // Without a screenshot an element's rect is its box; with one, its box in the saved pixels, cut to them.
    const rect = image ? scaled(a.rect, image.scale, a.kind === 'region' ? image : undefined) : a.rect
    return { kind: a.kind, note: a.note, rect, ...(a.element ? { element: a.element } : {}), ...(a.observed ? { observed: a.observed } : {}) }
  })
  return {
    opId: o.opId,
    from: o.from,
    via: o.via,
    url: m.url,
    title: m.title,
    viewport: m.viewport,
    epoch: m.epoch,
    ...(m.revision ? { revision: m.revision } : {}),
    label: link || !m.label ? { commit: '', content: '', changed: 0 } : { commit: m.label.commit, content: m.label.content, changed: m.label.changed, ...(m.label.readAt ? { readAt: m.label.readAt } : {}) },
    selectedAt: m.at,
    imageHow: image ? d.how : 'none',
    ...(image ? { imageScale: d.ratio * image.scale } : { imageWhy: 'why' in o.shot ? o.shot.why : d.why || 'no screenshot' }),
    ...(o.retakeOf || d.retakeOf ? { retakeOf: o.retakeOf || d.retakeOf } : {}),
    annotations,
  }
}

/** A saved capture's annotations as an edit sends them: each by its place `k` as read, a new region by 0. */
export function editOf(annotations: ReadonlyArray<{ k: number; kind: 'element' | 'region'; note: string; rect: CaptureRect }>): AnnotationEdit[] {
  return annotations.map((a) => ({ k: a.k, kind: a.kind, note: a.note, rect: a.rect }))
}

export function useCaptures() {
  const { request, requestBlob } = useApi()
  const path = (id: string) => `/api/captures/${encodeURIComponent(id)}`

  /** POST /api/captures: the capture's JSON, then its screenshot when there is one. */
  async function create(body: CaptureCreateBody, image: Blob | null): Promise<{ receipt: CaptureReceipt; capture: CaptureAnswer }> {
    return sendCommand(
      (b: CaptureCreateBody) => {
        const form = new FormData()
        form.append('capture', JSON.stringify(b))
        if (image) form.append('image', new Blob([image], { type: 'image/png' }), 'screenshot.png')
        return request<{ receipt: CaptureReceipt; capture: CaptureAnswer }>('/api/captures', { method: 'POST', body: form })
      },
      body,
    )
  }

  async function edit(id: string, expectedRev: number, annotations: AnnotationEdit[]): Promise<{ receipt: CaptureReceipt; capture: CaptureAnswer }> {
    return sendCommand((b) => request<{ receipt: CaptureReceipt; capture: CaptureAnswer }>(`${path(id)}/edit`, { method: 'POST', body: b }), { opId: mintCommandId(), expectedRev, annotations })
  }

  async function remove(id: string): Promise<{ receipt: CaptureReceipt }> {
    return sendCommand((b) => request<{ receipt: CaptureReceipt }>(`${path(id)}/delete`, { method: 'POST', body: b }), { opId: mintCommandId() })
  }

  async function read(id: string): Promise<CaptureAnswer> {
    return request<CaptureAnswer>(path(id))
  }

  /** The captures of an attempt, newest first (their first annotation alone). */
  async function ofAttempt(run: string, task: string, attempt: number, after?: string): Promise<{ captures: CaptureAnswer[]; next?: string }> {
    return request('/api/captures', { query: { run, task, attempt: String(attempt), ...(after ? { after } : {}) } })
  }

  /** The kept screenshot, as a Blob to show through an object URL. */
  async function image(id: string): Promise<Blob> {
    return requestBlob(`${path(id)}/image`)
  }

  return { create, edit, remove, read, ofAttempt, image }
}
