/**
 * The Alerts panel (AlertsPanel.vue, mounted once in the layout): the switches and the rules for where each kind of event goes.
 * Opened from your menu's "Alerts…", the palette's "Alerts…", the phone's "All alert rules", the feed's Edit routing and `?alerts`
 * on any page of the layout. `returnTo` is where the focus goes back when it closes (your menu's button, which the menu's own
 * closing gave it), since the item that opened it is gone.
 */
export function useAlertsPanel() {
  const open = useState<boolean>('alertsPanelOpen', () => false)
  const returnTo = useState<HTMLElement | null>('alertsPanelReturnTo', () => null)
  function show(from?: HTMLElement | null) {
    returnTo.value = from ?? null
    open.value = true
  }
  return { open, returnTo, show }
}

/**
 * `?alerts` on a page of the default layout opens the panel and is taken off the address, so a link (an old one to the Events page's
 * Routing tab among them) can open it. Called once, by the layout.
 */
export function useAlertsFromAddress() {
  const route = useRoute()
  const router = useRouter()
  const panel = useAlertsPanel()
  watch(
    () => 'alerts' in route.query,
    (asked) => {
      if (!asked) return
      panel.show()
      const { alerts: _, ...rest } = route.query
      void router.replace({ query: rest, hash: route.hash })
    },
    { immediate: true },
  )
}
