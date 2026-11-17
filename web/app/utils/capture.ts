import { parsePreviewMessage, type ElementReport, type PreviewToWorkbench } from './protocol'

/**
 * A person's captures of a preview (docs/plans/browser-evidence.md; docs/protocol.md, Captures and The preview label): the types the
 * capture routes answer and take, the one gate a bridge's message passes before the workbench reads it (parseBridgeMessage), the
 * label's words, and what changed in a capture since a send froze it (diffSinceSent). Pure: the pane, the editor and the sends import it.
 */

/** A rectangle: an element's box in the page's CSS pixels, an annotation's in the screenshot's pixels. */
export interface CaptureRect {
  x: number
  y: number
  width: number
  height: number
}

/** How the frame was shown: Fit the pane, or a device's size scaled down to fit it. */
export type ViewportMode = 'fit' | 'desktop' | 'tablet' | 'phone'

/** A device mode's frame in CSS pixels (Fit has none: the pane's own size). */
export const DEVICE_SIZES: Record<Exclude<ViewportMode, 'fit'>, { width: number; height: number }> = {
  desktop: { width: 1280, height: 800 },
  tablet: { width: 820, height: 1180 },
  phone: { width: 390, height: 844 },
}

/** The store's viewport (dispatch.Viewport): the pane's mode and the frame's scale, the rest as the bridge read the page. */
export interface CaptureViewport {
  mode: ViewportMode
  width: number
  height: number
  scale: number
  dpr: number
  colorScheme: 'light' | 'dark'
  userAgent: string
  scrollX: number
  scrollY: number
}

/** The label route's answer (GET /api/runs/{run}/tasks/{task}/attempts/{n}/preview[?seq=N]). */
export interface PreviewLabel {
  /** The preview as the admin opens it, the tree's secret put in: the frame's address and New tab's. */
  url: string
  tree: 'worktree' | 'preview'
  /** A candidate's checkout's number (`?seq=`). */
  seq?: number
  commit: string
  content: string
  changed: number
  changedPaths: string[]
  changedCut: boolean
  mutable: boolean
  state: string
  /** A checkout's candidate, `<run>/<task>/<n>@<commit>`. */
  candidate?: string
  where: string
  readAt: string
  /** Why the tree's changes were not read now (the read failed or ran out of its time). */
  unread?: string
}

/** What the pane read of the label at a selection: the moment's revision (dispatch.TreeRead as the create takes it). */
export interface LabelRead {
  commit: string
  content: string
  changed: number
  readAt?: string
}

/** Where a capture was taken: an attempt's preview (its checkout's `seq` for a candidate) or a link. */
export type CaptureFrom = { run: string; task: string; attempt: number; seq?: number } | { url: string }

/** An element annotation's moment (dispatch.Observed). */
export interface Observed {
  at: string
  url: string
  title: string
  viewport: CaptureViewport
  epoch: number
  revision?: string
  stable?: boolean
  label: { commit: string; content: string }
}

/** An annotation of a create (captureAnnotation): an element's report as the bridge gave it. */
export interface NewAnnotation {
  kind: 'element' | 'region'
  note: string
  rect: CaptureRect
  element?: ElementReport
  observed?: Observed
}

/** A create's capture part (POST /api/captures, the part `capture`). */
export interface CaptureCreateBody {
  opId: string
  byName?: string
  from: CaptureFrom
  via: 'web' | 'desktop'
  url: string
  title: string
  viewport: CaptureViewport
  epoch: number
  revision?: string
  label: LabelRead
  selectedAt: string
  imageHow: 'desktop' | 'tab' | 'none'
  imageScale?: number
  imageWhy?: string
  retakeOf?: string
  annotations: NewAnnotation[]
}

/** An edit's annotation: one the capture holds by its place `k`, or a new region (`k` 0). */
export interface AnnotationEdit {
  k: number
  kind: 'element' | 'region'
  note: string
  rect: CaptureRect
}

/** A source the server checked for an element's hint (dispatch.Source). */
export interface CaptureSource {
  path: string
  line?: number
  column?: number
  component?: string
  how: string
  found: boolean
  outside?: boolean
  class: 'tracked' | 'untracked' | 'ignored' | 'vendor' | 'unknown'
}

/** An element as the store keeps it (dispatch.ElementReport): its classes a list. */
export interface StoredElement {
  tag: string
  id?: string
  classes?: string[]
  role?: string
  name?: string
  text?: string
  selector: string
  box: CaptureRect
  attributes?: Array<[string, string]>
  state: ElementReport['state']
  hit: ElementReport['hit']
  html?: string
  neighbourhood?: string
  ancestors?: Array<{ tag: string; id?: string; classes?: string[]; role?: string }>
  styles?: Array<[string, string]>
  hints?: Array<{ how: string; file: string; line?: number; column?: number; component?: string }>
  cut?: string[]
}

/** An annotation of a capture as read (dispatch.Annotation). */
export interface CaptureAnnotation {
  k: number
  kind: 'element' | 'region'
  note: string
  rect: CaptureRect
  element?: StoredElement
  observed?: Observed
  sources?: CaptureSource[]
}

/** The capture document (`conductor.capture/1`), what a send freezes. */
export interface CaptureDocument {
  schema: string
  id: string
  rev: number
  url: string
  title: string
  viewport: CaptureViewport
  selectedAt: string
  savedAt: string
  origin: {
    kind: 'attempt' | 'candidate' | 'url'
    run?: string
    task?: string
    attempt?: number
    tree?: 'worktree' | 'preview'
    commit?: string
    changed: number
    changedPaths: string[]
    changedCut: boolean
    content?: string
    mutable: boolean
    candidate?: string
    where?: string
    drift?: { commit: string; content: string; changed: number; readAt: string }
    serving: { state: 'declared' | 'stale' | 'unconfirmed'; page?: string }
  }
  image: { sha256?: string; width?: number; height?: number; bytes?: number; how: 'desktop' | 'tab' | 'none'; why?: string; scale?: number }
  annotations: CaptureAnnotation[]
  retakeOf?: string
  retakeVia?: 'run' | 'ticket' | 'repo'
}

/** A capture's send (dispatch.CaptureSend), as a capture's read lists it (its request aside). */
export interface CaptureSendAnswer {
  id: string
  capture: string
  rev: number
  kind: string
  asked: unknown
  digest: string
  createdAt: string
  createdBy?: string
  actor: string
  opId: string
  state: 'pending' | 'sent' | 'refused' | 'cancelled'
  target?: string
  files?: string[]
  why?: string
  outcomes: Array<{ id: string; kind: string; target?: string; files?: string[]; why?: string; at: string; actor: string; opId: string }>
  cancels: Array<{ opId: string; at: string; actor: string }>
  /** The desk's words while a Brakeman send waits (memory only, the sends' row). */
  waiting?: string
}

/** A capture as `GET /api/captures/{id}` answers it (dispatch.Capture); a deleted one is its tombstone (id, rev, times). */
export interface CaptureAnswer extends CaptureDocument {
  createdAt: string
  createdBy?: string
  actor?: string
  via?: 'web' | 'desktop'
  epoch?: number
  scope: { crew?: string; repo?: string; project?: string }
  set: { rev: number; set: string }
  deletedAt?: string
  sends?: CaptureSendAnswer[]
}

/** A Dispatch operation's receipt (dispatch.Receipt). */
export interface CaptureReceipt {
  opId: string
  intent: string
  status: string
  rev?: number
  id?: string
  repeat?: boolean
  error?: string
  refusal?: string
}

// ---- The bridge's messages, as the workbench takes them ----

/** A message the workbench's window heard, as far as the gate reads it (a MessageEvent). */
export interface HeardMessage {
  source: unknown
  origin: string
  data: unknown
  isTrusted?: boolean
}

/** What a message must come from: the frame's own window, at the preview's origin, carrying the nonce of the hello it answers. */
export interface BridgeEnd {
  source: unknown
  origin: string
  nonce: string
}

/**
 * The one place the workbench takes a message from a previewed page (the plan's "The bridge's messages", the workbench's half): only
 * one the browser delivered, from the frame's own window (never another window, a popup, a nested frame or the workbench itself), at
 * the preview's origin exactly (a frame navigated to another origin answers no more), whose data parsePreviewMessage takes as a
 * message to the workbench: one of the bridge's types, every field within its bound, the whole within 64 KiB and an element report
 * within 32 KiB as the shared measure counts them, carrying the nonce the workbench's hello gave. Anything else is null, never an
 * exception: dropped unanswered. Nothing a message holds is ever executed or drawn as HTML.
 */
export function parseBridgeMessage(e: HeardMessage, want: BridgeEnd): PreviewToWorkbench | null {
  if (e.isTrusted === false) return null
  if (!want.source || e.source !== want.source) return null
  if (!want.origin || want.origin === 'null' || e.origin !== want.origin) return null
  if (!want.nonce) return null
  const m = parsePreviewMessage(e.data, 'workbench', want.nonce)
  return m && m.t !== 'hello' && m.t !== 'select' && m.t !== 'describe' && m.t !== 'look' && m.t !== 'cancel' ? (m as PreviewToWorkbench) : null
}

/** The origin of a URL, or '' for one that is not http or https (a frame is never given another scheme). */
export function originOf(url: string): string {
  try {
    const u = new URL(url)
    return u.protocol === 'http:' || u.protocol === 'https:' ? u.origin : ''
  } catch {
    return ''
  }
}

/** The names a host's loopback goes by: the same machine whatever the spelling. */
function loopback(host: string): boolean {
  const h = host.replace(/^\[|\]$/g, '').toLowerCase()
  return h === 'localhost' || h.endsWith('.localhost') || h === '::1' || h === '0:0:0:0:0:0:0:1' || /^127(\.\d{1,3}){3}$/.test(h) || h === '0.0.0.0'
}

function portOf(u: URL): string {
  return u.port || (u.protocol === 'https:' ? '443' : '80')
}

/**
 * Whether url is the workbench itself under any spelling: its own origin, or a loopback name (localhost, 127.0.0.1, [::1]) at the
 * workbench's port when the workbench is on the loopback too, or one of the server's other addresses it names (its TLS listener). The
 * pane never frames it (a frame of the workbench, its scripts allowed and its origin kept, would be the workbench).
 */
export function isWorkbench(url: string, workbench: string, others: readonly string[] = []): boolean {
  let u: URL
  try {
    u = new URL(url)
  } catch {
    return false
  }
  for (const w of [workbench, ...others]) {
    let o: URL
    try {
      o = new URL(w)
    } catch {
      continue
    }
    if (u.origin === o.origin) return true
    if (portOf(u) === portOf(o) && loopback(u.hostname) && loopback(o.hostname)) return true
  }
  return false
}

// ---- The label's words ----

/** A commit as the label says it: its first 7 characters. */
export function shortSha(commit: string | undefined): string {
  return (commit ?? '').slice(0, 7)
}

/**
 * What a sent capture is of, in words around its commit (the ticket's Captures): an attempt's worktree ("build attempt 1 · worktree
 * at <commit> · 2 changed"), a candidate's checkout ("candidate <commit> of build · attempt 2 · as committed"), or a link's page.
 */
export function captureOfWords(o: Pick<CaptureDocument['origin'], 'kind' | 'task' | 'attempt' | 'tree' | 'commit' | 'changed'>): { lead: string; commit: string; tail: string } {
  switch (o.kind) {
    case 'attempt':
      return { lead: `${o.task} attempt ${o.attempt} · ${o.tree} at`, commit: shortSha(o.commit), tail: `· ${o.changed} changed` }
    case 'candidate':
      return { lead: 'candidate', commit: shortSha(o.commit), tail: `of ${o.task} · attempt ${o.attempt} · as committed` }
    default:
      return { lead: 'a link', commit: '', tail: '' }
  }
}

/**
 * The label's line (the plan's "A preview in Conductor"): for a worktree "attempt 2 of build · worktree at a1b2c3d with 3 changed files
 * · changes as the agent works", for a candidate's checkout "candidate a1b2c3d of build · attempt 2 · as committed", for a link "a link
 * · no revision"; before the first read, "reading the revision…". A read that could not see the changes says so in place of the count.
 */
export function labelWords(label: Pick<PreviewLabel, 'tree' | 'commit' | 'changed' | 'unread'> | null | undefined, where: { task?: string; attempt?: number } | null): string {
  if (!where) return 'a link · no revision'
  if (!label) return 'reading the revision…'
  const task = where.task ?? ''
  const n = where.attempt ?? 0
  if (label.tree === 'preview') return `candidate ${shortSha(label.commit)} of ${task} · attempt ${n} · as committed`
  const changes = label.unread ? `its changes not read: ${label.unread}` : label.changed === 0 ? 'with no changed file' : `with ${label.changed} changed ${label.changed === 1 ? 'file' : 'files'}`
  return `attempt ${n} of ${task} · worktree at ${shortSha(label.commit)} ${changes} · changes as the agent works`
}

// ---- What changed since a send ----

type Annotated = Pick<CaptureAnnotation, 'kind' | 'note' | 'rect'> & { element?: { selector?: string; box?: CaptureRect } | ElementReport }

const sameRect = (a: CaptureRect, b: CaptureRect) => a.x === b.x && a.y === b.y && a.width === b.width && a.height === b.height

/** Whether two annotations are the same one across revisions: an element by its selector and box (never edited), a region by its rectangle. */
function same(a: Annotated, b: Annotated): boolean {
  if (a.kind !== b.kind) return false
  if (a.kind === 'element') return !!a.element && !!b.element && a.element.selector === b.element.selector && !!a.element.box && !!b.element.box && sameRect(a.element.box, b.element.box)
  return sameRect(a.rect, b.rect)
}

/**
 * What changed in a capture since a send froze it, in words ("annotation 3's note", "a region added", "annotation 2 removed",
 * "annotation 1 moved"), each annotation named by its place as sent (a new region by none): `now` is the capture as read, `sent` the
 * send's frozen document. The store renumbers the annotations at each edit, so one is followed across revisions by what never changes
 * of it (an element's selector and box, a region's rectangle, moved or not). Empty when nothing changed, and for the same revision.
 */
export function diffSinceSent(now: { rev: number; annotations: readonly Annotated[] }, sent: { rev: number; annotations: readonly Annotated[] }): string[] {
  if (now.rev === sent.rev) return []
  const out: string[] = []
  const taken = new Set<number>()
  const kept: Array<{ was: number; at: number }> = []
  sent.annotations.forEach((a, i) => {
    const at = now.annotations.findIndex((b, j) => !taken.has(j) && same(a, b))
    if (at < 0) {
      out.push(`annotation ${i + 1} removed`)
      return
    }
    taken.add(at)
    kept.push({ was: i, at })
    if (now.annotations[at]!.note !== a.note) out.push(`annotation ${i + 1}'s note`)
  })
  // Moved: its order among the kept ones changed (a removal before it alone moves nothing).
  const order = [...kept].sort((x, y) => x.at - y.at)
  order.forEach((k, i) => {
    if (kept[i] !== k) out.push(`annotation ${k.was + 1} moved`)
  })
  const added = now.annotations.filter((_, j) => !taken.has(j))
  for (const a of added) out.push(a.kind === 'region' ? 'a region added' : 'an element added')
  return out
}

// ---- What an annotation's element says, as text ----

/** An element's summary: its tag, id and first classes, then its accessible name in quotes (`button.save "Save"`). */
export function elementSummary(e: { tag: string; id?: string; classes?: string | string[]; name?: string; text?: string }): string {
  const classes = (Array.isArray(e.classes) ? e.classes : (e.classes ?? '').split(/\s+/)).filter(Boolean).slice(0, 3)
  const name = (e.name || e.text || '').trim().slice(0, 80)
  return `${e.tag}${e.id ? `#${e.id}` : ''}${classes.map((c) => `.${c}`).join('')}${name ? ` "${name}"` : ''}`
}

/** The hit test in words: what a click at the element's centre reaches, when it is not the element itself; '' when it is. */
export function hitWords(e: { hit: { self: boolean; selector?: string; tag?: string } }): string {
  if (e.hit.self) return ''
  const what = e.hit.selector || e.hit.tag
  return what ? `a click at its centre reaches ${what}` : 'a click at its centre reaches nothing of the page'
}

/** The element's disabled, hidden, inert and aria state, as words; empty when it has none. */
export function elementStateWords(s: ElementReport['state']): string[] {
  const out: string[] = []
  if (s.disabled) out.push('disabled')
  if (s.hidden) out.push('hidden')
  if (s.inert) out.push('inert')
  if (s.ariaDisabled) out.push(`aria-disabled=${s.ariaDisabled}`)
  if (s.ariaExpanded) out.push(`aria-expanded=${s.ariaExpanded}`)
  if (s.pointerEvents && s.pointerEvents !== 'auto') out.push(`pointer-events: ${s.pointerEvents}`)
  if (s.hovered) out.push('hovered')
  if (s.motion > 0) out.push(`${s.motion} ${s.motion === 1 ? 'animation' : 'animations'} playing`)
  return out
}

/** The words of an annotation whose element was seen moving (`stable: false`). */
export const MOVING_WORDS = 'this element was moving: its report is of a moment near the screenshot, not of it'
/** The words of an element that starts a new capture because the page changed since the first's screenshot. */
export const CHANGED_WORDS = 'the page changed since this screenshot: this element starts a new capture'
/** The words of a page whose epoch moved at every screenshot. */
export const RESTLESS_WORDS = 'this page does not hold still long enough to capture'
/** The words of a page that answered no hello within 3 s. */
export const NO_BRIDGE_WORDS = "this page does not load Conductor's bridge: Capture takes the screenshot and your notes only. In the desktop app every page has it"
/** The words of the pane asked to frame the workbench itself. */
export const SELF_WORDS = 'this is Conductor itself: open it in a new tab'

/**
 * A source's link in the attempt's editor: only a hint the server found in the tree (`found`), not outside it, with a relative path
 * that climbs out of nowhere (no `..`, no leading `/`, no scheme, no backslash); null for any other, shown as text alone.
 */
export function sourceLink(s: Pick<CaptureSource, 'path' | 'found' | 'outside' | 'line'>): { path: string; line?: number } | null {
  if (!s.found || s.outside) return null
  const p = s.path
  if (!p || p.startsWith('/') || p.includes('\\') || /^[a-z][a-z0-9+.-]*:/i.test(p) || p.split('/').some((x) => x === '..' || x === '.' || x === '')) return null
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f]/.test(p)) return null
  return { path: p, line: s.line && s.line > 0 ? s.line : undefined }
}

/** The preview page of an attempt, `&seq=` for its candidate's checkout (Open in Conductor). */
export function previewPath(t: { run: string; task: string; attempt: number; seq?: number }): string {
  const q = new URLSearchParams({ run: t.run, task: t.task, attempt: String(t.attempt) })
  if (t.seq) q.set('seq', String(t.seq))
  return `/preview?${q.toString()}`
}

/** How a preview names its tree's secret, which the label's admin URL puts in. */
export const TREE_SECRET_NAME = '${CONDUCTOR_TREE_SECRET}'

/**
 * Where Capture again opens the frame: the earlier capture's path and query on this preview's own origin, with this preview's own
 * fragment (where its tree's secret is put in); never another origin, whatever the capture's address names. '' when either does
 * not parse or the preview is not http or https.
 */
export function retakeAddress(captureUrl: string, previewUrl: string): string {
  try {
    const p = new URL(previewUrl)
    const c = new URL(captureUrl)
    const web = (u: URL) => u.protocol === 'http:' || u.protocol === 'https:'
    if (!web(p) || !web(c)) return ''
    const at = new URL(`${c.pathname}${c.search}`, p.origin)
    at.hash = p.hash
    return at.origin === p.origin ? at.href : ''
  } catch {
    return ''
  }
}

/** A capture's id (dispatch.ValidID): 16 lower-case hex digits. */
export const CAPTURE_ID = /^[0-9a-f]{16}$/
