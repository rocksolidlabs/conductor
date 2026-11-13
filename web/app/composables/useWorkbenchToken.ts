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

/** The tab's own storage key of a later link's token, handed to the next load of the tab, which takes it and removes it. */
const LINK_KEY = 'conductor.workbenchTokenLink'

/**
 * The workbench token typed by the operator, kept in localStorage for this browser; in the desktop app, the token the shell minted for
 * this run, read from its bridge and kept in memory only. A link's token is sent by the page loaded with it and kept once the server
 * accepts it (`fromLink`, `answered`).
 */
export function useWorkbenchToken() {
  const desktop = import.meta.client ? desktopBridge() : null
  /** The token this page sends. */
  const token = useState<string>('workbenchToken', () => (import.meta.client && !desktop ? readStored() : ''))
  /** 'trying': `token` is a link's the server has not answered yet; 'refused': the server refused it, and the page loads again. */
  const link = useState<'' | 'trying' | 'refused'>('workbenchTokenLink', () => '')
  /** `token` is held for this page alone: the browser refused to keep it. */
  const unkept = useState<boolean>('workbenchTokenUnkept', () => false)
  const needsToken = useState<boolean>('workbenchTokenNeeded', () => false)
  const asked = useState<boolean>('workbenchTokenAskedDesktop', () => false)
  if (desktop && !asked.value) {
    asked.value = true
    desktop
      .token()
      .then((t) => {
        if (t) {
          token.value = t
          needsToken.value = false
        }
      })
      .catch(() => {})
  }

  /** Keeps `value` in this browser's storage ('' removes it); `unkept` when the browser refuses. */
  function keep(value: string) {
    try {
      if (value) localStorage.setItem(STORAGE_KEY, value)
      else localStorage.removeItem(STORAGE_KEY)
      unkept.value = false
    } catch {
      unkept.value = true /* private mode */
    }
  }

  /** The operator's token: sent and kept at once. */
  function set(value: string) {
    token.value = value.trim()
    link.value = ''
    if (!desktop) keep(token.value)
    if (token.value) needsToken.value = false
  }

  function clear() {
    set('')
  }

  /** What this browser's storage holds, '' for none or when it refuses to be read. */
  function inStorage(): string {
    try {
      return localStorage.getItem(STORAGE_KEY) ?? ''
    } catch {
      return ''
    }
  }

  /** The token this browser keeps, '' for none: its storage's; the page's when the browser refused to keep it, or in the desktop app. */
  const kept = () => (desktop || unkept.value ? token.value : inStorage())

  /**
   * A load's link token, or with none the token a later link handed to this load (`handOn`), which is taken and removed: sent by this
   * page and kept once the server accepts it, the browser's storage as it was until then. In the desktop app, held as the operator's.
   */
  function fromLink(value: string) {
    try {
      value ||= sessionStorage.getItem(LINK_KEY) ?? ''
      sessionStorage.removeItem(LINK_KEY)
    } catch {
      /* the tab's storage refused: nothing was handed on */
    }
    if (!value) return
    if (desktop) return set(value)
    link.value = value === kept() ? '' : 'trying'
    token.value = value
    needsToken.value = false
  }

  /**
   * A later link's token, handed to the next load of this tab in the tab's own storage, never to what this page sends or the browser
   * keeps: true when it was taken, and the page is to be loaded again. Nothing is handed on for the token this page sends or the
   * browser keeps, for a page whose token the browser refused to keep, or in the desktop app.
   */
  function handOn(value: string): boolean {
    if (desktop || unkept.value || value === token.value || value === kept()) return false
    try {
      sessionStorage.setItem(LINK_KEY, value)
      return true
    } catch {
      return false
    }
  }

  /**
   * An answer to a request sent with `sent`, the token this page held as it sent it: `accepted` (a 2xx) or refused (a 401). An
   * accepted link's token is kept. A refused link's token is dropped: the page loads again at its address and sends the token the
   * browser keeps. A refused token is kept no longer: removed from the storage while it still holds it, and no longer held for the
   * page alone. The gate asks for the token only while the refused one is still the one held: a reply for a token since replaced asks
   * for nothing (#281). A request no token went with is refused for the one held when none is.
   */
  function answered(sent: string, accepted: boolean) {
    const mine = sent === token.value
    if (mine && link.value === 'refused') return
    if (accepted) {
      if (mine && link.value === 'trying') {
        link.value = ''
        keep(sent)
      }
      return
    }
    if (mine && link.value === 'trying') {
      link.value = 'refused'
      location.reload()
      return
    }
    if (sent && !desktop && inStorage() === sent) keep('')
    if (mine) {
      unkept.value = false
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
    handOn,
    answered,
  }
}
