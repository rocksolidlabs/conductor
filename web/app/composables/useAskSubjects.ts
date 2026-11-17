import type { Mention, MentionSources } from '~/utils/brakemanAsk'
import { mentionMatches } from '~/utils/brakemanAsk'
import { itemWords } from '~/utils/sidebar'

/**
 * What the ask bar's `@` list offers: only what the person's page already holds (the live runs with their tasks and the sessions,
 * the questions waiting on Needs you, and the palette's open tickets, read on the first `@` and cached as the palette caches them),
 * plus a ref typed whole. It reads no list of its own, and it runs no copy of Brakeman's scope: the server resolves every subject
 * under the turn's grant, and refuses what that does not cover.
 */
export function useAskSubjects() {
  const live = useAttention()
  const needs = useNeedsYou()
  const palette = usePalette()
  let ticketsAsked = false

  const sources = computed<MentionSources>(() => ({
    runs: live.runs.value,
    sessions: live.sessions.value,
    questions: needs.items.value
      .filter((i) => i.kind === 'question')
      .map((i) => ({ ref: i.ref, title: itemWords(i).title, session: i.session?.name })),
    tickets: palette.tickets.value,
  }))

  /** Reads the open tickets once, the first time the list opens with tickets among the kinds it offers. */
  function opened(kinds: readonly string[] = []) {
    if (ticketsAsked || (kinds.length && !kinds.includes('ticket'))) return
    ticketsAsked = true
    void palette.loadTickets().catch(() => {})
  }

  /** The matches for what follows the `@`, of the ref kinds asked (none: all), the page's own run first. */
  function matches(query: string, kinds: readonly string[] = [], near?: string): Mention[] {
    return mentionMatches(sources.value, query, kinds, near)
  }

  return { opened, matches }
}
