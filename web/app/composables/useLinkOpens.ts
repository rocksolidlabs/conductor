import { desktopBridge } from '~/utils/desktop'
import { asOpenEvent, offerWords, routeOpen } from '~/utils/opens'
import type { OpenEvent } from '~/utils/protocol'

/**
 * The links agents open (#436), as the events stream tells them (`open`): in the desktop app, the page claims a pending link and
 * opens it through the bridge in the browser the agent's settings name, and tells the server what became of it; a link no desktop
 * app took is offered in a toast with Open (routeOpen). A page in a plain browser tab starts nothing on its own.
 */
export function useLinkOpens() {
  const api = useSessions()
  const toast = useToast()

  function openOffered(ev: OpenEvent) {
    const bridge = desktopBridge()
    if (bridge?.openUrl) void bridge.openUrl(ev.url, ev.browser ?? null).catch(() => {})
    else window.open(ev.url, '_blank', 'noopener,noreferrer')
  }

  /** Takes the data of an `open` event; sessionName names the session that opened it. */
  function event(payload: unknown, sessionName: (id: string) => string) {
    if (!import.meta.client) return
    const ev = asOpenEvent(payload)
    if (!ev) return
    const bridge = desktopBridge()
    void routeOpen(ev, {
      bridge,
      claim: (id) => api.claimOpen(id),
      result: (id, outcome) => api.openResult(id, outcome),
      offer: (e) => {
        const w = offerWords(sessionName(e.sessionId), e, !!bridge?.openUrl)
        toast.add({ title: w.title, description: w.description, icon: 'i-lucide-external-link', color: 'neutral', actions: [{ label: 'Open', icon: 'i-lucide-external-link', onClick: () => openOffered(e) }] })
      },
      warn: (w) => toast.add({ title: w.title, description: w.description, icon: 'i-lucide-triangle-alert', color: 'warning' }),
    })
  }

  return { event }
}
