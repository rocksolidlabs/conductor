import type { SessionInfo } from '~/composables/useSessions'
import { keepIfHere, placeOfAddress, sessionPlace } from '~/utils/memberStrip'

type Carried = { tab?: string; open?: string; line?: string | number }

/**
 * Where an in-app link to a session goes (UX3 R72): a member of a person's run at its place inside its crew, else its own page
 * (sessionPlace), the session as given or as the live list knows it. A link to the member already open with nothing of its own to
 * carry is the page's own address (keepIfHere): it moves nothing and adds no history. `.address` maps an address the server builds
 * (`/sessions/<id>`) the same way.
 */
export function useSessionPlace() {
  const live = useAttention()
  const router = useRouter()
  const find = (id: string) => live.sessions.value.find((s) => s.id === id)
  const here = () => router.currentRoute.value
  function place(id: string, q: Carried = {}, s?: Pick<SessionInfo, 'crew'> | null): string {
    const to = sessionPlace(s ?? find(id), id, q)
    return Object.values(q).some((v) => v !== undefined && v !== '') ? to : keepIfHere(to, here())
  }
  return Object.assign(place, {
    address: (url: string) => keepIfHere(placeOfAddress(url, find), here()),
  })
}
