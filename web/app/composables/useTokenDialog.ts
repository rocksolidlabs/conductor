/**
 * The workbench token dialog's open state, shared by the layout that draws it and the controls that open it over a working app: the
 * menu's Workbench token…, the palette, and Set token on the home's and the Sessions page's alerts (the UX review's P18). With a token
 * held, the store's ask alone opens nothing: the connect page shows only when no token is held or the held one was refused.
 */
export function useTokenDialog() {
  const open = useState<boolean>('workbenchTokenDialog', () => false)
  return { open, show: () => (open.value = true) }
}
