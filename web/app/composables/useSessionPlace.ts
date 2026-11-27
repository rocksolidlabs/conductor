import { sessionPlace } from '~/utils/memberStrip'

/**
 * Where an in-app link to a session goes, by its id (UX3 R72): a member of a person's run at its place inside its crew, as the live
 * list knows the session, else its own page (sessionPlace).
 */
export function useSessionPlace() {
  const live = useAttention()
  return (id: string, q: { tab?: string; open?: string; line?: string | number } = {}) => sessionPlace(live.sessions.value.find((s) => s.id === id), id, q)
}
