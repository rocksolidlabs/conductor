import type { RecipientPick } from '~/utils/crews'

/** How many runs the tab keeps a pick and a line for: past it the one used least lately goes. */
export const RUN_RECIPIENTS_MAX = 30

interface Kept {
  pick: RecipientPick
  text: string
  used: number
}

/**
 * Who gets the next line from a run's message bar, and the line begun, kept for each run for the tab's life (in the app's state, not
 * the page's storage): a trip to a member and back loses neither, and a reload forgets both, as before. `pick` is null while `All` is
 * on (broadcastSelection). At most RUN_RECIPIENTS_MAX runs are kept.
 */
export function useRunRecipients(runId: Ref<string> | ComputedRef<string>) {
  const kept = useState<Record<string, Kept>>('runRecipients', () => ({}))
  let tick = 0
  function entry(): Kept {
    return kept.value[runId.value] ?? { pick: null, text: '', used: 0 }
  }
  function put(patch: Partial<Omit<Kept, 'used'>>) {
    const next = { ...kept.value, [runId.value]: { ...entry(), ...patch, used: Date.now() + tick++ / 1000 } }
    const ids = Object.keys(next)
    if (ids.length > RUN_RECIPIENTS_MAX) {
      ids.sort((a, b) => next[a]!.used - next[b]!.used)
      for (const id of ids.slice(0, ids.length - RUN_RECIPIENTS_MAX)) delete next[id]
    }
    kept.value = next
  }
  const pick = computed<RecipientPick>({
    get: () => entry().pick,
    set: (p) => put({ pick: p }),
  })
  const text = computed<string>({
    get: () => entry().text,
    set: (t) => put({ text: t }),
  })
  return { pick, text }
}
