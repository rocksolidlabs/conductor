import { getCurrentInstance, markRaw, onBeforeUnmount, reactive, watch } from 'vue'
import {
  CHANGED_WORDS,
  MOVING_WORDS,
  NO_BRIDGE_WORDS,
  RESTLESS_WORDS,
  type CaptureRect,
  type CaptureViewport,
  type HeardMessage,
  type LabelRead,
  type Observed,
  type ViewportMode,
  parseBridgeMessage,
} from '~/utils/capture'
import { MAX_PREVIEW_USER_AGENT, PREVIEW_MARK, type ElementReport, type PreviewPage, type PreviewToWorkbench } from '~/utils/protocol'
import { decodePng, fromBase64, sameIn, type Pixels, type ScreenshotSource } from '~/utils/screenshot'

/**
 * The workbench's side of the preview bridge (docs/plans/browser-evidence.md, "The messages" and "One capture is one moment"): the
 * handshake with the framed page (a hello with a nonce of its own, 3 s with no ready meaning no bridge), one request in flight at a
 * time, and the drafts. Every message is taken through parseBridgeMessage alone (the frame's own window, the preview's origin, the
 * hello's nonce, 64 KiB, the schema). A draft is one moment: its screenshot, taken at its first selection and checked with a `look`
 * (the page's epoch moved while the pixels were taken: the element is read again by its selector and the pixels taken again, at most
 * twice, then the words), its URL, viewport with scroll, epoch, declared revision and the label read then. A later selection joins it
 * only at that same moment and, where there are pixels, with a new frame the same as the screenshot inside every annotation's box and
 * the new element's; otherwise it starts a new draft with its own screenshot, the first kept beside it. An element whose box differed
 * between its screenshot and the frame after it, or that reports animations playing, is kept `stable: false`; without pixels no
 * stability is claimed.
 */

/** A draft's moment: what every element of it was selected at. */
export interface Moment {
  url: string
  title: string
  viewport: CaptureViewport
  epoch: number
  revision: string
  /** The label the pane read at the selection; null for a link's capture. */
  label: LabelRead | null
  /** The first selection's time (ISO). */
  at: string
}

/** One of a draft's annotations, its rect in the screenshot's pixels (the element's box in CSS pixels without a screenshot). */
export interface DraftAnnotation {
  key: number
  kind: 'element' | 'region'
  note: string
  rect: CaptureRect
  element?: ElementReport
  observed?: Observed
  /** What the pane says of it: that it was moving. */
  words: string
}

export interface Draft {
  id: number
  moment: Moment
  /** The screenshot's raw pixels, null without one. */
  shot: Pixels | null
  how: 'desktop' | 'tab' | 'none'
  /** Why there is no screenshot. */
  why: string
  /** The screenshot's pixels per CSS pixel of the page. */
  ratio: number
  annotations: DraftAnnotation[]
  /** What the pane says of it as a whole: that it started because the page changed. */
  words: string[]
  /** Whether a later selection may join it: not once the frame said hello again, or once it was saved. */
  open: boolean
  /** The capture this one retakes (Capture again), '' for none: the create sends it as `retakeOf`. */
  retakeOf: string
}

/** What the session needs of the pane. */
export interface BridgeDeps {
  /** Hears the workbench window's messages; returns what stops hearing them. */
  listen(heard: (e: HeardMessage) => void): () => void
  /** The frame's window now and the preview's origin (from the address the pane gave the frame). */
  end(): { window: { postMessage(message: unknown, targetOrigin: string): void } | null; origin: string }
  /** The label read now (its commit, content and count), null for a link. */
  label(): Promise<LabelRead | null>
  /** The screenshot source, asked for at the first screenshot of a session. */
  source(): Promise<ScreenshotSource>
  /** The frame's box in the window's CSS pixels. */
  rect(): CaptureRect
  /** The device mode and the frame's scale (its CSS pixels to the window's). */
  view(): { mode: ViewportMode; scale: number }
  /** What a draft without the bridge records of the page: the frame's address and its size. */
  fallback(): { url: string; width: number; height: number }
  /**
   * Called before every frame is read, and awaited: the pane draws nothing of its own over the frame's box while pixels are taken
   * (the editor over a phone's frame goes, a size a device mode changed is laid out) and waits until that is painted.
   */
  beforeFrame?(): Promise<void>
  /** The frame element now: a source made for another (Refresh and Capture again replace it) is ended and made again. */
  frameElement?(): unknown
  now?(): Date
  nonce?(): string
  setTimer?(fn: () => void, ms: number): unknown
  clearTimer?(id: unknown): void
}

/** How long a hello waits for its ready, and a look or a describe for its answer. */
export const HELLO_MS = 3000
/** The hello is said again at these times within its 3 s, for a bridge whose script starts after the frame's load. */
export const HELLO_AGAIN_MS = [1000, 2000] as const
export const ANSWER_MS = 3000
/** How many annotations a draft holds (dispatch.MaxCaptureAnnotations). */
export const MAX_ANNOTATIONS = 20

type Waiting = { t: 'select' | 'describe' | 'look'; resolve(m: PreviewToWorkbench | null): void; timer?: unknown }

function mintNonce(): string {
  const b = new Uint8Array(16)
  globalThis.crypto.getRandomValues(b)
  return Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('')
}

/** Whether two moments are the same: URL, viewport (mode, size, scale, ratio, scheme, agent, scroll), epoch, revision and the label's revision. */
export function sameMoment(a: Moment, b: Moment): boolean {
  const va = a.viewport
  const vb = b.viewport
  return (
    a.url === b.url &&
    a.epoch === b.epoch &&
    a.revision === b.revision &&
    va.mode === vb.mode &&
    va.width === vb.width &&
    va.height === vb.height &&
    va.scale === vb.scale &&
    va.dpr === vb.dpr &&
    va.colorScheme === vb.colorScheme &&
    va.userAgent === vb.userAgent &&
    va.scrollX === vb.scrollX &&
    va.scrollY === vb.scrollY &&
    (a.label?.commit ?? '') === (b.label?.commit ?? '') &&
    (a.label?.content ?? '') === (b.label?.content ?? '')
  )
}

/** The store's viewport from the bridge's (its size made whole pixels) and the pane's mode and scale. */
export function viewportOf(p: PreviewPage['viewport'], view: { mode: ViewportMode; scale: number }): CaptureViewport {
  return {
    mode: view.mode,
    width: Math.round(p.width),
    height: Math.round(p.height),
    scale: view.scale,
    dpr: p.dpr,
    colorScheme: p.colorScheme,
    userAgent: p.userAgent,
    scrollX: p.scrollX,
    scrollY: p.scrollY,
  }
}

/** An element's box in a screenshot's pixels. */
export function boxIn(e: Pick<ElementReport, 'box'>, ratio: number): CaptureRect {
  return { x: e.box.x * ratio, y: e.box.y * ratio, width: e.box.width * ratio, height: e.box.height * ratio }
}

/** The test seam (C9): the screenshot source a test hands in; whatever else a test sees still goes through parseBridgeMessage. */
export const previewSeam: { source: ScreenshotSource | null } = { source: null }

/**
 * In a browser, read once as this module loads from `window.__conductorPreviewSeam` (put there by a Playwright init script alone)
 * and taken off the window: a later write, by the page or anything else, is never read. It holds `frame(rect)`, a PNG of the
 * frame's box in base64, and nothing else.
 */
function seamFromWindow(): ScreenshotSource | null {
  const w = globalThis as unknown as { window?: Record<string, unknown> }
  if (!w.window) return null
  const raw = w.window.__conductorPreviewSeam as { frame?: (r: CaptureRect) => Promise<unknown> } | undefined
  try {
    delete w.window.__conductorPreviewSeam
  } catch {
    /* a property that will not go: it is still never read again */
  }
  if (!raw || typeof raw.frame !== 'function') return null
  const frame = raw.frame.bind(raw)
  return {
    how: 'tab',
    async frame(rect) {
      const png = await frame({ x: Math.round(rect.x), y: Math.round(rect.y), width: Math.round(rect.width), height: Math.round(rect.height) })
      return typeof png === 'string' && png ? decodePng(fromBase64(png)) : null
    },
    stop() {},
  }
}
previewSeam.source = seamFromWindow()

/** A session with one framed page: its handshake, its requests and its drafts. */
export function createPreviewSession(deps: BridgeDeps) {
  const now = () => (deps.now ? deps.now() : new Date())
  const setTimer = deps.setTimer ?? ((fn: () => void, ms: number) => setTimeout(fn, ms))
  const clearTimer = deps.clearTimer ?? ((id: unknown) => clearTimeout(id as ReturnType<typeof setTimeout>))
  const state = reactive({
    /** The handshake: none yet, a hello waiting, the bridge ready, or absent (no ready within 3 s). */
    bridge: 'none' as 'none' | 'hello' | 'ready' | 'absent',
    /** What the pane says of the bridge: that the page does not load it. */
    words: '',
    /** What is under way: a selection, a describe, a screenshot; '' for nothing. */
    busy: '' as '' | 'select' | 'describe' | 'shot',
    /** The last refusal's words (a page that would not hold still, a selector that matched nothing, a full draft). */
    refusal: '',
    drafts: [] as Draft[],
    /** The capture a Capture again under way retakes: every draft it starts names it. */
    retakeOf: '',
  })
  let nonce = ''
  let waiting: Waiting | null = null
  let helloTimer: unknown = null
  let helloAgain: unknown[] = []
  let sourceFor: unknown = undefined
  let source: Promise<ScreenshotSource> | null = null
  let ids = 0
  let keys = 0

  const off = deps.listen((e) => {
    const end = deps.end()
    const m = parseBridgeMessage(e, { source: end.window, origin: end.origin, nonce })
    if (!m) return
    if (m.t === 'ready') {
      if (state.bridge !== 'hello') return
      if (helloTimer !== null) clearTimer(helloTimer)
      helloTimer = null
      state.bridge = 'ready'
      state.words = ''
      return
    }
    const w = waiting
    if (!w) return
    const fits = (w.t === 'select' && (m.t === 'selected' || m.t === 'cancelled')) || (w.t === 'describe' && m.t === 'described') || (w.t === 'look' && m.t === 'looked')
    if (!fits) return
    finish(m)
  })

  function finish(m: PreviewToWorkbench | null) {
    const w = waiting
    if (!w) return
    waiting = null
    if (w.timer !== undefined) clearTimer(w.timer)
    w.resolve(m)
  }

  function post(m: Record<string, unknown>): boolean {
    const end = deps.end()
    if (!end.window || !end.origin || !nonce) return false
    end.window.postMessage({ conductor: PREVIEW_MARK, nonce, ...m }, end.origin)
    return true
  }

  /** Asks the bridge, one request at a time: null when one is already in flight, the frame is gone, or no answer came in time. */
  function ask(t: Waiting['t'], extra: Record<string, unknown> = {}): Promise<PreviewToWorkbench | null> {
    if (waiting || state.bridge !== 'ready') return Promise.resolve(null)
    return new Promise((resolve) => {
      waiting = { t, resolve }
      // A selection waits for the person; a look and a describe for the page, within their bound.
      if (t !== 'select') waiting.timer = setTimer(() => finish(null), ANSWER_MS)
      if (!post({ t, ...extra })) finish(null)
    })
  }

  function stopHello() {
    if (helloTimer !== null) clearTimer(helloTimer)
    helloTimer = null
    for (const t of helloAgain) clearTimer(t)
    helloAgain = []
  }

  /**
   * The frame (re)loaded: a hello with a new nonce, said again twice within its 3 s (a bridge whose script starts after the frame's
   * load hears a later one); whatever was asked of the page before is dropped, and no draft is joined again.
   */
  function hello() {
    finish(null)
    stopHello()
    nonce = deps.nonce ? deps.nonce() : mintNonce()
    for (const d of state.drafts) d.open = false
    state.bridge = 'hello'
    state.words = ''
    if (!post({ t: 'hello' })) {
      state.bridge = 'absent'
      state.words = NO_BRIDGE_WORDS
      return
    }
    helloAgain = HELLO_AGAIN_MS.map((ms) => setTimer(() => state.bridge === 'hello' && post({ t: 'hello' }), ms))
    helloTimer = setTimer(() => {
      helloTimer = null
      if (state.bridge === 'hello') {
        state.bridge = 'absent'
        state.words = NO_BRIDGE_WORDS
      }
    }, HELLO_MS)
  }

  /** The screenshot source, made for the frame element there now: one made for an element since replaced is ended first. */
  function screenshots(): Promise<ScreenshotSource> {
    const el = deps.frameElement?.()
    if (source && el !== sourceFor) void release()
    if (!source) {
      sourceFor = el
      source = deps.source()
    }
    return source
  }

  /**
   * The frame's pixels now, once the pane has cleared and painted what it draws over the frame's box. A source that gives none is
   * ended, so the next capture asks for one again rather than waiting on it.
   */
  async function shoot(src: ScreenshotSource) {
    await deps.beforeFrame?.()
    const px = await src.frame(deps.rect())
    if (!px && src.how !== 'none' && source && (await source) === src) void release()
    return px
  }

  /** Ends the screenshot source (Save, Escape, the pane's close): the next capture asks for one again. */
  async function release() {
    const s = source
    source = null
    sourceFor = undefined
    if (s) (await s).stop()
  }

  /** The draft a selection may join: the last one still open. */
  const current = () => [...state.drafts].reverse().find((d) => d.open) ?? null

  function momentOf(page: PreviewPage, label: LabelRead | null, at: string): Moment {
    return { url: page.url, title: page.title, viewport: viewportOf(page.viewport, deps.view()), epoch: page.epoch, revision: page.revision, label, at }
  }

  function observedOf(d: Draft, page: PreviewPage, at: string, stable: boolean | undefined): Observed {
    const m = d.moment
    return {
      at,
      url: m.url,
      title: page.title,
      viewport: m.viewport,
      epoch: m.epoch,
      ...(m.revision ? { revision: m.revision } : {}),
      ...(stable === undefined ? {} : { stable }),
      label: { commit: m.label?.commit ?? '', content: m.label?.content ?? '' },
    }
  }

  function join(d: Draft, el: ElementReport, page: PreviewPage, at: string, stable: boolean | undefined) {
    if (d.annotations.length >= MAX_ANNOTATIONS) {
      state.refusal = `a capture holds at most ${MAX_ANNOTATIONS} annotations: save this one, then select again for a new capture`
      return
    }
    const s = stable === undefined ? undefined : stable && el.state.motion === 0
    d.annotations.push({ key: ++keys, kind: 'element', note: '', rect: boxIn(el, d.ratio), element: el, observed: observedOf(d, page, at, s), words: s === false ? MOVING_WORDS : '' })
  }

  /**
   * A new draft at the element's moment. Its screenshot is `first` (a frame that differed from the draft before) or a frame taken
   * now; a look follows it, and when the page's epoch moved meanwhile the element is read again by its selector and the pixels taken
   * again, at most twice, then the words and no draft. Its stability: the element's box in the screenshot and in the frame after it.
   */
  async function startDraft(m: { element: ElementReport } & PreviewPage, label: LabelRead | null, at: string, first: Pixels | null, words: string[]) {
    const src = await screenshots()
    let page: { element: ElementReport } & PreviewPage = m
    let shot = first
    if (src.how !== 'none') {
      for (let tries = 0; ; tries++) {
        shot ??= await shoot(src)
        if (!shot) break
        const looked = await ask('look')
        if (!looked || looked.t !== 'looked' || looked.epoch === page.epoch) break
        if (tries >= 2) {
          state.refusal = RESTLESS_WORDS
          return
        }
        // The page moved while the pixels were taken: its element read again now, then the pixels.
        const again = await ask('describe', { selector: page.element.selector })
        if (!again || again.t !== 'described' || !again.element) {
          state.refusal = RESTLESS_WORDS
          return
        }
        page = { ...again, element: again.element }
        label = await deps.label()
        shot = null
      }
    }
    const moment = momentOf(page, label, at)
    const ratio = shot && page.viewport.width > 0 ? shot.width / page.viewport.width : 1
    const d: Draft = reactive({ id: ++ids, moment, shot: shot && markRaw(shot), how: shot ? src.how : 'none', why: shot ? '' : src.why || 'no screenshot could be taken', ratio, annotations: [], words, open: true, retakeOf: state.retakeOf }) as Draft
    let stable: boolean | undefined
    if (shot) {
      const next = await shoot(src)
      stable = next ? sameIn(shot, next, boxIn(page.element, ratio)) : undefined
    }
    state.drafts.push(d)
    join(state.drafts[state.drafts.length - 1]!, page.element, page, at, stable)
  }

  /** An element the bridge selected or described: it joins the open draft at the same moment and pixels, or starts a new one. */
  async function take(m: { element: ElementReport } & PreviewPage) {
    state.busy = 'shot'
    try {
      const label = await deps.label()
      const at = now().toISOString()
      const moment = momentOf(m, label, at)
      const d = current()
      if (d && sameMoment(d.moment, moment)) {
        if (!d.shot) return join(d, m.element, m, at, undefined)
        const src = await screenshots()
        const frame = await shoot(src)
        if (!frame) return join(d, m.element, m, at, undefined)
        const boxes = [...d.annotations.map((a) => a.rect), boxIn(m.element, d.ratio)]
        if (boxes.every((b) => sameIn(d.shot!, frame, b))) return join(d, m.element, m, at, true)
        return await startDraft(m, label, at, frame, [CHANGED_WORDS])
      }
      await startDraft(m, label, at, null, d ? [CHANGED_WORDS] : [])
    } finally {
      state.busy = ''
    }
  }

  /** Selection mode in the page: the element the person picks joins a draft or starts one; Escape there or cancel() ends it. */
  async function select() {
    if (state.bridge !== 'ready' || state.busy || waiting) return
    state.refusal = ''
    state.busy = 'select'
    const m = await ask('select')
    state.busy = ''
    if (m?.t === 'selected') await take(m)
  }

  /** Select by selector: the element it names (one no hit test returns among them) joins or starts a draft as a selection does. */
  async function describe(selector: string) {
    if (state.bridge !== 'ready' || state.busy || waiting || !selector) return
    state.refusal = ''
    state.busy = 'describe'
    const m = await ask('describe', { selector })
    state.busy = ''
    if (m?.t !== 'described') return
    if (!m.element) {
      state.refusal = 'no element of the page matches that selector'
      return
    }
    await take({ ...m, element: m.element })
  }

  /**
   * Capture again (the plan's "A candidate's preview"): each element of an earlier capture described again by its selector on this
   * page, in its order, with its note, into a new draft that names the earlier as the capture it retakes; one that is not on this page
   * is said. Nothing joins a draft from before it. The pane opens the same URL and device mode first.
   */
  async function retake(of: string, elements: ReadonlyArray<{ selector: string; note: string }>) {
    if (state.bridge !== 'ready' || state.busy || waiting) return
    state.refusal = ''
    for (const d of state.drafts) d.open = false
    state.retakeOf = of
    const missing: string[] = []
    try {
      for (const e of elements) {
        state.busy = 'describe'
        const m = await ask('describe', { selector: e.selector })
        state.busy = ''
        if (m?.t !== 'described' || !m.element) {
          missing.push(e.selector)
          continue
        }
        await take({ ...m, element: m.element })
        const d = current()
        const a = d?.annotations[d.annotations.length - 1]
        if (d?.retakeOf === of && a && a.element?.selector === m.element.selector && !a.note) a.note = e.note
      }
    } finally {
      state.retakeOf = ''
      state.busy = ''
    }
    if (missing.length) state.refusal = `not on this page now, so not retaken: ${missing.join(', ')}`
  }

  /** Leaves selection mode: the bridge answers cancelled, which ends the selection waiting for it. */
  function cancel() {
    if (waiting?.t === 'select') post({ t: 'cancel' })
  }

  /** A draft with the screenshot and no element: the page has no bridge, or the person captures without selecting. */
  async function snap() {
    if (state.busy) return
    state.refusal = ''
    state.busy = 'shot'
    try {
      const label = await deps.label()
      const src = await screenshots()
      const shot = await shoot(src)
      const f = deps.fallback()
      const view = deps.view()
      const ua = typeof navigator === 'undefined' ? '' : navigator.userAgent.slice(0, MAX_PREVIEW_USER_AGENT)
      const dpr = typeof window === 'undefined' ? 1 : window.devicePixelRatio || 1
      const moment: Moment = {
        url: f.url,
        title: '',
        viewport: { mode: view.mode, width: Math.round(f.width), height: Math.round(f.height), scale: view.scale, dpr, colorScheme: 'light', userAgent: ua, scrollX: 0, scrollY: 0 },
        epoch: 0,
        revision: '',
        label,
        at: now().toISOString(),
      }
      state.drafts.push({ id: ++ids, moment, shot: shot && markRaw(shot), how: shot ? src.how : 'none', why: shot ? '' : src.why || 'no screenshot could be taken', ratio: shot && f.width > 0 ? shot.width / f.width : 1, annotations: [], words: [], open: false, retakeOf: '' })
    } finally {
      state.busy = ''
    }
  }

  /** A region drawn on a draft's screenshot, in its pixels. */
  function addRegion(d: Draft, rect: CaptureRect) {
    if (!d.shot || d.annotations.length >= MAX_ANNOTATIONS) return
    d.annotations.push({ key: ++keys, kind: 'region', note: '', rect, words: '' })
  }

  /** Drops a draft (saved, or thrown away), and its pixels with it. */
  function drop(d: Draft) {
    const i = state.drafts.findIndex((x) => x.id === d.id)
    if (i >= 0) state.drafts.splice(i, 1)
  }

  function close() {
    finish(null)
    stopHello()
    off()
    void release()
  }

  /** Waits for the hello under way to be answered or given up, at most its 3 s and a little. */
  function settled(): Promise<void> {
    if (state.bridge !== 'hello') return Promise.resolve()
    // The hello's own timer ends it (absent) at its 3 s, so this waits no longer than that.
    return new Promise((resolve) => {
      const stop = watch(
        () => state.bridge,
        (b) => {
          if (b === 'hello') return
          stop()
          resolve()
        },
        { flush: 'sync' },
      )
    })
  }

  /** Capture: selection mode where the page answers the bridge (once its hello is answered or given up), the screenshot alone where it does not. */
  async function start() {
    await settled()
    if (state.bridge === 'ready') await select()
    else await snap()
  }

  return { state, hello, select, describe, cancel, retake, snap, start, addRegion, drop, release, close }
}

export type PreviewSession = ReturnType<typeof createPreviewSession>

/** The session for a component: closed with it. */
export function usePreviewBridge(deps: BridgeDeps): PreviewSession {
  const s = createPreviewSession(deps)
  if (getCurrentInstance()) onBeforeUnmount(() => s.close())
  return s
}
