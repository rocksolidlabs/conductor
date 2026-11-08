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
 * this run, read from its bridge and kept in memory only.
 */
export function useWorkbenchToken() {
  const desktop = import.meta.client ? desktopBridge() : null
  const token = useState<string>('workbenchToken', () => (import.meta.client && !desktop ? readStored() : ''))
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

  function set(value: string) {
    token.value = value.trim()
    if (!desktop) {
      try {
        if (token.value) localStorage.setItem(STORAGE_KEY, token.value)
        else localStorage.removeItem(STORAGE_KEY)
      } catch {
        /* private mode */
      }
    }
    if (token.value) needsToken.value = false
  }

  function clear() {
    set('')
  }

  /**
   * A refusal (401) of a request sent with `sent`, the token the browser held as it sent it: the gate asks for the token only while that
   * is still the one held. A reply to a request made with the token before, landing after another was saved, asks for nothing: the
   * token held now has not been refused (#281). A request no token went with is refused for the one held when none is.
   */
  function refused(sent: string) {
    if (sent === token.value) needsToken.value = true
  }

  return {
    token,
    hasToken: computed(() => token.value.length > 0),
    needsToken,
    set,
    clear,
    refused,
  }
}
