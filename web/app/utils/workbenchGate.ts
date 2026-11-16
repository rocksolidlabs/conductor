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
