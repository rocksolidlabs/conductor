import { itemId, providerName, shellBridge, tapTarget, type ShellItem, type ShellState } from '~/utils/shell'
import { createShellState } from '~/utils/shellState'
import { shellAnswering } from '~/utils/transport/shell'

// The phone shell's state as its screens read it (docs/plans/phone-shell.md, OSS-1b): the bridge (null outside a shell), the
// provider (null with none), its name, and its state as it last reported it (utils/shellState.ts), subscribed to once. Open a
// server never reads it.

let store: ReturnType<typeof createShellState> | null = null
let answer: ReturnType<typeof shellAnswering> | null = null
/** The state's refs with no provider: nothing is read. */
const none = { state: ref<ShellState | null>(null), stateWords: ref(''), lastInbox: ref<{ items: ShellItem[]; asOf: string } | null>(null) }

export function useShell() {
  const bridge = shellBridge()
  const provider = bridge?.provider ?? null
  const name = providerName(provider)
  if (provider && !store && import.meta.client) {
    store = createShellState(provider, { name, signedOut: () => answer?.signedOut() })
    void store.start()
  }
  const s = store ?? none
  return {
    bridge,
    provider,
    name,
    state: readonly(s.state),
    stateWords: readonly(s.stateWords),
    refresh: () => store?.refresh() ?? Promise.resolve(),
    lastInbox: s.lastInbox,
    /** The provider's answering (its answer machine, and the DecisionAnswering for an item's page), made once. */
    answer: () => {
      if (!provider) return null
      answer ??= shellAnswering(provider, name)
      return answer
    },
  }
}

let routed = false

/**
 * The ways into the shell's pages (docs/plans/phone-shell.md, OSS-2b, "The routes into the page"), listened for once: a notification
 * tapped opens its handle's item (`tapTarget`); a link the shell is opened with goes to the provider's `route`, its item opened, or
 * the inbox for a link it does not know, and with no provider, Open a server with the address filled in and not opened.
 */
export function useShellRoutes(): void {
  const bridge = shellBridge()
  if (!bridge || routed || !import.meta.client) return
  routed = true
  const provider = bridge.provider
  bridge.push.onTap((data) => void navigateTo(tapTarget(data)))
  bridge.links.onOpen(async (url) => {
    if (!provider) return void navigateTo({ path: '/shell', query: { address: url } })
    const id = itemId(await provider.route(url).catch(() => null))
    await navigateTo(id ? `/shell/decisions/${encodeURIComponent(id)}` : '/shell')
  })
}
