import type { Decision } from './decisions'
import type { DeliveryStep } from './inbox'

// The phone shell's bridge as the workbench reads it (docs/plans/phone-shell.md, Part two, OSS-1b, "web/app/utils/shell.ts"): v1
// whole, every type a provider and its fakes implement, and the reader. Inside the shell `window.conductorShell` is built by the
// shell's own script before the workbench starts; outside one there is none and every reader answers null. Every string a provider
// hands the screens is bounded here, cut or refused, and drawn as text. Nothing here names a service.

export interface ConductorShell {
  v: 1
  platform: 'ios' | 'android' | 'web'
  push: {
    permission(): Promise<'granted' | 'denied' | 'prompt'>
    request(): Promise<'granted' | 'denied'>
    onTap(cb: (data: Record<string, string>) => void): () => void
  }
  links: { onOpen(cb: (url: string) => void): () => void }
  openServer(address: string): Promise<{ ok: true; words?: string } | { ok: false; words: string }>
  /** At most 5, newest first. */
  servers: { recent(): Promise<string[]>; forget(): Promise<void> }
  provider: ShellProvider | null
  /** A provider module's; the workbench never calls it. */
  register(factory: unknown): void
}

/** A refusal any provider call may reject with: a code the shell knows, or the provider's own, and its words. */
export interface ShellRefusal {
  code: string
  words: string
}

export interface ShellProvider {
  /** One line, at most 40 characters. */
  name: string
  state(): Promise<ShellState>
  onState(cb: (s: ShellState) => void): () => void
  signIn(): Promise<ShellSignIn>
  cancelSignIn(): Promise<void>
  /** Takes effect on the phone at once; ends what it can when it can (ending). */
  signOut(): Promise<void>
  enableNotifications(): Promise<'on' | 'denied'>
  /** A test notification to this phone. */
  test(): Promise<{ words: string }>
  /** An opened link to an item's id, or null when it is not the provider's. */
  route(url: string): Promise<string | null>
  inbox: ShellInbox
  drafts: ShellDrafts
}

export interface ShellState {
  signedIn: boolean
  /** At most 80 characters. */
  who?: string
  notifications: 'on' | 'off' | 'denied' | 'unknown'
  /** RFC 3339. */
  lastNotifiedAt?: string
  /** Signed out here, its end not yet confirmed. */
  ending?: boolean
  /** At most 500: what the provider says of its state (cannot be reached, cannot answer now). */
  words?: string
}

export type ShellSignIn =
  /** The code at most 16 characters; the page https://, at most 200 bytes. */
  | { code: string; page: string; expiresAt: string }
  /** Why no sign-in can begin now. */
  | { words: string }

/** The current sign-in's drafts, kept by the provider. */
export interface ShellDrafts {
  get(id: string): Promise<ShellDraft | null>
  /** At most 32. */
  list(): Promise<ShellDraft[]>
  put(d: ShellDraft): Promise<{ ok: true } | { ok: false; code: 'full' | 'too_large' | 'not_saved' | 'signed_out'; words: string }>
  remove(id: string): Promise<void>
}

/** At most 12 KiB as JSON. */
export interface ShellDraft {
  /** The item's id. */
  id: string
  /** Minted once, when the draft begins. */
  responseId: string
  /** The decision's rev it was written against. */
  rev: number
  choice?: number
  text?: string
  rationale?: string
  state: 'editing' | 'waiting' | 'unknown'
  /** Set by the provider with state 'unknown', before a send begins: what it sends, hashed in its own form. */
  h?: string
  savedAt: string
}

export interface ShellAnswer {
  responseId: string
  expectedRev: number
  choice?: number
  text?: string
  rationale?: string
}

export interface ShellReply {
  item?: ShellItem
  outcome: 'kept' | 'same' | 'refused' | 'unknown'
  code?: string
  words?: string
}

export type ShellAnswerOf = { state: string; words: string; h: string } | { state: 'none' } | { code: 'unreachable'; words: string }

export interface ShellInbox {
  list(after?: string): Promise<{ items: ShellItem[]; next?: string; asOf: string }>
  /** Rejects a ShellRefusal: not_visible, unreachable, offline. */
  item(id: string): Promise<ShellItem>
  respond(id: string, a: ShellAnswer): Promise<ShellReply>
  /** This person's answer under that responseId, in any state. */
  answerOf(id: string, responseId: string): Promise<ShellAnswerOf>
  resolve(handle: string): Promise<{ id: string } | { code: 'unknown_handle' | 'unreachable'; words: string }>
  /** While the app is in the foreground only. */
  watch(cb: (id: string) => void): () => void
}

export interface ShellItem {
  /** The provider's id of the item, at most 64 bytes. */
  id: string
  /** DR1's record (utils/decisions.ts) as the provider holds it: history, comments and evidence contents may be absent. */
  decision: Decision
  /** Responses the provider holds and does not hand. */
  responsesOmitted?: number
  /** MB0's four steps (utils/inbox.ts), exactly as given. */
  rows: DeliveryStep[]
  /** This person's latest answer, if any. */
  own?: { responseId: string; state: string; words: string }
  /** The server that holds the decision, as the provider last heard from it. */
  owner: { name: string; reportedAt?: string }
  asOf: string
  /** Drawn from what the provider kept, not read now. */
  offline: boolean
  /** Words for where it is answered when the provider takes no answer from the phone: no Answer is drawn. */
  elsewhere?: string
}

/** The bounds of what a provider hands the screens. */
export const SHELL_BOUNDS = { name: 40, who: 80, words: 500, code: 16, page: 200, refusalCode: 32, handle: 256, itemId: 64 } as const

/** A text bounded to max characters, one line, as text: a control character (a line break among them) becomes a space; past max it is cut with an ellipsis. */
export function boundLine(s: unknown, max: number): string {
  if (typeof s !== 'string') return ''
  // eslint-disable-next-line no-control-regex
  const flat = s.replace(/[\u0000-\u001f\u007f-\u009f]/g, ' ')
  const chars = [...flat]
  return chars.length > max ? `${chars.slice(0, max - 1).join('')}…` : flat
}

/** A refusal's code: 1 to 32 bytes of a-z and _, or none. */
export function boundCode(c: unknown): string | null {
  return typeof c === 'string' && /^[a-z_]{1,32}$/.test(c) ? c : null
}

/** A sign-in page: https:// of at most 200 bytes with no control character or space, or none. */
export function boundPage(p: unknown): string | null {
  if (typeof p !== 'string' || new TextEncoder().encode(p).length > SHELL_BOUNDS.page || !/^https:\/\/[^\s\u0000-\u001f\u007f-\u009f]+$/.test(p)) return null
  try {
    return new URL(p).protocol === 'https:' ? p : null
  } catch {
    return null
  }
}

/** A time a provider hands (RFC 3339), or none. */
function boundTime(t: unknown): string | undefined {
  return typeof t === 'string' && t.length <= 40 && !Number.isNaN(Date.parse(t)) ? t : undefined
}

/** The provider's name as the screens draw it: one line of at most 40 characters, "The provider" for none. */
export function providerName(p: Pick<ShellProvider, 'name'> | null | undefined): string {
  return boundLine(p?.name, SHELL_BOUNDS.name).trim() || 'The provider'
}

/** A provider's state as the screens read it, every string bounded; a state not of its shape is signed out with no words. */
export function readState(s: unknown): ShellState {
  const o = (s && typeof s === 'object' ? s : {}) as Record<string, unknown>
  const notifications = o.notifications === 'on' || o.notifications === 'off' || o.notifications === 'denied' ? o.notifications : 'unknown'
  const out: ShellState = { signedIn: o.signedIn === true, notifications }
  const who = boundLine(o.who, SHELL_BOUNDS.who).trim()
  if (who) out.who = who
  const last = boundTime(o.lastNotifiedAt)
  if (last) out.lastNotifiedAt = last
  if (o.ending === true) out.ending = true
  const words = boundLine(o.words, SHELL_BOUNDS.words).trim()
  if (words) out.words = words
  return out
}

/** A sign-in as the screen reads it: a code, its page and its expiry, or words; one with a code past 16 characters or a page that is not https:// of at most 200 bytes is words. */
export function readSignIn(r: unknown): { code: string; page: string; expiresAt: string } | { words: string } {
  const o = (r && typeof r === 'object' ? r : {}) as Record<string, unknown>
  if (typeof o.code === 'string') {
    const page = boundPage(o.page)
    const expiresAt = boundTime(o.expiresAt)
    // eslint-disable-next-line no-control-regex
    if (page && expiresAt && o.code.length >= 1 && [...o.code].length <= SHELL_BOUNDS.code && !/[\s\u0000-\u001f\u007f-\u009f]/.test(o.code)) return { code: o.code, page, expiresAt }
    return { words: 'The sign-in could not begin: what came back is not a sign-in this phone can show.' }
  }
  return { words: boundLine(o.words, SHELL_BOUNDS.words).trim() || 'No sign-in can begin now.' }
}

/** A provider call's refusal as the screens read it: its code (one the shell knows, or the provider's own, else "refused") and its words. */
export function readRefusal(e: unknown, fallback = 'It did not work. Try again.'): ShellRefusal {
  const o = (e && typeof e === 'object' ? e : {}) as Record<string, unknown>
  return { code: boundCode(o.code) ?? 'refused', words: boundLine(o.words, SHELL_BOUNDS.words).trim() || fallback }
}

/** Words of a reply (a test notification's), bounded. */
export function readWords(r: unknown, fallback: string): string {
  const o = (r && typeof r === 'object' ? r : {}) as Record<string, unknown>
  return boundLine(o.words, SHELL_BOUNDS.words).trim() || fallback
}

let read = false
let cached: ConductorShell | null = null

/** What a page reads the bridge from: the window, and the console a bridge it ignores is said on. */
export interface ShellHost {
  conductorShell?: unknown
}

/**
 * The bridge, read once (the workbench reads it at its start): null outside a shell; a bridge whose `v` is not 1, or that is not one,
 * ignored with one console line.
 */
export function shellBridge(host: ShellHost | undefined = typeof window === 'undefined' ? undefined : (window as ShellHost)): ConductorShell | null {
  if (read) return cached
  read = true
  cached = readBridge(host?.conductorShell)
  return cached
}

/** A bridge read again (the tests', and a reader handed another window). */
export function readBridge(b: unknown): ConductorShell | null {
  if (b === undefined || b === null) return null
  const o = b as Partial<ConductorShell>
  if (typeof o !== 'object' || o.v !== 1) {
    console.warn(`conductorShell: version ${typeof o === 'object' ? String(o.v) : 'unknown'} is not one this workbench reads; the shell's bridge is ignored`)
    return null
  }
  if (typeof o.openServer !== 'function' || typeof o.servers?.recent !== 'function' || typeof o.push?.onTap !== 'function' || typeof o.links?.onOpen !== 'function') {
    console.warn("conductorShell: the bridge lacks a part of v1; the shell's bridge is ignored")
    return null
  }
  return o as ConductorShell
}

/** Forgets the bridge read (the tests'). */
export function resetShellBridge(): void {
  read = false
  cached = null
}

/** Where the shell keeps its pages: /shell and its children. */
export function inShellPath(path: string): boolean {
  return path === '/shell' || path.startsWith('/shell/') || path.startsWith('/shell?') || path.startsWith('/shell#')
}

/** Where the middleware sends a page: inside a shell every address but /shell's goes to /shell; outside one /shell's go to /. */
export function shellRedirect(path: string, inShell: boolean): string | null {
  if (inShell) return inShellPath(path) ? null : '/shell'
  return inShellPath(path) ? '/' : null
}

/** The time left of a sign-in's code, in words: "Expires in 9 min", "Expires in less than a minute", or "" once it has expired. */
export function timeLeft(expiresAt: string, now: number): string {
  const ms = Date.parse(expiresAt) - now
  if (!(ms > 0)) return ''
  const min = Math.floor(ms / 60_000)
  return min < 1 ? 'Expires in less than a minute' : `Expires in ${min} min`
}

/** The notifications' state in words, with the last one received when the provider says. */
export function notificationWords(s: Pick<ShellState, 'notifications' | 'lastNotifiedAt'>, clock: (iso: string) => string): string {
  const state = { on: 'On', off: 'Off', denied: 'Turned off in the phone’s settings', unknown: 'Not known' }[s.notifications]
  return s.lastNotifiedAt ? `${state} · the last received at ${clock(s.lastNotifiedAt)}` : state
}

/** The words while a sign-out's end is not yet confirmed. */
export function endingWords(name: string): string {
  return `Signed out on this phone; ${name} will be told when it can be reached`
}
