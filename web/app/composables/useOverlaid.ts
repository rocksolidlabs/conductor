/** A dialog, a menu or a list of choices open: its keys are its own. A tooltip shown is none of them. */
export const OVERLAY_SELECTOR = '[role="dialog"], [role="menu"], [role="listbox"]'

/**
 * Whether an overlay is open as a key goes down, read before any page key sees the key (a capture listener on the window). A page's
 * keys (defineShortcuts) take it to leave the key alone, not merely to do nothing: a shortcut that matches prevents the key's default
 * before its handler runs, and a menu's Escape (reka's dismissable layer) then does not close it. A menu's item picked by a click holds
 * the focus outside any text field, where the page's keys are live: before this, Escape on a ticket's page left a picker open once an
 * item had been picked.
 */
export function useOverlaid() {
  const overlaid = ref(false)
  const read = () => {
    overlaid.value = !!document.querySelector(OVERLAY_SELECTOR)
  }
  onMounted(() => window.addEventListener('keydown', read, { capture: true }))
  onBeforeUnmount(() => window.removeEventListener('keydown', read, { capture: true }))
  return overlaid
}
