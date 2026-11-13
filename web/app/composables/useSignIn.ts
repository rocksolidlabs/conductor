import { onBeforeUnmount, onMounted, ref, watch, type Ref } from 'vue'
import { MAX_SIGNIN_CODE, MAX_SIGNIN_URL, MAX_SIGNIN_WHO, MAX_SIGNIN_WHY, type SignInState } from '../utils/protocol'

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

/** A sign-in as the card shows it: each field '' when its state does not carry it. */
export interface SignInView {
  state: SignInState
  who: string
  code: string
  url: string
  expires: string
  why: string
}

const STATES: readonly SignInState[] = ['signed-out', 'starting', 'code', 'signed-in']
const bytes = (s: string) => new TextEncoder().encode(s).length

/** A one-line text of at most `max` bytes, or ''. */
function line(v: unknown, max: number): string {
  return typeof v === 'string' && !/[\u0000-\u001f\u007f]/.test(v) && bytes(v) <= max ? v : ''
}

/** Whether `u` is a page the card may link to: an https URL with a host, within its bound. */
export function signInPage(u: unknown): u is string {
  if (typeof u !== 'string' || !u || u.length > MAX_SIGNIN_URL) return false
  try {
    const p = new URL(u)
    return p.protocol === 'https:' && !!p.host && !p.username && !p.password
  } catch {
    return false
  }
}

/**
 * A reply as the card keeps it, checked again here: null for `{supported: false}`, a state the app does not know, or a code
 * without its code or its https page (nothing then is shown, never a link the server did not check).
 */
export function asSignIn(body: unknown): SignInView | null {
  const b = body as Record<string, unknown> | null
  if (!b || typeof b !== 'object' || b.supported !== true || !STATES.includes(b.state as SignInState)) return null
  const state = b.state as SignInState
  const v: SignInView = { state, who: '', code: '', url: '', expires: '', why: line(b.why, MAX_SIGNIN_WHY) }
  if (state === 'signed-in') v.who = line(b.who, MAX_SIGNIN_WHO)
  if (state === 'code') {
    const code = typeof b.code === 'string' && b.code.length <= MAX_SIGNIN_CODE && /^[A-Za-z0-9-]+$/.test(b.code) ? b.code : ''
    if (!code || !signInPage(b.url)) return null
    v.code = code
    v.url = b.url
    v.expires = typeof b.expires === 'string' && !Number.isNaN(Date.parse(b.expires)) ? b.expires : ''
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

/** A failed call as the card reads it: its HTTP status (0 for none). */
const statusOf = (e: unknown) => (e as { status?: number } | null)?.status ?? 0

/** What the card says when the provider did not answer a begin or a sign-out. */
export const SIGNIN_UNAVAILABLE = 'The identity provider did not answer. Try again in a moment.'

/**
 * The sign-in's reads for one card. `show`, `visible`, `identity` and `token` say what the page knows; the route is read while all
 * hold, and nothing is read or scheduled otherwise. A reply is applied only when it answers the last call made, so an older read
 * never overwrites what a begin or a sign-out answered.
 */
export class SignInPoll {
  private seq = 0
  private timer: ReturnType<typeof setTimeout> | undefined
  private wait = SIGNIN_FIRST_MS
  private shown = false
  private seen = true
  private has = false
  private forToken = ''

  constructor(
    private readonly call: (method: SignInMethod) => Promise<unknown>,
    readonly view: Ref<SignInView | null>,
    readonly error: Ref<string>,
    readonly busy: Ref<boolean>,
  ) {}

  private get active() {
    return this.shown && this.seen && this.has && !!this.forToken
  }

  /** The card shows, or is gone (what it read is forgotten). */
  show(on: boolean) {
    this.shown = on
    if (on) void this.read()
    else this.forget()
  }

  /** The document is visible, or hidden (no read until it is visible again, when it reads at once). */
  visible(on: boolean) {
    if (on === this.seen) return
    this.seen = on
    if (on) void this.read()
    else this.stop()
  }

  /** Whether GET /api/capabilities has a ready identity provider: without one, nothing is read and nothing is shown. */
  identity(on: boolean) {
    if (on === this.has) return
    this.has = on
    if (on) void this.read()
    else this.forget()
  }

  /** The workbench token: another one, or none, forgets what was read and drops a reply on its way. */
  token(t: string) {
    if (t === this.forToken) return
    this.forToken = t
    this.forget()
    void this.read()
  }

  /** Begins a sign-in (POST) or signs out (DELETE); what the provider answers is shown, its refusal in the card's words. */
  async act(method: 'POST' | 'DELETE'): Promise<void> {
    if (!this.active || this.busy.value) return
    this.busy.value = true
    this.error.value = ''
    this.wait = SIGNIN_FIRST_MS
    try {
      await this.send(method)
    } finally {
      this.busy.value = false
    }
  }

  /** Reads the route now when it may be read. */
  read(): Promise<void> {
    return this.active ? this.send('GET') : Promise.resolve()
  }

  private async send(method: SignInMethod) {
    this.stop()
    const seq = ++this.seq
    try {
      const body = await this.call(method)
      if (seq !== this.seq) return
      this.view.value = asSignIn(body)
      if (method !== 'GET') this.error.value = ''
    } catch (e) {
      if (seq !== this.seq) return
      const status = statusOf(e)
      // Where the server offers no sign-in (any longer), nothing is shown.
      if (status === 404) this.view.value = null
      else if (method !== 'GET' && status !== 401) this.error.value = SIGNIN_UNAVAILABLE
    }
    if (seq === this.seq) this.next()
  }

  /** After a reply: the next read, sooner while a person waits on the sign-in. */
  private next() {
    this.stop()
    if (!this.active) return
    let after = SIGNIN_EVERY_MS
    if (waiting(this.view.value?.state)) {
      after = this.wait
      this.wait = Math.min(2 * this.wait, SIGNIN_EVERY_MS)
    } else this.wait = SIGNIN_FIRST_MS
    this.timer = setTimeout(() => {
      this.timer = undefined
      void this.read()
    }, after)
  }

  private stop() {
    if (this.timer !== undefined) clearTimeout(this.timer)
    this.timer = undefined
  }

  /** Forgets what was read and drops a reply on its way. */
  private forget() {
    this.seq++
    this.stop()
    this.wait = SIGNIN_FIRST_MS
    this.view.value = null
    this.error.value = ''
  }
}

/**
 * The Settings card's sign-in: `view` (null while there is none to show), `error` (the card's words for a begin or a sign-out the
 * provider did not answer), `busy`, `begin` and `signOut`. The caller's setup ties the reads to its own lifetime and to the document's
 * visibility, the workbench token and the identity capability.
 */
export function useSignIn() {
  const view = ref<SignInView | null>(null)
  const error = ref('')
  const busy = ref(false)
  const { request } = useApi()
  const admin = useWorkbenchToken()
  const capabilities = useCapabilities()
  const poll = new SignInPoll((method) => request<unknown>('/api/signin', { method }), view, error, busy)
  const onVisibility = () => poll.visible(document.visibilityState === 'visible')
  const stops: Array<() => void> = []
  onMounted(() => {
    poll.visible(document.visibilityState === 'visible')
    poll.token(admin.token.value)
    poll.identity(capabilities.has('identity'))
    stops.push(
      watch(admin.token, (t) => poll.token(t)),
      watch(
        () => capabilities.has('identity'),
        (on) => poll.identity(on),
      ),
    )
    document.addEventListener('visibilitychange', onVisibility)
    poll.show(true)
  })
  onBeforeUnmount(() => {
    document.removeEventListener('visibilitychange', onVisibility)
    for (const s of stops) s()
    poll.show(false)
  })
  return { view, error, busy, begin: () => poll.act('POST'), signOut: () => poll.act('DELETE') }
}
