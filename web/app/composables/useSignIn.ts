import { onBeforeUnmount, onMounted, ref, watch, type Ref, type WatchSource } from 'vue'
import { realAfter, withDeadline, type After } from '../utils/capacity'
import { MAX_SIGNIN_CODE, MAX_SIGNIN_URL, MAX_SIGNIN_WHO, MAX_SIGNIN_WHY, SIGNIN_UNAVAILABLE_CODE, type SignInState } from '../utils/protocol'

/**
 * The identity provider's sign-in, read, begun and ended from Settings (GET, POST and DELETE /api/signin, docs/protocol.md). The
 * route is read only while the card shows, the document is visible, the workbench token is held and GET /api/capabilities has a
 * ready identity provider, so a plain build makes no request of it. While a sign-in is `starting` or shows a `code` it is read
 * again SIGNIN_FIRST_MS after the last reply, doubling to SIGNIN_EVERY_MS; in every other state every SIGNIN_EVERY_MS, so a
 * revocation elsewhere, a lapse, a renewal or a sign-out settling shows without a reload. What is read lives in the card's memory
 * alone: never in the page's storage, and forgotten with the token or when the card goes.
 */
export const SIGNIN_FIRST_MS = 2000
export const SIGNIN_EVERY_MS = 30_000
/** A request's deadline: the server answers within its own waits (1 s for the act, 1 s for the status), so this only bounds a hang. */
export const SIGNIN_DEADLINE_MS = 15_000

/** A sign-in as the card shows it: each field '' when its state does not carry it; `ended` a code past its `expires`, not shown. */
export interface SignInView {
  state: SignInState
  who: string
  code: string
  url: string
  expires: string
  why: string
  ended: boolean
}

const STATES: readonly SignInState[] = ['signed-out', 'starting', 'code', 'signed-in']
const bytes = (s: string) => new TextEncoder().encode(s).length
const appOrigin = () => (typeof location === 'undefined' ? '' : location.origin)

/** What the card refuses in `who` and `why`, which the server sends without format characters and separators: controls (C0, DEL, C1), format characters, line and paragraph separators; and a lone surrogate. */
const NOT_ONE_LINE = /[\p{Cc}\p{Cf}\p{Zl}\p{Zp}\p{Cs}]/u

/** A one-line text of at most `max` bytes, or ''. */
function line(v: unknown, max: number): string {
  return typeof v === 'string' && !NOT_ONE_LINE.test(v) && bytes(v) <= max ? v : ''
}

/**
 * The page the card links to, `u` itself, or '' for no link. The server's rule for `url` (ext's signInURL, docs/protocol.md): 1 to
 * MAX_SIGNIN_URL bytes of printable ASCII with no space, quote (" ' `), backslash, angle bracket or '#', every '%' before two hex
 * digits; `https://` in lower case, a host that is a name (labels of a-z, 0-9 and '-', none empty, the last beginning with a letter),
 * after a ':' a port of 1 to 65535 without leading zeros that is not 443 (a ':' with no port is refused), then a path. Beside it, the
 * browser writes the address as given
 * (`new URL(u).href === u`) and its origin is not the app's own.
 */
export function signInPage(u: unknown, origin = appOrigin()): string {
  if (typeof u !== 'string' || bytes(u) > MAX_SIGNIN_URL || !/^[\x21-\x7e]+$/.test(u) || /["'`\\<>#]|%(?![0-9A-Fa-f]{2})/.test(u)) return ''
  const m = /^https:\/\/(?:[a-z0-9-]+\.)*[a-z][a-z0-9-]*(?::([1-9][0-9]{0,4}))?\//.exec(u)
  if (!m || Number(m[1] ?? 1) > 65535 || m[1] === '443') return ''
  try {
    const p = new URL(u)
    return p.href === u && p.origin !== origin ? u : ''
  } catch {
    return ''
  }
}

/**
 * A reply as the card keeps it, checked again here: null for `{supported: false}`, a state the app does not know, or a code without
 * its code. A code's page is linked only as signInPage allows. Whether a code has ended is the poll's to judge (SignInPoll.judge).
 */
export function asSignIn(body: unknown, origin = appOrigin()): SignInView | null {
  const b = body as Record<string, unknown> | null
  if (!b || typeof b !== 'object' || b.supported !== true || !STATES.includes(b.state as SignInState)) return null
  const state = b.state as SignInState
  const v: SignInView = { state, who: '', code: '', url: '', expires: '', why: line(b.why, MAX_SIGNIN_WHY), ended: false }
  if (state === 'signed-in') v.who = line(b.who, MAX_SIGNIN_WHO)
  if (state === 'code') {
    const code = typeof b.code === 'string' && b.code.length <= MAX_SIGNIN_CODE && /^[A-Za-z0-9-]+$/.test(b.code) ? b.code : ''
    if (!code) return null
    if (typeof b.expires === 'string' && !Number.isNaN(Date.parse(b.expires))) v.expires = b.expires
    Object.assign(v, { code, url: signInPage(b.url, origin) })
  }
  return v
}

/** The code as the card shows it: two groups of four for eight letters and digits (a dash between them or none), else one group. */
export function codeGroups(code: string): string[] {
  const m = /^([A-Za-z0-9]{4})-?([A-Za-z0-9]{4})$/.exec(code)
  return m ? [m[1]!, m[2]!] : [code]
}

/** Whether a state is one a person waits on: read again sooner. */
const waiting = (s: SignInState | undefined) => s === 'starting' || s === 'code'

export type SignInMethod = 'GET' | 'POST' | 'DELETE'

/** A failed call as the card reads it: its HTTP status (0 for none) and the server's code. */
const statusOf = (e: unknown) => (e as { status?: number } | null)?.status ?? 0
const codeOf = (e: unknown) => (e as { code?: string } | null)?.code ?? ''

/** What the card says of a begin or a sign-out that was not taken (actNotTaken), 401 and 404 aside. */
export const SIGNIN_UNAVAILABLE = 'That was not done. Try again in a moment.'
/** What the card says of a begin or a sign-out whose outcome is not known (anything actNotTaken does not take): no act is offered until a read shows a state. */
export const SIGNIN_READING = 'Reading the sign-in…'

/**
 * Whether a failed begin or sign-out says it was not taken: the server's 503 signin_unavailable, or a refusal made before the
 * provider is asked (400, 401, 403, 404, 429). The one place an act's failure is sorted: anything else (the card's deadline, a
 * request that failed with no answer, another 5xx, a reply the card cannot read, 503 signin_status_unknown) leaves it not known.
 */
export function actNotTaken(e: unknown): boolean {
  const s = statusOf(e)
  return [400, 401, 403, 404, 429].includes(s) || (s === 503 && codeOf(e) === SIGNIN_UNAVAILABLE_CODE)
}
const DEADLINE = 'signin-deadline'
/** The longest wait a timer takes (2^31 - 1 ms): a code's end further off is looked at again then. */
const MAX_TIMER = 2 ** 31 - 1

/** What a card shows: the sign-in (null while there is none), an act's error line, a reading note, and whether an act is on its way. */
export interface SignInRefs {
  view: Ref<SignInView | null>
  error: Ref<string>
  note: Ref<string>
  busy: Ref<boolean>
}

/**
 * The sign-in's requests for one card. `show`, `visible`, `identity` and `token` say what the page knows; the route is read while all
 * hold, and nothing is read or scheduled otherwise. One request is on its way at a time, each aborted at SIGNIN_DEADLINE_MS: a read
 * asked for meanwhile is owed and made once that request settles (dropped if the card is not active then), and an act aborts a read
 * on its way, so its reply is always its own. No act is made while an act's outcome is not known, so a read on its way to settle one
 * is never aborted by an act. A code's end is judged by the wall clock, `now`.
 */
export class SignInPoll {
  readonly view: Ref<SignInView | null>
  readonly error: Ref<string>
  readonly note: Ref<string>
  readonly busy: Ref<boolean>
  private timer: (() => void) | undefined
  private ending: (() => void) | undefined
  private wait = SIGNIN_FIRST_MS
  private shown = false
  private seen = true
  private has = false
  private forToken = ''
  private sent: AbortController | null = null
  private owed = false
  /** The last reply's view, before its code's end is judged. */
  private last: SignInView | null = null

  constructor(
    private readonly call: (method: SignInMethod, signal: AbortSignal) => Promise<unknown>,
    refs: SignInRefs,
    private readonly after: After = realAfter,
    private readonly now: () => number = () => Date.now(),
  ) {
    this.view = refs.view
    this.error = refs.error
    this.note = refs.note
    this.busy = refs.busy
  }

  private get active() {
    return this.shown && this.seen && this.has && !!this.forToken
  }

  /** The card shows, or is gone (what it read is forgotten). */
  show(on: boolean) {
    this.shown = on
    if (on) this.read()
    else this.forget()
  }

  /** The document is visible, or hidden (no read until it is visible again, when it judges the code's end and reads at once). */
  visible(on: boolean) {
    if (on === this.seen) return
    this.seen = on
    if (on) {
      this.judge()
      this.read()
    } else this.stop()
  }

  /** Whether GET /api/capabilities has a ready identity provider: without one, nothing is read and nothing is shown. */
  identity(on: boolean) {
    if (on === this.has) return
    this.has = on
    if (on) this.read()
    else this.forget()
  }

  /** The workbench token: another one, or none, forgets what was read and aborts the request on its way. */
  token(t: string) {
    if (t === this.forToken) return
    this.forToken = t
    this.forget()
    this.read()
  }

  /**
   * Begins a sign-in (POST) or signs out (DELETE); what the provider answers is shown, its refusal in the card's words. None is made
   * while the last act's outcome is not known (`note`), until a read shows a state.
   */
  async act(method: 'POST' | 'DELETE'): Promise<void> {
    if (!this.active || this.busy.value || this.note.value) return
    this.busy.value = true
    this.wait = SIGNIN_FIRST_MS
    await this.send(method)
  }

  /** Reads the route now when it may be read, or once the request on its way has settled. */
  read() {
    if (!this.active) return
    if (this.sent) this.owed = true
    else void this.send('GET')
  }

  private async send(method: SignInMethod) {
    this.stop()
    this.sent?.abort()
    this.owed = false
    const ctl = new AbortController()
    this.sent = ctl
    let body: unknown
    let err: unknown = null
    try {
      body = await withDeadline(
        (deadline) => (deadline.addEventListener('abort', () => ctl.abort()), this.call(method, ctl.signal)),
        SIGNIN_DEADLINE_MS,
        DEADLINE,
        this.after,
      )
    } catch (e) {
      err = e
    }
    // Aborted by an act, another token, the capability gone or the card gone: nothing of it is shown.
    if (this.sent !== ctl) return
    this.sent = null
    if (method !== 'GET') this.busy.value = false
    const v = err ? null : asSignIn(body)
    if (!err && (v || method === 'GET')) {
      this.last = v
      this.error.value = ''
      this.note.value = ''
    } else if (statusOf(err) === 404) this.last = null
    else if (method === 'GET') {
      // A read that failed or met its deadline keeps what is shown and follows the schedule.
    } else if (!actNotTaken(err)) {
      // An act's reply the card cannot read is not known too.
      this.note.value = SIGNIN_READING
      this.error.value = ''
    } else if (statusOf(err) !== 401) this.error.value = SIGNIN_UNAVAILABLE
    this.judge()
    // The read owed is spent by the one read it starts, or dropped when the card is not active.
    const owed = this.owed
    this.owed = false
    if (owed && this.active) void this.send('GET')
    else this.next()
  }

  /**
   * Shows the last reply, its code judged: it has ended once the wall clock reaches its `expires`. Called on every reply outcome,
   * when the page is shown, and by its own timer, armed from what is left; nothing is kept from one look to the next.
   */
  private judge() {
    this.ending?.()
    this.ending = undefined
    const v = this.last
    const left = v?.state === 'code' && v.expires ? Date.parse(v.expires) - this.now() : Infinity
    if (left <= 0) {
      this.view.value = { ...v!, code: '', url: '', ended: true }
      return
    }
    this.view.value = v
    if (left !== Infinity) {
      this.ending = this.after(Math.min(left, MAX_TIMER), () => {
        this.ending = undefined
        this.judge()
      })
    }
  }

  /** After a reply: the next read, sooner while a person waits on the sign-in or its status is being read. */
  private next() {
    this.stop()
    if (!this.active) return
    let after = SIGNIN_EVERY_MS
    if (waiting(this.view.value?.state) || this.note.value) {
      after = this.wait
      this.wait = Math.min(2 * this.wait, SIGNIN_EVERY_MS)
    } else this.wait = SIGNIN_FIRST_MS
    this.timer = this.after(after, () => {
      this.timer = undefined
      this.read()
    })
  }

  private stop() {
    this.timer?.()
    this.timer = undefined
  }

  /** Forgets what was read and aborts the request on its way. */
  private forget() {
    this.sent?.abort()
    this.sent = null
    this.owed = false
    this.stop()
    this.last = null
    this.judge()
    this.wait = SIGNIN_FIRST_MS
    this.error.value = ''
    this.note.value = ''
    this.busy.value = false
  }
}

/** The document as the wiring reads it: its visibility and its `visibilitychange` event. */
export interface SignInDocument {
  readonly visibilityState: string
  addEventListener(type: 'visibilitychange', fn: () => void): void
  removeEventListener(type: 'visibilitychange', fn: () => void): void
}

/** Ties a card's poll to the document's visibility, the workbench token and the identity capability, and shows it; answers what unties it all. */
export function wireSignIn(poll: SignInPoll, doc: SignInDocument, token: WatchSource<string>, identity: WatchSource<boolean>): () => void {
  const onVisibility = () => poll.visible(doc.visibilityState === 'visible')
  onVisibility()
  const stops = [watch(token, (t) => poll.token(t), { immediate: true }), watch(identity, (on) => poll.identity(on), { immediate: true })]
  doc.addEventListener('visibilitychange', onVisibility)
  poll.show(true)
  return () => {
    doc.removeEventListener('visibilitychange', onVisibility)
    for (const s of stops) s()
    poll.show(false)
  }
}

/**
 * The Settings card's sign-in: `view` (null while there is none to show), `error` (the card's words for a begin or a sign-out that
 * was not taken), `note` (its status being read), `busy`, `begin` and `signOut`, tied to the card's lifetime (wireSignIn).
 */
export function useSignIn() {
  const refs: SignInRefs = { view: ref<SignInView | null>(null), error: ref(''), note: ref(''), busy: ref(false) }
  const { request } = useApi()
  const admin = useWorkbenchToken()
  const capabilities = useCapabilities()
  const poll = new SignInPoll((method, signal) => request<unknown>('/api/signin', { method, signal }), refs)
  let unwire = () => {}
  onMounted(() => (unwire = wireSignIn(poll, document, admin.token, () => capabilities.has('identity'))))
  onBeforeUnmount(() => unwire())
  return { ...refs, begin: () => poll.act('POST'), signOut: () => poll.act('DELETE') }
}
