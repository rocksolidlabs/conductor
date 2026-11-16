import { watch, type Ref } from 'vue'

/** What the token dialog reads of the workbench token's store (useWorkbenchToken). */
interface Ask {
  kept: () => string
  cancel: () => void
}

/**
 * The workbench token dialog's draft and close (WorkbenchTokenGate.vue), opened only by the person (the menu's Workbench token…, the
 * palette, Set token on an alert): opened, its draft starts from the token this browser keeps, never from one only this page holds;
 * closed (Cancel, Escape, outside it, Save), the ask ends. A refusal does not open it: the connect page asks then (connectShown).
 */
export function tokenDialog(open: Ref<boolean>, draft: Ref<string>, admin: Ask) {
  watch(open, (v) => {
    if (v) draft.value = admin.kept()
    else admin.cancel()
  })
}

/** What decides the connect page (the UX review's P18): the store's token, its standing, and in the desktop app its bridge. */
export interface ConnectFacts {
  /** The page runs in the desktop app, whose token comes from its shell's bridge. */
  desktop: boolean
  /** The bridge has answered (or failed); always true in a browser. */
  bridged: boolean
  hasToken: boolean
  standing: '' | 'trying' | 'dropped' | 'refused'
}

/**
 * Whether the connect page stands in for the app: when this page holds no token, or the server refused the one it holds. Never while
 * a link's token is tried (or, refused, the page loads again), and in the desktop app never before its bridge has answered, so the
 * page does not flash while the shell hands over its token.
 */
export function connectShown(f: ConnectFacts): boolean {
  if (f.desktop && !f.bridged) return false
  if (f.standing === 'trying' || f.standing === 'dropped') return false
  return !f.hasToken || f.standing === 'refused'
}

/**
 * Whether the page to connect says the server refused a token: only when this page holds one it sent and the server refused. A page
 * that holds none is asked for one, never told that one was refused (a read sent with none is answered 401 as well).
 */
export function refusedShown(f: Pick<ConnectFacts, 'hasToken' | 'standing'>): boolean {
  return f.standing === 'refused' && f.hasToken
}

/** What the page to connect needs to try a token: one request of its own with it, and what takes it once the server has. */
export interface TokenTry {
  /** One request with `token` as its own credential (never held back, told nothing of the store): resolves when the server took it. */
  ask: (token: string) => Promise<unknown>
  /** Takes the token: the page to connect goes, and what it held back goes once. */
  take: (token: string) => void
}

/**
 * Tries `value` with the page's one request (`ask`) and takes it only once the server has accepted it: until then the page stays and
 * everything it holds back stays held, so a wrong token costs one refused request, never a burst of them. Resolves to '' when taken,
 * else to the words the page shows: refused, too many refused tries from this address, or the server's own words.
 */
export async function tryToken(value: string, t: TokenTry): Promise<string> {
  const token = value.trim()
  if (!token) return 'Paste the workbench token first.'
  try {
    await t.ask(token)
  } catch (e) {
    const status = (e as { status?: number }).status ?? 0
    if (status === 401 || status === 403) return 'The server refused that token.'
    if (status === 429) return 'Too many refused tries from this address: wait a minute, then try again.'
    return (e as Error).message || 'Cannot reach the server.'
  }
  t.take(token)
  return ''
}
