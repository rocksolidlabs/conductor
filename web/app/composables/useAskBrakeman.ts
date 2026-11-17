import type { BrakemanRequest } from '~/utils/brakeman'
import { displayBy } from '~/utils/brakeman'
import type { AskBody } from '~/utils/brakemanAsk'
import { askedToast } from '~/utils/brakemanAsk'

/**
 * The one way a door asks Brakeman (the ask bar, the palette's rows, a task's menu, Needs you, Ask again): the request sent through
 * useBrakeman().ask under the person's display name (a label, not identity), so every door sends the same body and gets the same
 * refusal. `tell` sends and says so in a toast, "Asked as br-14" with Open, or the server's refusal: a toast of the person's own
 * press, never one Brakeman raises.
 */
export function useAskBrakeman() {
  const bk = useBrakeman()
  const identity = useIdentity()
  const toast = useToast()

  /** Sends the body under the person's name: the request kept, or the server's refusal thrown. */
  function send(body: AskBody): Promise<BrakemanRequest> {
    return bk.ask({ ...body, by: displayBy(identity.name.value) || undefined })
  }

  /** Sends and tells: the request kept, or null once the refusal's words are shown. */
  async function tell(body: AskBody): Promise<BrakemanRequest | null> {
    try {
      const r = await send(body)
      toast.add(askedToast(r))
      return r
    } catch (e) {
      toast.add({ title: 'Not asked', description: (e as Error).message, icon: 'i-lucide-triangle-alert', color: 'error' })
      return null
    }
  }

  return { send, tell }
}
