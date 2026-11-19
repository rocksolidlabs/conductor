import { ref } from 'vue'
import { readRefusal, readState, withinWait, type ShellItem, type ShellProvider, type ShellState } from './shell'

// The provider's state as the phone shell's screens hold it (docs/plans/phone-shell.md, OSS-1b and OSS-2b): its last report, the
// words of a read that failed, and the inbox it last listed. A sign-out is one change, however it is seen (the provider's own
// report, or a read of its state), and it is cleaned up once, the same way: nothing begun before it writes a card, a draft or a
// state again (`signedOut`), and the inbox kept for a provider that cannot be reached is dropped.

export function createShellState(provider: Pick<ShellProvider, 'state' | 'onState'>, o: { name: string; signedOut: () => void; wait?: number }) {
  const state = ref<ShellState | null>(null)
  const stateWords = ref('')
  /** The inbox as the provider last listed it, for a provider that cannot be reached now; dropped at a sign-out. */
  const lastInbox = ref<{ items: ShellItem[]; asOf: string } | null>(null)

  /** A state read or reported: a change from signed in to signed out does the sign-out's cleanup. */
  function apply(next: ShellState): void {
    const was = state.value?.signedIn
    state.value = next
    stateWords.value = ''
    if (was && !next.signedIn) {
      o.signedOut()
      lastInbox.value = null
    }
  }

  /** Reads the provider's state now, within the wait; a refusal's words kept, the last state kept with them. */
  async function refresh(): Promise<void> {
    try {
      apply(readState(await withinWait(provider.state(), `${o.name} did not say whether this phone is signed in.`, o.wait)))
    } catch (e) {
      stateWords.value = readRefusal(e, `${o.name} could not say whether this phone is signed in.`).words
    }
  }

  /** Subscribes to the provider's reports and reads its state once. */
  function start(): Promise<void> {
    try {
      provider.onState((s) => apply(readState(s)))
    } catch {
      // A provider that cannot tell its changes is read again by the screens that act on it (refresh).
    }
    return refresh()
  }

  return { state, stateWords, lastInbox, apply, refresh, start }
}
