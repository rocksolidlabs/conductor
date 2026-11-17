import type { PublicationSummary, PublishOffer } from '~/composables/useDispatch'
import { githubHref } from './tracker'

// A ticket's publications to GitHub (Program item IN1a, docs/plans/tracker-linking.md, PR-9; docs/protocol.md, "A ticket's
// publications"): what the composer may insert, the body it sends, the check that the preview drawn is the very bytes Send names, and
// each publication's state, close and actions in words. Pure: DispatchPublish.vue and DispatchPublications.vue draw it, vitest proves it
// (publication.test.ts). Publishing is a person's alone: nothing here posts, and nothing but the offer's parts is ever inserted.

/** The parts a composer may offer, in the order they are inserted (dispatch.PublicationParts). */
export const PUBLICATION_PARTS = ['deliveries', 'checks', 'key'] as const
export type PublicationPart = (typeof PUBLICATION_PARTS)[number]

/** A publication's bound of posts (dispatch.MaxPublicationTries). */
export const MAX_PUBLICATION_TRIES = 5

/** The body's bound, in bytes of UTF-8 (dispatch.MaxPublicationBody). */
export const MAX_PUBLICATION_BODY = 16 << 10

/** Each part's name in the composer: what ticking it inserts. */
export const PART_LABELS: Record<PublicationPart, string> = {
  deliveries: 'The delivery links',
  checks: "The delivered candidate's checks",
  key: "The ticket's key and title",
}

/** An offered part: its name and the exact Markdown ticking it inserts. */
export interface OfferedPart {
  name: PublicationPart
  label: string
  markdown: string
}

/**
 * The parts of an offer the composer draws: only the names a composer may offer, each once (the first of a name), in the fixed order;
 * any other entry of the offer, or a part with no Markdown, is not offered. All start unticked (the person's choice: owner decision 8).
 */
export function offeredParts(offer: Pick<PublishOffer, 'parts'> | null | undefined): OfferedPart[] {
  const out: OfferedPart[] = []
  for (const name of PUBLICATION_PARTS) {
    const p = offer?.parts?.find((x) => x.name === name)
    if (p && typeof p.markdown === 'string' && p.markdown.trim()) out.push({ name, label: PART_LABELS[name], markdown: p.markdown })
  }
  return out
}

/**
 * The body a publication's prepare sends, and the parts it names: the person's own text, then each ticked part's exact Markdown in the
 * offer's order, each after a blank line. A name ticked that the offer does not hold inserts nothing and is not named: nothing but the
 * offered parts is ever inserted.
 */
export function composeBody(text: string, offered: readonly OfferedPart[], ticked: readonly string[]): { body: string; parts: PublicationPart[] } {
  const chosen = offered.filter((p) => ticked.includes(p.name))
  const pieces = [text.replace(/\s+$/, ''), ...chosen.map((p) => p.markdown.replace(/\s+$/, ''))].filter((s) => s.trim() !== '')
  return { body: pieces.join('\n\n'), parts: chosen.map((p) => p.name) }
}

/** The body as the server keeps it: `\r\n` and a lone `\r` made `\n`, trailing white space trimmed. */
export function normalBody(s: string): string {
  return s.replace(/\r\n/g, '\n').replace(/\r/g, '\n').replace(/[ \t\n]+$/, '')
}

const utf8 = new TextEncoder()

/** Why a body cannot be prepared, in words ('' when it can): empty, past 16 KiB of UTF-8, or with a control character but a line break or a tab. */
export function bodyProblem(body: string): string {
  const s = normalBody(body)
  if (!s) return 'Write the outcome, or tick a part'
  const bytes = utf8.encode(s).length
  if (bytes > MAX_PUBLICATION_BODY) return `At most ${MAX_PUBLICATION_BODY / 1024} KiB: this is ${Math.ceil(bytes / 1024)} KiB`
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u0008\u000b-\u001f\u007f-\u009f]/.test(s)) return 'A control character other than a line break or a tab cannot be published'
  return ''
}

/** Whether a target is an issue, the only kind whose close is offered: the linked item by its snapshot's kind, a link by its address. */
export function targetIsIssue(target: string, url: string, linkedKind?: string): boolean {
  if (target === 'linked' && linkedKind) return linkedKind === 'issue'
  try {
    return /\/issues\/\d+\/?$/.test(new URL(url).pathname)
  } catch {
    return false
  }
}

/** A target's words in the composer's pick: the linked issue, or the link's item by its address's `owner/repo#n`. */
export function targetLabel(target: string, url: string): string {
  const ref = itemRef(url)
  if (target === 'linked') return ref ? `The linked item, ${ref}` : 'The linked item'
  return ref ? `The link to ${ref}` : `The link to ${url}`
}

/**
 * The target a composer opens on: the one asked for (a link's row) when the offer holds it, else none, and why in words, never
 * another item; the offer's first when none was asked for.
 */
export function pickTarget(asked: string | undefined, targets: ReadonlyArray<{ target: string }>): { target: string; refused: string } {
  if (asked) {
    return targets.some((t) => t.target === asked)
      ? { target: asked, refused: '' }
      : { target: '', refused: 'That link cannot be published to: its address is not a GitHub item Conductor reads with a token kept for its host.' }
  }
  return { target: targets[0]?.target ?? '', refused: '' }
}

/** `owner/repo#n` of a GitHub item's address, '' for one that is not. */
export function itemRef(url: string): string {
  try {
    const m = /^\/([^/]+)\/([^/]+)\/(?:issues|pull)\/(\d+)\/?$/.exec(new URL(url).pathname)
    return m ? `${m[1]}/${m[2]}#${m[3]}` : ''
  } catch {
    return ''
  }
}

// ---------------------------------------------------------------------------
// The preview: the exact bytes Send posts, checked against the digest Send names.

/**
 * The preview's parts: the body and GitHub's marker after a blank line, as the server posts them; null when the preview does not end
 * with the publication's marker so, which the composer never offers to send.
 */
export function previewParts(preview: string, marker: string): { body: string; marker: string } | null {
  const tail = '\n\n' + marker
  if (!marker || !preview.endsWith(tail) || preview.length === tail.length) return null
  return { body: preview.slice(0, -tail.length), marker }
}

/** Whether a preview is the bytes `postSha256` names, ending with its marker: Send is offered for no other. */
export function previewHolds(preview: string, postSha256: string, marker: string): boolean {
  return !!previewParts(preview, marker) && /^[0-9a-f]{64}$/.test(postSha256) && sha256Hex(preview) === postSha256
}

/** The marker said in words: what it is, that GitHub does not show it, that it says nothing of the ticket. */
export function markerWords(marker: string): string {
  return `Last, hidden on GitHub, the marker ${marker}: the publication's random id, which says nothing of the ticket, so Conductor finds this comment and never posts it twice.`
}

const K = new Uint32Array([
  0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5, 0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe,
  0x9bdc06a7, 0xc19bf174, 0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da, 0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7,
  0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967, 0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85, 0xa2bfe8a1, 0xa81a664b,
  0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070, 0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
  0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
])

/**
 * The SHA-256 of a text's UTF-8 bytes, in hex. Its own, not the browser's: `crypto.subtle` is absent from a page not served over TLS or
 * from loopback, and the preview is checked on every page.
 */
export function sha256Hex(text: string): string {
  const bytes = utf8.encode(text)
  const n = bytes.length
  const size = (((n + 9 + 63) / 64) | 0) * 64
  const buf = new Uint8Array(size)
  buf.set(bytes)
  buf[n] = 0x80
  const dv = new DataView(buf.buffer)
  dv.setUint32(size - 8, Math.floor((n * 8) / 0x100000000))
  dv.setUint32(size - 4, (n * 8) >>> 0)
  const h = new Uint32Array([0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19])
  const w = new Uint32Array(64)
  const rotr = (x: number, r: number) => (x >>> r) | (x << (32 - r))
  for (let off = 0; off < size; off += 64) {
    for (let i = 0; i < 16; i++) w[i] = dv.getUint32(off + i * 4)
    for (let i = 16; i < 64; i++) {
      const a = w[i - 15]!
      const b = w[i - 2]!
      const s0 = rotr(a, 7) ^ rotr(a, 18) ^ (a >>> 3)
      const s1 = rotr(b, 17) ^ rotr(b, 19) ^ (b >>> 10)
      w[i] = (w[i - 16]! + s0 + w[i - 7]! + s1) >>> 0
    }
    let [a, b, c, d, e, f, g, hh] = [h[0]!, h[1]!, h[2]!, h[3]!, h[4]!, h[5]!, h[6]!, h[7]!]
    for (let i = 0; i < 64; i++) {
      const t1 = (hh + (rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25)) + ((e & f) ^ (~e & g)) + K[i]! + w[i]!) >>> 0
      const t2 = ((rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22)) + ((a & b) ^ (a & c) ^ (b & c))) >>> 0
      hh = g
      g = f
      f = e
      e = (d + t1) >>> 0
      d = c
      c = b
      b = a
      a = (t1 + t2) >>> 0
    }
    h[0] = (h[0]! + a) >>> 0
    h[1] = (h[1]! + b) >>> 0
    h[2] = (h[2]! + c) >>> 0
    h[3] = (h[3]! + d) >>> 0
    h[4] = (h[4]! + e) >>> 0
    h[5] = (h[5]! + f) >>> 0
    h[6] = (h[6]! + g) >>> 0
    h[7] = (h[7]! + hh) >>> 0
  }
  return Array.from(h, (x) => x.toString(16).padStart(8, '0')).join('')
}

// ---------------------------------------------------------------------------
// A publication's state, its close and its actions.

/** A tone of the words: the error tone for a send that failed or a close refused, neutral ink otherwise; never amber (lead's decision 4). */
export type PublicationTone = 'error' | 'muted' | 'default'

/** A time in a publication's words: "14:02" today, "16 Nov 14:02" another day. */
export function atWords(iso: string | undefined, now: number): string {
  if (!iso) return ''
  const t = new Date(iso)
  if (Number.isNaN(t.getTime())) return ''
  const hm = t.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  return new Date(now).toDateString() === t.toDateString() ? hm : `${t.toLocaleDateString([], { day: 'numeric', month: 'short' })} ${hm}`
}

/** The account a publication was posted as: GitHub's login, else the person who sent it. */
function sender(p: Pick<PublicationSummary, 'sentAsLogin' | 'sentBy'>): string {
  return p.sentAsLogin ? `@${p.sentAsLogin}` : p.sentBy || 'an account not recorded'
}

/** The words that GitHub has not shown the comment: it is never sent again. */
export const UNCONFIRMED_WORDS = 'not confirmed: GitHub has not shown this comment; it may still appear'

/**
 * A publication's state in words, with its tone: "Published to acme/api#612 by @alice at 14:02"; a draft; being sent; not sent, with the
 * server's words; not confirmed; discarded; and a restored one said as of another data directory.
 */
export function publicationWords(p: PublicationSummary, now: number): { text: string; tone: PublicationTone } {
  const restored = p.restored ? ' (restored from an export: never sent from here)' : ''
  switch (p.state) {
    case 'sent':
      return { text: `Published to ${p.targetRef} by ${sender(p)}${p.sentAt ? ` at ${atWords(p.sentAt, now)}` : ''}${p.restored ? ' (restored from an export)' : ''}`, tone: 'default' }
    case 'sending':
      return { text: `Being sent to ${p.targetRef}: Check looks for its comment on GitHub`, tone: 'muted' }
    case 'failed':
      return { text: `Not sent to ${p.targetRef}${p.error ? `: ${p.error}` : ''}${restored}`, tone: 'error' }
    case 'unconfirmed':
      return { text: `To ${p.targetRef}, ${UNCONFIRMED_WORDS}${restored}`, tone: 'muted' }
    case 'discarded':
      return { text: `Discarded, to ${p.targetRef}`, tone: 'muted' }
    default:
      return { text: `A draft to ${p.targetRef}, not sent${restored}`, tone: 'muted' }
  }
}

/** How the issue is closed, in words, by the reason the publication holds: "as completed", "as not planned". */
export function closeAs(reason: string | undefined): string {
  return reason === 'not_planned' ? 'as not planned' : 'as completed'
}

/** The issue's close, asked with the publication, in words ('' for none asked). */
export function closeWords(p: Pick<PublicationSummary, 'closeIssue' | 'closeState' | 'closeError' | 'state'>): { text: string; tone: PublicationTone } {
  if (!p.closeIssue) return { text: '', tone: 'muted' }
  const as = closeAs(p.closeIssue)
  switch (p.closeState) {
    case 'closed':
      return { text: `The issue is closed${p.closeError ? `: ${p.closeError}` : ` ${as}`}`, tone: 'default' }
    case 'closing':
      return { text: `The issue's close ${as} was sent and GitHub has not answered: Close now looks at it again`, tone: 'muted' }
    case 'refused':
      return { text: `The issue's close was refused${p.closeError ? `: ${p.closeError}` : ''}; the comment stays`, tone: 'error' }
    case 'pending':
      return { text: p.state === 'sent' ? `The issue's close ${as} is not made yet` : `Closes the issue ${as} once the comment is posted`, tone: 'muted' }
    default:
      return { text: `Closes the issue ${as} once the comment is posted`, tone: 'muted' }
  }
}

export type PublicationAction = 'check' | 'send-again' | 'close-now' | 'new' | 'discard'

/**
 * What a person may do with a publication, as the server allows it: Check, for one `sending` or `unconfirmed`, or a restored draft or
 * failed one; Send again, for a `failed` one of this store only (never one not confirmed, never a restored one); Close now, for a sent one
 * of this store whose close did not finish; Publish as new, for one not confirmed or restored and not sent (the first may still appear);
 * Discard, for a draft, a failed or a not confirmed one.
 */
export function publicationActions(p: Pick<PublicationSummary, 'state' | 'restored' | 'closeIssue' | 'closeState'> & Partial<Pick<PublicationSummary, 'tries'>>): PublicationAction[] {
  const out: PublicationAction[] = []
  const s = p.state
  /** A failed one at its bound of tries is never sent again (the server refuses it): its text may be published as new. */
  const spent = s === 'failed' && (p.tries ?? 0) >= MAX_PUBLICATION_TRIES
  if (s === 'sending' || s === 'unconfirmed' || (p.restored && (s === 'draft' || s === 'failed'))) out.push('check')
  if (s === 'failed' && !p.restored && !spent) out.push('send-again')
  if (s === 'sent' && !p.restored && p.closeIssue && (p.closeState === 'pending' || p.closeState === 'closing' || p.closeState === 'refused')) out.push('close-now')
  if (s !== 'sent' && s !== 'discarded' && s !== 'sending' && (s === 'unconfirmed' || p.restored || spent)) out.push('new')
  if (s === 'draft' || s === 'failed' || s === 'unconfirmed') out.push('discard')
  return out
}

/** Whether a person can act on it, the server's rule for `needYou` (one sending, not confirmed or failed, a restored draft, a sent one whose close did not finish). */
export function needsPerson(p: Pick<PublicationSummary, 'state' | 'restored' | 'closeIssue' | 'closeState'>): boolean {
  if (p.state === 'sending' || p.state === 'unconfirmed' || p.state === 'failed') return true
  if (p.state === 'draft') return !!p.restored
  return p.state === 'sent' && !p.restored && !!p.closeIssue && (p.closeState === 'pending' || p.closeState === 'closing' || p.closeState === 'refused')
}

/** "1 older publication needs you", "3 older publications need you"; '' for none. */
export function needYouWords(n: number): string {
  if (!n) return ''
  return n === 1 ? '1 older publication needs you' : `${n} older publications need you`
}

/** The strip's words for the publications that need a person among those it holds and the older ones: '' for none. Neutral, never amber. */
export function publicationsStripWords(shown: readonly PublicationSummary[], older: number): string {
  const n = shown.filter(needsPerson).length + older
  if (!n) return ''
  return n === 1 ? '1 publication needs you' : `${n} publications need you`
}

/**
 * The comment's address, drawn as a link only by the one rule of what of GitHub's text is a link (githubHref: https on the target's own
 * host, as written), and only when it is the target's own page with nothing but a `#issuecomment-<n>` anchor beyond it; '' otherwise
 * (the state's words still say it was published).
 */
export function commentLink(p: Pick<PublicationSummary, 'remoteUrl' | 'targetUrl'>): string {
  if (!p.remoteUrl || !p.targetUrl) return ''
  try {
    const t = new URL(p.targetUrl)
    if (!githubHref(p.remoteUrl, t.host)) return ''
    const u = new URL(p.remoteUrl)
    if (u.username || u.password || u.search || u.pathname.replace(/\/$/, '') !== t.pathname.replace(/\/$/, '')) return ''
    if (u.hash && !/^#issuecomment-\d+$/.test(u.hash)) return ''
    return p.remoteUrl
  } catch {
    return ''
  }
}

/** The warning Publish as new carries: the first may still appear. */
export const PUBLISH_AS_NEW_WARNING = 'The first publication may still appear on GitHub: publishing this again can leave two comments.'

// ---------------------------------------------------------------------------
// A run's source: the linked item its launch's package names.

/**
 * The run header's words for the item a launch's package names as its source: "GitHub acme/api#612" for a package whose source is a
 * GitHub item; '' for none, for a source the launch held unlinked, or for one that is not a reference.
 */
export function packageSourceWords(pkg: unknown): string {
  const src = (pkg as { source?: { unlinked?: boolean; system?: unknown; ref?: unknown } } | null)?.source
  if (!src || src.unlinked || src.system !== 'github' || typeof src.ref !== 'string') return ''
  if (!/^[\w.-]{1,100}\/[\w.-]{1,100}#\d{1,10}$/.test(src.ref)) return ''
  return `GitHub ${src.ref}`
}
