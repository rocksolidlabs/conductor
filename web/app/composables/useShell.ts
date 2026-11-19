import { itemId, providerName, readRefusal, readState, shellBridge, tapTarget, type ShellItem, type ShellProvider, type ShellState } from '~/utils/shell'
import { shellAnswering } from '~/utils/transport/shell'

// The phone shell's state as its screens read it (docs/plans/phone-shell.md, OSS-1b): the bridge (null outside a shell), the
// provider (null with none), its name, and its state as it last reported it, subscribed to once. Open a server never reads it.

const state = ref<ShellState | null>(null)
const stateWords = ref('')
let started = false
let answer: ReturnType<typeof shellAnswering> | null = null
/** The inbox as the provider last listed it, for a provider that cannot be reached now; dropped at a sign-out. */
const lastInbox = ref<{ items: ShellItem[]; asOf: string } | null>(null)

/** Reads the provider's state now; its refusal's words kept, the last state kept with them. */
async function refresh(p: ShellProvider): Promise<void> {
  try {
    state.value = readState(await p.state())
    stateWords.value = ''
  } catch (e) {
    stateWords.value = readRefusal(e, 'The provider could not say whether this phone is signed in.').words
  }
}

export function useShell() {
  const bridge = shellBridge()
  const provider = bridge?.provider ?? null
  if (provider && !started && import.meta.client) {
    started = true
    try {
      provider.onState((s) => {
        const was = state.value?.signedIn
        state.value = readState(s)
        stateWords.value = ''
        // A sign-out: nothing begun before it writes a card, a draft or a state (A4).
        if (was && !state.value.signedIn) {
          answer?.machine.signedOut()
          lastInbox.value = null
        }
      })
    } catch {
      // A provider that cannot tell its changes is read again by the screens that act on it.
    }
    void refresh(provider)
  }
  return {
    bridge,
    provider,
    name: providerName(provider),
    state: readonly(state),
    stateWords: readonly(stateWords),
    refresh: () => (provider ? refresh(provider) : Promise.resolve()),
    lastInbox,
    /** The provider's answering (its answer machine and the DecisionAnswering), made once. */
    answer: () => {
      if (!provider) return null
      answer ??= shellAnswering(provider, providerName(provider))
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
