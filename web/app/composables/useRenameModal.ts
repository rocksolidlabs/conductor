/** The session the Rename dialog is open on (#318): its id, and its name as the dialog opened. */
export interface RenameTarget {
  id: string
  name: string
}

/** Open state of the Rename dialog, shared by a sidebar row's menu, the session page and the palette: one dialog, in the layout. */
export function useRenameModal() {
  const open = useState<boolean>('renameOpen', () => false)
  const target = useState<RenameTarget | null>('renameTarget', () => null)
  // Counts the dialog's openings: a save remembers the one it was sent from, and a reply that comes back after the dialog was
  // dismissed and opened again (on another session, or the same) leaves that later opening alone.
  const opening = useState<number>('renameOpening', () => 0)
  return {
    open,
    target,
    opening,
    show: (s: RenameTarget) => {
      target.value = { id: s.id, name: s.name }
      opening.value++
      open.value = true
    },
  }
}
