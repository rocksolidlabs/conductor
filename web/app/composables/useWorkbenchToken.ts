import { desktopBridge } from '~/utils/desktop'

const STORAGE_KEY = 'conductor.workbenchToken'
/** The key before the token was renamed; a value found there moves over once. */
const OLD_STORAGE_KEY = 'conductor.adminToken'

function readStored(): string {
  try {
    const now = localStorage.getItem(STORAGE_KEY)
    if (now) return now
    const old = localStorage.getItem(OLD_STORAGE_KEY)
    if (old) {
      localStorage.setItem(STORAGE_KEY, old)
      localStorage.removeItem(OLD_STORAGE_KEY)
      return old
    }
    return ''
  } catch {
    return ''
  }
}

/**
 * The workbench token typed by the operator, kept in localStorage for this browser; in the desktop app, the token the shell minted for
 * this run, read from its bridge and kept in memory only. A link's token is sent by the page it came with and kept once the server
 * accepts it, when a read of the browser's storage finds no other (`fromLink`, `answered`); the token the browser keeps is changed by
 * the operator alone (`set`, `clear`).
 */
export function useWorkbenchToken() {
  const desktop = import.meta.client ? desktopBridge() : null
  /** The token this page sends. */
  const token = useState<string>('workbenchToken', () => (import.meta.client && !desktop ? readStored() : ''))
  /**
   * What this page knows of `token`: '' nothing against it; 'trying' a link's the server has not answered yet; 'dropped' a link's the
   * server refused, while the page loads again; 'refused' the server refused it, and the gate asks for another.
   */
  const standing = useState<'' | 'trying' | 'dropped' | 'refused'>('workbenchTokenStanding', () => '')
  const needsToken = useState<boolean>('workbenchTokenNeeded', () => false)
  const asked = useState<boolean>('workbenchTokenAskedDesktop', () => false)
  if (desktop && !asked.value) {
    asked.value = true
    desktop
      .token()
      .then((t) => {
        if (t) set(t)
      })
      .catch(() => {})
  }

  /** What this browser's storage holds: '' for none, null when it cannot be read. */
  function inStorage(): string | null {
    try {
      return localStorage.getItem(STORAGE_KEY) ?? ''
    } catch {
      return null
    }
  }

  /** The token this browser keeps, which the gate's draft starts from: '' when it keeps none or cannot be read, and in the desktop app. */
  const kept = () => (desktop ? '' : (inStorage() ?? ''))

  /** Keeps `value` in this browser's storage ('' removes it); a browser that refuses keeps what it kept. */
  function keep(value: string) {
    try {
      if (value) localStorage.setItem(STORAGE_KEY, value)
      else localStorage.removeItem(STORAGE_KEY)
    } catch {
      /* private mode */
    }
  }

  /** The operator's token: sent and kept at once, with no refusal against it. */
  function set(value: string) {
    token.value = value.trim()
    standing.value = ''
    if (!desktop) keep(token.value)
    if (token.value) needsToken.value = false
  }

  function clear() {
    set('')
  }

  /** Whether this page holds a token the server has not refused: a link's being tried counts, and so does one the browser does not keep. */
  const holds = () => !!token.value && standing.value !== 'refused'

  /**
   * A link's token, at a load: the token this page holds (at a load, the kept one) changes nothing; any other is sent by this page and
   * tried, kept once the server accepts it (`answered`), the browser's storage as it was until then. In the desktop app the bridge's
   * token is the workbench's, and a link's is dropped.
   */
  function fromLink(value: string) {
    if (!value || desktop || value === token.value) return
    standing.value = 'trying'
    token.value = value
  }

  /**
   * A link's token that arrives after the load, on an address that is not a decide address (the fragment plugin): taken as a load's
   * only while this page holds none, or one it has seen refused; else dropped. This page's own token, refused or not, changes nothing.
   */
  function fromLaterLink(value: string) {
    if (!holds()) fromLink(value)
  }

  /**
   * The decide page's rule (decide.global.ts): this page is the operator's while it holds a token it has not seen refused. A refused
   * link's token counts no more while the page loads again, and a load counts the kept token again.
   */
  const operator = () => holds() && standing.value !== 'dropped'

  /**
   * An answer to a request sent with `sent`, the token this page held as it sent it: `accepted` (the events stream itself answering,
   * which the server does for an operator's credential; openEvents) or refused (a 401 of any request). An answer for a token since
   * replaced decides nothing (#281). An accepted link's token is kept when a read of the browser's storage finds no token (one that
   * cannot be read keeps nothing), and the gate is done. A refused link's
   * token is dropped: the page loads again at its address and sends the token the browser keeps. Any other refused token stays as the
   * browser keeps it, and the gate asks for another; a request no token went with is refused for the one held when none is.
   */
  function answered(sent: string, accepted: boolean) {
    if (sent !== token.value || standing.value === 'dropped') return
    if (standing.value === 'trying') {
      standing.value = accepted ? '' : 'dropped'
      if (!accepted) return location.reload()
      if (inStorage() === '') keep(sent)
      needsToken.value = false
    } else if (!accepted) {
      standing.value = 'refused'
      needsToken.value = true
    }
  }

  return {
    token,
    hasToken: computed(() => token.value.length > 0),
    needsToken,
    set,
    clear,
    kept,
    fromLink,
    fromLaterLink,
    operator,
    answered,
  }
}
