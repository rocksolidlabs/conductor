import { providerName, readRefusal, readState, shellBridge, type ShellProvider, type ShellState } from '~/utils/shell'

// The phone shell's state as its screens read it (docs/plans/phone-shell.md, OSS-1b): the bridge (null outside a shell), the
// provider (null with none), its name, and its state as it last reported it, subscribed to once. Open a server never reads it.

const state = ref<ShellState | null>(null)
const stateWords = ref('')
let started = false

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
        state.value = readState(s)
        stateWords.value = ''
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
  }
}
