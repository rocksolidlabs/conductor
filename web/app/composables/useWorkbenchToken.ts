import { computed, effectScope, ref, watch, type ComputedRef, type Ref } from 'vue'
import { desktopBridge } from '~/utils/desktop'
import { connectShown } from '~/utils/workbenchGate'

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

/** What the store knows of the token this page sends (`useWorkbenchToken`). */
interface Held {
  /** The token this page sends. */
  token: Ref<string>
  /** How many times `token` has changed in this page: what a list read with one token compares, so that it keeps no copy of it. */
  changes: Ref<number>
  /**
   * What this page knows of `token`: '' nothing against it; 'trying' a link's the server has not answered yet; 'dropped' a link's the
   * server refused, while the page loads again; 'refused' the server refused it, and the gate asks for another.
   */
  standing: Ref<'' | 'trying' | 'dropped' | 'refused'>
  needsToken: Ref<boolean>
  /** Whether the desktop app's bridge has answered its token (or failed): true at once in a browser, which has no bridge. */
  bridged: Ref<boolean>
  /** How many mounted layouts put the page to connect in place of the app while it is due (`hold`): the default layout's one. */
  gates: Ref<number>
  /** How many of the app's requests have waited for the page to connect to go (`waited`): what a test reads to see them held. */
  waited: Ref<number>
  /** The page to connect stands in for the app (`gated`): made once, with the store, in a scope no component owns. */
  gated?: ComputedRef<boolean>
  /**
   * The wait of the requests held while the page to connect shows: one for each time it shows, resolved when it goes, `true` when a
   * token the server took is held then (the held reads go), `false` when it went with none (a layout of another kind: they are dropped).
   */
  period: { promise: Promise<boolean>; resolve: (taken: boolean) => void } | null
  /**
   * A token that came while the page to connect shows (a link's fragment, the desktop app's bridge), for the page to try with its one
   * request before it is taken (`take`); '' for none.
   */
  offered: Ref<{ value: string; link: boolean } | null>
}

/**
 * Where the store lives: a property of this page's Nuxt app, in the page's memory and outside its payload, so no state Nuxt writes
 * at a reload it makes itself (`nuxt:reload:state`, the payload's `state`) holds the token or a copy of it.
 */
const HELD = '_conductorWorkbenchToken'

/**
 * The workbench token typed by the operator, kept in localStorage for this browser; in the desktop app, the token the shell minted for
 * this run, read from its bridge and kept in memory only. A link's token is sent by the page it came with and kept once the server
 * accepts it, when a read of the browser's storage finds no other (`fromLink`, `answered`); the token the browser keeps is changed by
 * the operator alone (`set`, `clear`).
 */
export function useWorkbenchToken() {
  const desktop = import.meta.client ? desktopBridge() : null
  const app = useNuxtApp() as unknown as { [HELD]?: Held }
  const first = !app[HELD]
  const held = (app[HELD] ??= { token: ref(desktop ? '' : readStored()), changes: ref(0), standing: ref<Held['standing']['value']>(''), needsToken: ref(false), bridged: ref(!desktop), gates: ref(0), waited: ref(0), period: null, offered: ref(null) })
  const { token, standing, needsToken, bridged } = held
  /**
   * The page to connect stands in for the app (no token held, or the server refused the one held) under a layout that draws it: the
   * app's reads that would carry that token wait (`connected`), so a refused tab spends nothing of the server's limit of refused
   * requests for its address, which the next person's token tries and link joins from that address share.
   */
  if (!held.gated)
    // Made once in a scope of its own: a component that first asked for the store, unmounted, stops nothing a later wait reads.
    effectScope(true).run(() => {
      const g = computed(() => held.gates.value > 0 && connectShown({ desktop: !!desktop, bridged: bridged.value, hasToken: token.value.length > 0, standing: standing.value }))
      held.gated = g
      watch(g, (up) => {
        if (up || !held.period) return
        const p = held.period
        held.period = null
        // Gone with a token the server did not refuse held: a token was taken. Gone otherwise (no layout draws the page): dropped.
        p.resolve(token.value.length > 0 && standing.value !== 'refused')
        // At once: a gate that goes up and down within one tick still lets what it held go.
      }, { flush: 'sync' })
    })
  const gated = held.gated!
  if (desktop && first)
    desktop
      .token()
      .then((t) => {
        // While the page to connect shows, the bridge's token is tried by the page's one request before it is taken.
        if (t && gated.value) held.offered.value = { value: t, link: false }
        else if (t) set(t)
      })
      .catch(() => {})
      .finally(() => (bridged.value = true))

  /** Every write of `token` is made here, and a change is counted. */
  function send(value: string) {
    if (value !== token.value) held.changes.value++
    token.value = value
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
    send(value.trim())
    standing.value = ''
    if (!desktop) keep(token.value)
    if (token.value) needsToken.value = false
  }

  function clear() {
    set('')
  }

  /** The gate closed with no token saved: the ask ends, so the next refusal asks again. */
  function cancel() {
    needsToken.value = false
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
    send(value)
  }

  /**
   * A link's token that arrives after the load, on an address that is not a decide address (the fragment plugin): taken as a load's
   * only while this page holds none, or one it has seen refused; else dropped. This page's own token, refused or not, changes nothing.
   */
  function fromLaterLink(value: string) {
    if (holds()) return
    // While the page to connect shows, the link's token is tried by the page's one request first: nothing held goes with it untried.
    if (!gated.value) return fromLink(value)
    if (value && !desktop) held.offered.value = { value, link: true }
  }

  /**
   * A token the server took, tried by the page to connect with its one request: the operator's (`set`), or a link's, kept as an
   * accepted link's is (when the browser's storage holds none). The page to connect goes, and what it held back goes once.
   */
  function take(value: string, link = false) {
    held.offered.value = null
    if (!link) return set(value)
    send(value.trim())
    standing.value = ''
    if (inStorage() === '') keep(token.value)
    needsToken.value = false
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

  /**
   * Resolves once the page to connect is gone: `true` when a token the server took is held then, `false` when it went with none; at once
   * (`true`) when it is not shown. Rejects with an AbortError when `signal` aborts first, or has already, as an aborted fetch does. The
   * wait is the store's, not a component's: a request held from a component that then unmounts still resolves.
   */
  function connected(signal?: AbortSignal): Promise<boolean> {
    if (!gated.value) return Promise.resolve(true)
    const aborted = () => new DOMException('aborted while the page to connect was shown', 'AbortError')
    if (signal?.aborted) return Promise.reject(aborted())
    held.waited.value++
    if (!held.period) {
      let resolve!: (taken: boolean) => void
      const promise = new Promise<boolean>((r) => (resolve = r))
      held.period = { promise, resolve }
    }
    const wait = held.period.promise
    if (!signal) return wait
    return new Promise<boolean>((resolve, reject) => {
      const on = () => reject(aborted())
      signal.addEventListener('abort', on, { once: true })
      void wait.then((taken) => {
        signal.removeEventListener('abort', on)
        resolve(taken)
      })
    })
  }

  /** A layout that draws the page to connect in place of the app: the gate holds while it is mounted. Returns the release. */
  function hold(): () => void {
    held.gates.value++
    let released = false
    return () => {
      if (released) return
      released = true
      held.gates.value--
    }
  }

  return {
    token,
    changes: held.changes,
    gated,
    connected,
    hold,
    take,
    offered: held.offered,
    waited: held.waited,
    standing,
    bridged,
    desktop: !!desktop,
    hasToken: computed(() => token.value.length > 0),
    needsToken,
    set,
    clear,
    cancel,
    kept,
    fromLink,
    fromLaterLink,
    operator,
    answered,
  }
}
