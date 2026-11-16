import { computed } from 'vue'
import { needsYou, type NeedsYou } from '~/utils/needsYou'

/**
 * The one list of what needs you (utils/needsYou.ts), over the app's three stores: the attention's sessions, the durable decisions and
 * the inbox. Every count of it reads this: `number` with `known` and `more`, never a store's own count.
 */
export function useNeedsYou() {
  const attention = useAttention()
  const decisions = useDecisions()
  const inbox = useInbox()
  const list = computed<NeedsYou>(() =>
    needsYou({
      sessions: { list: attention.sessions.value, loaded: attention.loaded.value, error: attention.error.value },
      decisions: { records: decisions.decisions.value, loaded: decisions.loaded.value, error: decisions.stale.value.error, cut: decisions.cut.value, unavailable: decisions.unavailable.value },
      inbox: inbox.state.value,
      runOf: attention.runOf,
    }),
  )
  return {
    list,
    items: computed(() => list.value.items),
    answerable: computed(() => list.value.answerable),
    runs: computed(() => list.value.runs),
    number: computed(() => list.value.number),
    known: computed(() => list.value.known),
    more: computed(() => list.value.more),
  }
}
