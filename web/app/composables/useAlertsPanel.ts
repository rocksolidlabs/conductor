/**
 * The Alerts panel (AlertsPanel.vue, mounted once in the layout): opened from your menu's "Alerts…". `returnTo` is where the focus
 * goes back when it closes (your menu's button, which the menu's own closing gave it), since the item that opened it is gone.
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
